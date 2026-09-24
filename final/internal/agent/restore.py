# restore — 启动时从持久化层恢复 agent 运行时状态
#
# 对应 main 分支 internal/application/chat/mem_restore.go：
#   - restore_from_db：恢复偏好 + LTM 全字段（含 ID/CreatedAt/LastAccessed/category/
#     tags/slot_hint/score/embedding）+ 聊天记录
#   - restore_rag_from_db：恢复 RAG chunks
#   - init_knowledge_graph：三件套
#       1) 构造 KGStore（复用同一个 Neo4jClient）并注入 RAG
#       2) 构造 GraphMemory（持有 LTM 反向引用与同一个 Neo4j 客户端）
#       3) sync_prev_id 把 LTM.last_id 同步到 GraphMemory.prev_id
#       4) attachGraph：通过 ltm.set_graph_memory 把 graph 挂到 LTM
#     与 main 一致：不做 bulkIndex；只对齐 prev_id 即可。
#
# 所有恢复动作做异常吞没（best-effort），任意一项失败不阻塞 agent 启动。
import logging
from typing import Optional

logger = logging.getLogger(__name__)


def restore_from_db(agent):
    """启动时从 PostgreSQL 恢复跨会话的偏好、长期记忆和聊天记录。

    与 main 分支 mem_restore.go restoreFromDB 顺序一致：
      1) 偏好：Preference.__init__ 内已自动 load_from_storage，无需额外动作；
      2) 长期记忆：调 ``ltm.load_from_storage`` 把 PG 全量 row 还原（含 6 字段）；
      3) 聊天记录：从 chat_history 取最近 N 条（每轮 = user + assistant）回放到 STM。
    """
    ltm = getattr(agent, "ltm", None)
    if ltm is not None and hasattr(ltm, "load_from_storage"):
        try:
            ltm.load_from_storage()
        except Exception as e:
            logger.warning("⚠️  ltm.load_from_storage 失败: %s", e)

    chat_repo = getattr(agent, "chat_repo", None)
    if chat_repo is not None:
        chat_limit = agent.cfg.short_term_max_turns * 2  # 每轮 = user + assistant
        try:
            history = chat_repo.load(chat_limit)
        except Exception as e:
            logger.warning("⚠️  chat_history.load 失败: %s", e)
            history = []
        for h in history or []:
            role = getattr(h, "role", None)
            content = getattr(h, "content", None)
            if role and content:
                try:
                    agent.stm.add(role, content)
                except Exception:
                    pass
        if history:
            logger.info("✅ 聊天记录恢复：%d 条", len(history))


def restore_rag_from_db(agent):
    """从 PostgreSQL 加载持久化的 RAG chunks 到内存索引。"""
    try:
        rag = getattr(agent, "rag", None)
        if rag is not None and hasattr(rag, "_check_existing_chunks"):
            rag._check_existing_chunks()
    except Exception as e:
        logger.warning("⚠️  restore_rag_from_db 失败: %s", e)


def init_knowledge_graph(agent):
    """初始化 Neo4j 知识图谱并接入 RAG / GraphMemory（三件套）。

    与 main 分支 mem_restore.go initKnowledgeGraph 顺序一致：
      1) 构造 Neo4jClient + KGStore 并注入 RAG；
      2) 复用同一个 Neo4jClient 构造 GraphMemory，并持有 LTM 反向引用；
      3) graph_memory.sync_prev_id() 把 LTM 已恢复的最大 id 同步到图侧 prev_id；
      4) ltm.set_graph_memory(graph_memory) 完成 attachGraph，反向回注 ltm。

    Neo4j 不可用时降级：agent.kg = agent.graph_memory = None，所有 hook 静默 no-op。
    """
    agent.kg = None
    agent.graph_memory = None

    try:
        from internal.graph.kgstore import KGStore
        from internal.platform.neo4j import Neo4jClient
    except Exception as e:
        logger.info("ℹ️  KGStore/Neo4jClient 模块不可用 (%s)，跳过知识图谱初始化", e)
        return

    try:
        client = Neo4jClient(agent.cfg)
    except Exception as e:
        logger.info("ℹ️  Neo4j 不可用 (%s)，记忆/RAG 退化为非图模式", e)
        return

    # 1) KGStore + RAG 注入
    def _llm_fn(system_prompt: str, user_msg: str) -> str:
        from internal.llm.llm import Message
        return agent.llm.chat(
            [Message(role="user", content=user_msg)],
            system_prompt=system_prompt,
        )

    try:
        kg = KGStore(agent.cfg, client, llm_fn=_llm_fn)
    except Exception as e:
        logger.info("ℹ️  KGStore 构造失败 (%s)，记忆/RAG 退化为非图模式", e)
        return
    agent.kg = kg

    rag = getattr(agent, "rag", None)
    if rag is not None and hasattr(rag, "set_kg_store"):
        try:
            rag.set_kg_store(kg)
        except Exception as e:
            logger.warning("⚠️  rag.set_kg_store 失败: %s", e)

    # 2) GraphMemory：复用同一个 Neo4jClient + 持有 LTM 反向引用
    try:
        from internal.memory.graph_memory import GraphMemory
    except Exception as e:
        logger.warning("⚠️  GraphMemory 模块不可用: %s", e)
        return

    sim = float(
        getattr(agent.cfg, "memory_consolidation_similarity", 0.7) or 0.7
    )
    ltm = getattr(agent, "ltm", None)
    try:
        graph_memory = GraphMemory(
            agent.cfg, client, llm=_llm_fn, sim_threshold=sim, ltm=ltm
        )
    except Exception as e:
        logger.warning("⚠️  GraphMemory 构造失败: %s", e)
        return

    # 3) sync_prev_id：从 LTM 当前 last_id 对齐图侧 prev_id（DB 恢复后必做）
    try:
        graph_memory.sync_prev_id()
    except Exception as e:
        logger.warning("⚠️  graph_memory.sync_prev_id 失败: %s", e)

    # 4) attachGraph：把图层挂到 LTM（set_graph_memory 内部会自动反向 set_ltm）
    if ltm is not None and hasattr(ltm, "set_graph_memory"):
        try:
            ltm.set_graph_memory(graph_memory)
        except Exception as e:
            logger.warning("⚠️  ltm.set_graph_memory 失败: %s", e)

    agent.graph_memory = graph_memory
    logger.info("🕸️  知识图谱已就绪（KG + GraphMemory 已挂载）")
