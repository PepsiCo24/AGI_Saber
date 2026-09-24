# agent — Python 版统一智能体（重写后版本）
#
# 主分支 Go 版 internal/agent/agent.go 拆出的诸多职责被分散到同目录下的：
#   - router.py          — chat / tool / react / rag 模式路由
#   - planner.py         — ReAct 模式下的 Planner LLM
#   - restore.py         — 启动期从 PG 恢复偏好/长期记忆/聊天记录 + KG 初始化
#   - cancel.py          — 取消令牌注册表 + go_safe
#   - init_sandbox.py    — 沙箱 + exec_command 工具初始化
#   - memory_writer.py   — 异步记忆写入 + 回复事实抽取
#   - status.py          — 系统状态视图聚合
#
# 本文件只负责：构造 + 路由分派 + 图调度入口（react 模式走 GraphRuntime）。
import json
import logging
import re
import threading
import time
from dataclasses import asdict
from dataclasses import dataclass, field
from datetime import date, datetime
from typing import Any, Dict, List, Optional

from config.config import APIConfig
from internal.document.library import DOCUMENT_SOURCE_AGENT, WriteRequest
from internal.infra.infra import Infrastructure
from internal.llm.llm import Client as LLMClient, Message
from internal.memory.memory import LongTerm, Preference, ShortTerm
from internal.memory.mem_stack import ConsolidationConfig, MemoryStack
from internal.promptctx import (
    ConstraintsSource,
    ContextAssembler,
    PlannerSnapshot,
    PlannerSource,
    Policy,
    ProfileSource,
    Query,
    RecallSource,
    SourceRegistry,
    StepObservation,
    TaskMemBuffer,
    TaskMemSource,
    ToolCallTrace,
    ToolStateSource,
    ToolStateTracker,
    default_schemas,
)
from internal.rag.rag import Engine as RAGEngine
from internal.rag.reranker import LLMReranker
from internal.rag.rewriter import HistoryMessage, LLMRewriter
from internal.tools.tools import Tool, ToolExecutor, default_tools, new_mcp_tool

from .cancel import CancelRegistry, go_safe
from .graph_runtime import GraphConfig, GraphRuntime
from .init_sandbox import init_sandbox
from .memory_writer import (
    AsyncMemoryWriter,
    async_update_memory,
    extract_memory_from_reply,
    maybe_consolidate_memory,
)
from .restore import init_knowledge_graph, restore_from_db, restore_rag_from_db
from .router import detect_tool, need_rag, need_react, need_react_from_tools, need_tool
from .planner import llm_plan_graph
from .status import infra_status, status as build_status
from .subagents import register_builtin_subagents

logger = logging.getLogger(__name__)


class StepType:
    THOUGHT = "Thought"
    ACTION = "Action"
    OBSERVATION = "Observation"
    FINAL_ANSWER = "Final Answer"


@dataclass
class ReActStep:
    type: str
    content: str
    tool: str = ""
    params: Optional[Dict[str, str]] = None


@dataclass
class ChatOptions:
    use_rag: bool = False
    selected_tools: Optional[List[str]] = None
    explicit: bool = False


@dataclass
class Response:
    query: str
    answer: str = ""
    mode: str = "chat"
    steps: List[ReActStep] = field(default_factory=list)
    tool_call: Optional[Dict[str, Any]] = None
    search_results: List[dict] = field(default_factory=list)
    task: Optional[dict] = None
    extracted_info: str = ""
    short_term_count: int = 0
    long_term_count: int = 0
    preferences: Dict[str, str] = field(default_factory=dict)
    interrupted: bool = False


class UnifiedAgent:
    """统一智能体入口。负责装配各子模块、路由分派与 ReAct 推理循环。"""

    def __init__(self, cfg: APIConfig, inf: Infrastructure):
        self.cfg = cfg
        self.inf = inf
        self.llm = LLMClient(cfg)
        self.stm = ShortTerm(cfg.short_term_max_turns)
        self.ltm = LongTerm(cfg, inf)
        self.preference = Preference("default_user", inf)
        # 三层记忆 + 偏好聚合容器（与 main memoryStack 对齐）。
        # graph_memory 由 init_knowledge_graph 在末尾通过 attach_graph 注入。
        self.mem = MemoryStack(stm=self.stm, ltm=self.ltm, preference=self.preference)
        # 用 ConsolidationConfig（dataclass + memory_consolidation_* 别名）替换裸 cfg
        # 喂给 LongTerm；保留对 APIConfig 的引用便于后续访问其它字段。
        try:
            self.ltm.set_consolidation_config(ConsolidationConfig.from_api_config(cfg))
        except Exception as e:
            logger.warning("⚠️  ConsolidationConfig 装配失败: %s", e)
        # 暴露 chat_history 仓储，供 restore.py 与 _save_chat_history 复用。
        self.chat_repo = getattr(getattr(inf, "repo", None), "chat_history", None)

        # RAG 引擎构造失败不致命：降级为禁用知识库
        try:
            self.rag = RAGEngine(cfg, inf, self.llm)
        except Exception as e:
            logger.warning("⚠️  RAG 引擎初始化失败: %s（已禁用知识库）", e)
            self.rag = None

        # 默认工具集；planner / sandbox 可后续追加
        self.tool_executor = ToolExecutor(default_tools(cfg=cfg, llm=self.llm))
        self.subagents = register_builtin_subagents(self)

        # 注册依赖 agent 上下文的内置工具（rag_search 闭包，与 Go 版
        # registerBuiltinTools 对齐）。search_web 已在 default_tools 中
        # 通过 search_web_factory 处理 Tavily / LLM 降级，无需重复注册。
        self._register_builtin_tools()

        # 取消令牌注册表 + 兼容旧接口的 process-level cancel event
        self._cancel_registry = CancelRegistry()
        self._memory_lock = threading.Lock()

        # 异步记忆写入器（单 worker 线程串行化）
        self.memory_writer = AsyncMemoryWriter()

        # 接通 LLM embed / RAG generate
        try:
            self.ltm.set_embed_fn(self.llm.embed)
        except Exception as e:
            logger.warning("⚠️  LTM embed 函数注入失败: %s", e)
        if self.rag is not None:
            try:
                self.rag.set_generate_fn(self._llm_generate)
                if getattr(cfg, "rag_rewrite_enabled", False):
                    self.rag.set_rewriter(LLMRewriter(self._llm_generate, cfg.rag_rewrite_num_queries))
                if getattr(cfg, "rag_rerank_enabled", False):
                    self.rag.set_reranker(LLMReranker(self._llm_generate, cfg.rag_rerank_preview_len))
            except Exception as e:
                logger.warning("⚠️  RAG generate 函数注入失败: %s", e)

        # 加载持久化的长期记忆 + chat_history（best-effort）
        # 注：实际还原由 restore_from_db 完成，此处的 ltm.load_from_storage
        # 仅作为冷启动 RAG 索引装载前的快速预热——已合并到 _bootstrap_concurrent。

        # bootstrap 4 路并发（与 main bootstrapConcurrent 对齐）：
        #   - ragchunk.init(dim)         建 Milvus collection + ES 索引
        #   - restore_from_db            从 PG 恢复偏好 / LTM / 聊天记录
        #   - restore_rag_from_db        从 PG 恢复 RAG chunks
        #   - init_sandbox               Docker 探测 + exec_command 注册
        # 主线程同时同步注册 builtin 工具（rag_search 已在 _register_builtin_tools
        # 中提前完成，无须再放进并发组）。
        self.sandbox = None
        self._bootstrap_concurrent()

        # 知识图谱：必须在 restore_from_db 完成后串行执行（依赖 ltm 已就绪）
        self.kg = None
        try:
            init_knowledge_graph(self)
        except Exception as e:
            logger.warning("⚠️  init_knowledge_graph 失败: %s", e)
            self.kg = None
        # KG 就绪后把 graph_memory 挂回 mem stack（对应 main attachGraph）
        self.mem.attach_graph(getattr(self, "graph_memory", None))

        # 快照计数器（每 N 轮序列化 agent_state 到 PG）
        self._turn_count = 0
        self._snapshot_every = max(1, getattr(cfg, "snapshot_every_turns", 5) or 5)
        self._build_prompt_context()

        logger.info("✅ UnifiedAgent 初始化完成")

    def _bootstrap_concurrent(self) -> None:
        """与 main bootstrapConcurrent 对齐的 4 路并发启动。

        每个子任务自行做异常吞没，整体串行总耗时被压缩到最慢一项。
        """
        def _ragchunk_init():
            try:
                repo = getattr(getattr(self.inf, "repo", None), "ragchunk", None)
                if repo is not None and hasattr(repo, "init"):
                    repo.init(int(self.cfg.rag_milvus_dim or 1024))
            except Exception as e:
                logger.warning("⚠️  ragchunk.init 失败: %s", e)

        def _restore_db():
            try:
                restore_from_db(self)
            except Exception as e:
                logger.warning("⚠️  restore_from_db 失败: %s", e)

        def _restore_rag():
            try:
                restore_rag_from_db(self)
            except Exception as e:
                logger.warning("⚠️  restore_rag_from_db 失败: %s", e)

        def _init_sandbox():
            try:
                init_sandbox(self)
            except Exception as e:
                logger.warning("⚠️  init_sandbox 失败: %s", e)
                self.sandbox = None

        threads = [
            threading.Thread(target=_ragchunk_init, name="bootstrap:ragchunk", daemon=True),
            threading.Thread(target=_restore_db, name="bootstrap:restore-db", daemon=True),
            threading.Thread(target=_restore_rag, name="bootstrap:restore-rag", daemon=True),
            threading.Thread(target=_init_sandbox, name="bootstrap:sandbox", daemon=True),
        ]
        for t in threads:
            t.start()
        for t in threads:
            t.join()

    # ── 对外 API ────────────────────────────────────────────────────────────

    def cancel(self):
        """触发所有 in-flight 请求的取消。"""
        self._cancel_registry.cancel_all()

    def process(self, query: str) -> Response:
        return self.process_with_options(query, ChatOptions(explicit=False))

    def process_with_options(self, query: str, opts: ChatOptions) -> Response:
        token, unregister = self._cancel_registry.register()
        try:
            return self._dispatch(query, opts, token)
        finally:
            unregister()

    def process_stream(self, query: str, opts: ChatOptions, on_event) -> Response:
        token, unregister = self._cancel_registry.register()
        try:
            return self._dispatch(query, opts, token, on_event)
        finally:
            unregister()

    def route(self, user_input: str, use_rag: bool = False) -> str:
        return self.process_with_options(user_input, ChatOptions(use_rag=use_rag, explicit=False)).answer

    def get_tools(self) -> List[Dict[str, Any]]:
        return self.tool_executor.get_tool_descriptions()

    def add_tool(self, tool: Tool):
        self.tool_executor.add_tool(tool)

    def _register_builtin_tools(self) -> None:
        """注册依赖 agent 自身字段的内置工具。

        与 Go 版 UnifiedAgent.registerBuiltinTools 对齐：
        rag_search 必须在 self.rag 构造之后注册，因为闭包要捕获 self.rag。
        """
        def _rag_search(params: Dict[str, Any]) -> str:
            query = params.get("query", "") if isinstance(params, dict) else ""
            if not query:
                query = "相关内容"
            if self.rag is None:
                raise RuntimeError("RAG 引擎未初始化")
            if not getattr(self.rag, "loaded", False):
                raise RuntimeError("知识库为空，请先在「私人黑洞」上传文档")
            answer, _ = self._run_rag_query(query)
            return answer

        self.tool_executor.add_tool(Tool(
            name="rag_search",
            description="从私人黑洞（个人知识库）中检索相关文档内容",
            params=[{"name": "query", "type": "string", "description": "检索关键词或问题"}],
            func=_rag_search,
        ))
        self._register_document_tools()

    def _register_document_tools(self) -> None:
        for tool in [
            self._write_document_tool(),
            self._list_documents_tool(),
            self._read_document_tool(),
            self._ingest_document_tool(),
        ]:
            self.tool_executor.add_tool(tool)

    def _write_document_tool(self) -> Tool:
        return Tool(
            name="write_document",
            description="将 Markdown 文档写入本地文档库，可选择同步入库 RAG。适合保存报告、总结、研究结果。",
            params=[
                {"name": "title", "type": "string", "description": "文档标题"},
                {"name": "content_md", "type": "string", "description": "Markdown 正文"},
                {"name": "doc_type", "type": "string", "description": "文档类型，如 report/note/summary"},
                {"name": "source", "type": "string", "description": "来源，如 agent_generated"},
                {"name": "summary", "type": "string", "description": "简短摘要"},
                {"name": "ingest_to_rag", "type": "boolean", "description": "是否写入后立即进入 RAG 索引"},
            ],
            func=lambda params: _json_string(self.write_document(
                WriteRequest(
                    title=_param_string(params, "title"),
                    doc_type=_param_string_default(params, "doc_type", "report"),
                    source=_param_string_default(params, "source", DOCUMENT_SOURCE_AGENT),
                    created_by="agent",
                    content_md=_param_string(params, "content_md") or _param_string(params, "content"),
                    summary=_param_string(params, "summary"),
                    metadata={"tool": "write_document"},
                ),
                _param_bool(params, "ingest_to_rag"),
            )),
        )

    def _list_documents_tool(self) -> Tool:
        return Tool(
            name="list_documents",
            description="列出本地文档库中的文档。",
            params=[],
            func=lambda params: _json_string({"documents": self.list_documents()}),
        )

    def _read_document_tool(self) -> Tool:
        return Tool(
            name="read_document",
            description="读取本地文档库中的指定文档最新版本。",
            params=[{"name": "document_id", "type": "string", "description": "文档 ID"}],
            func=lambda params: _json_string(self.get_document(_param_string(params, "document_id"))),
        )

    def _ingest_document_tool(self) -> Tool:
        return Tool(
            name="ingest_document",
            description="将本地文档库中的文档版本切分并写入 RAG 索引。",
            params=[
                {"name": "document_id", "type": "string", "description": "文档 ID"},
                {"name": "version_id", "type": "string", "description": "版本 ID，不填则使用最新版本"},
            ],
            func=lambda params: _json_string(self.ingest_document(
                _param_string(params, "document_id"),
                _param_string(params, "version_id"),
            )),
        )

    def _document_store(self):
        store = getattr(getattr(getattr(self, "inf", None), "repo", None), "documents", None)
        if store is None:
            raise RuntimeError("document library not configured")
        return store

    def write_document(self, req: WriteRequest, ingest_to_rag: bool = False) -> Dict[str, Any]:
        wr = self._document_store().write(req)
        out = _to_jsonable(wr)
        if ingest_to_rag:
            out["ingest"] = self._ingest_content(
                wr.version.content_md,
                document_id=wr.document.id,
                version_id=wr.version.id,
                section=wr.document.doc_type,
            )
        return out

    def list_documents(self) -> List[Any]:
        return self._document_store().list()

    def get_document(self, document_id: str) -> Dict[str, Any]:
        doc, ver = self._document_store().get(document_id)
        return {"document": doc, "version": ver}

    def ingest_document(self, document_id: str, version_id: str = "") -> Dict[str, Any]:
        store = self._document_store()
        if version_id:
            ver = store.get_version(version_id)
        else:
            _, ver = store.get(document_id)
        doc_id = document_id or ver.document_id
        return self._ingest_content(
            ver.content_md,
            document_id=doc_id,
            version_id=ver.id,
            section="document",
        )

    def _ingest_content(self, content: str, document_id: str, version_id: str, section: str) -> Dict[str, Any]:
        if self.rag is None:
            raise RuntimeError("RAG 引擎未初始化")
        chunk_count = self.rag.ingest(content)
        return {
            "chunk_count": int(chunk_count or 0),
            "document_id": document_id,
            "version_id": version_id,
            "section": section,
        }

    def register_mcp_tool(self, name: str, description: str, params: List[Dict[str, str]], func):
        self.add_tool(new_mcp_tool(name, description, params, func))

    def rag_ingest(self, document: str) -> int:
        if self.rag is None:
            return 0
        return self.rag.ingest(document)

    def rag_query(self, question: str) -> tuple:
        if self.rag is None:
            return ("RAG 不可用", [])
        return self.rag.query(question)

    def status(self) -> Dict[str, Any]:
        return build_status(self)

    def infra_status(self) -> Dict[str, str]:
        return infra_status(self)

    # ── 调度主循环 ─────────────────────────────────────────────────────────

    def _dispatch(self, query: str, opts: ChatOptions, token, on_event=None) -> Response:
        """三段式编排：prepare → dispatch → finalize（与 main runOnce 对齐）。"""
        pr = self._prepare(query, opts)
        resp = Response(query=query, mode=pr["mode"])
        resp.extracted_info = pr["extracted"]
        if resp.extracted_info:
            _emit(on_event, "memory", {"extracted_info": resp.extracted_info})
        _emit(on_event, "route", {"mode": resp.mode})

        if token.is_cancelled():
            resp.interrupted = True
            resp.answer = "[已中断] 请求在开始前被取消"
            _emit(on_event, "done", _to_jsonable(resp))
            return resp

        self._dispatch_mode(pr, resp, token, on_event)

        if token.is_cancelled():
            resp.interrupted = True

        self._finalize(query, resp)
        _emit(on_event, "done", _to_jsonable(resp))
        return resp

    # ── prepare ──────────────────────────────────────────────────────────────

    def _prepare(self, query: str, opts: ChatOptions) -> Dict[str, Any]:
        """STM 写入 + 偏好提取 + 路由决策 + 上下文装配 + 历史构建。"""
        self.stm.add("user", query)
        self._save_chat_history("user", query)

        # 偏好/记忆抽取（同步规则即时回显 + 异步 LLM 扩展）
        # 注：async_update_memory 的写入路径直接修改 resp.extracted_info，
        # 这里复用一个 Response 占位以承接同步部分的输出。
        ph = Response(query=query)
        async_update_memory(self, query, ph)

        rag_loaded = bool(self.rag and getattr(self.rag, "loaded", False))
        if opts.explicit:
            if opts.selected_tools:
                route_tools = self._filter_tools(opts.selected_tools)
                mode = "react" if need_react_from_tools(query, route_tools) else "tool"
            elif opts.use_rag and rag_loaded:
                mode, route_tools = "rag", None
            else:
                mode, route_tools = "chat", None
        else:
            if need_react(query):
                mode, route_tools = "react", self.tool_executor.snapshot()
            elif need_tool(query):
                mode, route_tools = "tool", self.tool_executor.snapshot()
            elif need_rag(query, rag_loaded):
                mode, route_tools = "rag", None
            else:
                mode, route_tools = "chat", None

        mem_prefix = self._build_memory_system_prefix(query)
        hist_msgs = self._build_history_messages(query)

        return {
            "query": query,
            "mode": mode,
            "route_tools": route_tools,
            "mem_prefix": mem_prefix,
            "hist_msgs": hist_msgs,
            "extracted": ph.extracted_info,
        }

    # ── dispatch ─────────────────────────────────────────────────────────────

    def _dispatch_mode(self, pr: Dict[str, Any], resp: Response, token, on_event=None) -> None:
        """按 mode 分发到对应 handler，把结果填回 resp。"""
        mode = pr["mode"]
        query = pr["query"]
        mem_prefix = pr["mem_prefix"]
        hist_msgs = pr["hist_msgs"]
        route_tools = pr["route_tools"]
        resp.extracted_info = pr["extracted"]

        if mode == "react":
            resp.answer, resp.steps, resp.task = self._run_react_with_tools(
                query, route_tools, mem_prefix, hist_msgs, token, on_event
            )
        elif mode == "tool":
            resp.answer, resp.tool_call = self._run_tool_from_set(
                query, route_tools, mem_prefix, hist_msgs, token, on_event
            )
        elif mode == "rag":
            resp.answer, resp.search_results = self._run_rag_query(query)
            _emit(on_event, "rag_result", {"search_results": resp.search_results})
            _emit(on_event, "token", {"content": resp.answer})
        else:
            resp.answer = self._chat_response(mem_prefix, hist_msgs, token, on_event)

    # ── finalize ─────────────────────────────────────────────────────────────

    def _finalize(self, query: str, resp: Response) -> None:
        """assistant 写回 + 异步记忆抽取 + 异步图感知合并 + 事件发布 + 计数。"""
        self.stm.add("assistant", resp.answer)
        self._save_chat_history("assistant", resp.answer)

        # 异步：从回复中提取事实 → 长期记忆
        self.memory_writer.submit(lambda: extract_memory_from_reply(self, resp.answer))
        # 异步：长期记忆合并/淘汰（有图层时走图感知合并）
        self.memory_writer.submit(lambda: maybe_consolidate_memory(self))

        # 每 N 轮快照一次 agent 状态到 PG
        self._turn_count += 1
        if self._turn_count % self._snapshot_every == 0:
            go_safe("snapshot", lambda: self._save_agent_snapshot(query, resp))

        try:
            self.inf.repo.events.publish(
                "agent.chat",
                json.dumps({"query": query, "mode": resp.mode}, ensure_ascii=False),
            )
        except Exception:
            pass

        resp.short_term_count = self.stm.count()
        resp.long_term_count = len(self.ltm.items)
        resp.preferences = self.preference.get_all()

    # ── Memory / Prompt 拼装 ───────────────────────────────────────────────

    def _llm_generate(self, system_prompt: str, user_msg: str) -> str:
        return self.llm.chat([Message(role="user", content=user_msg)], system_prompt=system_prompt)

    def _build_prompt_context(self) -> None:
        self.task_mem = TaskMemBuffer(20)
        self.tool_tracker = ToolStateTracker(10)
        registry = SourceRegistry()
        registry.register(ProfileSource(self.preference, self.ltm))
        registry.register(PlannerSource(self._planner_snapshot))
        registry.register(TaskMemSource(self.task_mem))
        registry.register(ToolStateSource(lambda: self.tool_executor.snapshot(), self.tool_tracker))
        registry.register(ConstraintsSource([
            Policy(pattern="rm -rf", reason="禁止破坏性删除命令", level="block"),
            Policy(pattern="sudo", reason="禁止提权命令", level="block"),
        ]))
        registry.register(RecallSource(self.ltm))
        self.prompt_assembler = ContextAssembler(default_schemas(), registry)

    def _planner_snapshot(self):
        task = self._cancel_registry.current_task() if hasattr(self, "_cancel_registry") else None
        if not task:
            return None
        steps = task.get("steps") or []
        return PlannerSnapshot(
            task_id=task.get("task_id", ""),
            query=task.get("query", ""),
            status=task.get("status", ""),
            phase=task.get("phase", ""),
            total_steps=len(steps),
            current_step=task.get("current_step", 0),
            interrupted_at=task.get("interrupted_at", ""),
        )

    def _build_context_prefix(self, query: str, mode: str = "chat") -> str:
        if not hasattr(self, "prompt_assembler"):
            self._build_prompt_context()
        try:
            return self.prompt_assembler.assemble(Query(text=query, mode=mode)).render()
        except Exception as e:
            logger.warning("⚠️  promptctx 装配失败，降级到旧记忆前缀: %s", e)
            return self._build_memory_system_prefix(query)

    def push_task_mem(self, obs: StepObservation) -> None:
        if hasattr(self, "task_mem"):
            self.task_mem.push(obs)

    def record_tool_call(self, trace: ToolCallTrace) -> None:
        if hasattr(self, "tool_tracker"):
            self.tool_tracker.record(trace)

    def save_snapshot(self, task: dict) -> None:
        task_id = task.get("task_id", f"task_{int(time.time())}") if isinstance(task, dict) else f"task_{int(time.time())}"
        # 持锁追加到 cancel registry（与 main taskRuntime.appendSnapshot 对齐）
        if isinstance(task, dict):
            self._cancel_registry.append_snapshot(dict(task))
        try:
            self.inf.repo.snapshot.save(task_id, json.dumps(task, ensure_ascii=False))
        except Exception as e:
            logger.warning("⚠️  快照写入失败: %s", e)

    def snapshot_list(self) -> List[dict]:
        """返回当前任务的内存快照列表（对应 main snapshotList）。"""
        return self._cancel_registry.snapshot_list()

    def _save_chat_history(self, role: str, content: str) -> None:
        """best-effort 写聊天记录。优先 chat_repo，其次 inf.repo.chat_history。"""
        chat_repo = getattr(self, "chat_repo", None)
        if chat_repo is None:
            chat_repo = getattr(getattr(self.inf, "repo", None), "chat_history", None)
        if chat_repo is not None and hasattr(chat_repo, "save"):
            try:
                chat_repo.save(role, content)
            except Exception:
                pass

    def _build_memory_system_prefix(self, query: str = "") -> str:
        parts: List[str] = []
        prefs = self.preference.get_all()
        if prefs:
            parts.append(f"用户偏好: {json.dumps(prefs, ensure_ascii=False)}")
        memories = self.ltm.recall(query, self.cfg.long_term_top_k) if query else []
        if memories:
            parts.append("相关记忆:\n" + "\n".join(f"- {m.content}" for m in memories))
        return "\n".join(parts)

    def _recent_history_for_rag(self) -> List[HistoryMessage]:
        return [HistoryMessage(role=m["role"], content=m["content"]) for m in self.stm.get()]

    def _run_rag_query(self, query: str):
        if self.rag is None:
            return "RAG 不可用", []
        if hasattr(self.rag, "query_with_history"):
            return self.rag.query_with_history(query, self._recent_history_for_rag())
        return self.rag.query(query)

    def _build_history_messages(self, query: str) -> List[Message]:
        msgs = [Message(role=m["role"], content=m["content"]) for m in self.stm.get()]
        if not msgs or msgs[-1].content != query:
            msgs.append(Message(role="user", content=query))
        return msgs

    def _chat_response(self, mem_prefix: str, hist_msgs: List[Message], token=None, on_event=None) -> str:
        system_prompt = "你是一个简洁的AI助手。结合你掌握的用户信息，使回答更个性化。"
        if mem_prefix:
            system_prompt = mem_prefix + "\n\n" + system_prompt
        return self._chat_llm(system_prompt, hist_msgs, token, on_event)

    def _chat_llm(self, system_prompt: str, messages: List[Message], token=None, on_event=None) -> str:
        if on_event is None:
            return self.llm.chat(messages, system_prompt=system_prompt)
        return self.llm.chat_stream_context(
            token,
            system_prompt,
            messages,
            on_token=lambda content: _emit(on_event, "token", {"content": content}),
        )

    # ── 工具调用（tool 模式） ──────────────────────────────────────────────

    def _filter_tools(self, names: List[str]) -> Dict[str, Tool]:
        return self.tool_executor.filter_tools(names)

    def _parse_tool_params(self, tool_name: str, user_input: str) -> Dict[str, str]:
        params: Dict[str, str] = {}
        if tool_name == "get_weather":
            match = re.search(r"(天气|温度)\s*([^\s,，。？?!！]+)", user_input)
            if match:
                params["city"] = match.group(2)
        elif tool_name in {"search_web", "rag_search"}:
            match = re.search(r"(搜索|查找|知识|文档)\s*(.*)", user_input)
            if match and match.group(2).strip():
                params["query"] = match.group(2).strip()
            else:
                params["query"] = user_input
        return params

    # 偏好键 → 候选工具参数名（与 main 分支 fillParamsFromPreference 完全一致）
    _PREFERENCE_PARAM_MAP = {
        "城市": ("city", "location", "location_name"),
        "时区": ("timezone", "tz", "time_zone"),
        "姓名": ("name", "username", "user_name"),
        "语言": ("language", "lang"),
        "国家": ("country", "nation"),
    }

    def _fill_params_from_preference(self, params: Dict[str, Any]) -> None:
        """在工具 Execute 之前用偏好补齐空槽位（不覆盖既有非空值）。

        与 main 分支 UnifiedAgent.fillParamsFromPreference 对齐：取偏好快照后
        按 5 键映射表逐个尝试填入候选参数名，仅当对应槽位缺失或为空字符串时才赋值。
        """
        if not isinstance(params, dict):
            return
        try:
            snapshot = self.preference.get_all() or {}
        except Exception:
            return
        if not snapshot:
            return
        for pref_key, candidates in self._PREFERENCE_PARAM_MAP.items():
            value = snapshot.get(pref_key)
            if value is None or str(value) == "":
                continue
            for name in candidates:
                existing = params.get(name)
                if existing is None or str(existing) == "":
                    params[name] = value

    def _run_tool_from_set(self, query: str, tools_map: Dict[str, Tool], mem_prefix: str, hist_msgs: List[Message], token=None, on_event=None):
        tool_name = detect_tool(query, tools_map)
        if not tool_name:
            return self._chat_response(mem_prefix, hist_msgs, token, on_event), None
        params = self._parse_tool_params(tool_name, query)
        # 偏好补全：在 tool_executor.call 之前注入偏好（对应 Go 版 fillParamsFromPreference）
        self._fill_params_from_preference(params)
        result = self.tool_executor.call(tool_name, params)
        answer = result.content if result.success else f"工具调用失败: {result.error}"
        tool_call = {
            "tool_name": tool_name,
            "params": params,
            "tool_result": result.content,
            "success": result.success,
            "error": result.error,
        }
        _emit(on_event, "tool_call", tool_call)
        if result.success:
            system_prompt = "你是一个善于综合信息的AI助手。结合你掌握的用户信息，使回答更个性化。"
            if mem_prefix:
                system_prompt = mem_prefix + "\n\n" + system_prompt
            user_msg = f"用户问：{query}\n工具 {tool_name} 返回结果：{result.content}\n请根据结果自然地回答用户。"
            answer = self._chat_llm(system_prompt, [Message(role="user", content=user_msg)], token, on_event)
        return answer, tool_call

    # ── 图调度（统一 react 入口） ──────────────────────────────────────────

    def _run_react_with_tools(self, query: str, tools_map: Dict[str, Tool], mem_prefix: str, hist_msgs: List[Message], token, on_event=None):
        """ReAct 模式入口：与 main 分支 runReAct 行为一致。

        - llm_plan_graph 拿到节点列表；
        - 节点为空 → 直接调 chat LLM 给一句话回答（对应 Go chatLLM 兜底），不再做工具迭代；
        - 节点非空 → 走 GraphRuntime 拓扑分层 + race + 重试；执行结束后用
          _generate_final_answer（对应 Go llmGenerate）合成自然语言回复。
        """
        task = {"task_id": f"task_{int(time.time())}", "query": query, "status": "running", "steps": []}
        self._cancel_registry.set_task(task)
        try:
            plan_nodes = llm_plan_graph(self, query, tools_map, mem_prefix)
            if not plan_nodes:
                # 与 Go runReAct: planNodes 空 → chatLLM 一句话答复
                return self._chat_response(mem_prefix, hist_msgs, token, on_event), [], task

            from internal.graph.task_graph import TaskGraph

            graph = TaskGraph(plan_nodes)
            try:
                graph.validate()
            except Exception:
                for node in plan_nodes:
                    node.depends_on = []
                graph = TaskGraph(plan_nodes)
            cfg = GraphConfig(
                max_parallel=getattr(self.cfg, "graph_max_parallel", 2),
                race_timeout_ms=getattr(self.cfg, "graph_race_timeout_ms", 30000),
                enable_racing=getattr(self.cfg, "graph_enable_racing", True),
            )
            result = GraphRuntime(graph, self, cfg, tools_map, task).execute(token)
            steps = [
                ReActStep(
                    type=StepType.OBSERVATION,
                    content=node.result or node.error,
                    tool=node.tool_name,
                    params=node.params,
                )
                for node in graph.nodes.values()
            ]
            final_answer = self._generate_final_answer(query, steps, mem_prefix, token, on_event)
            steps.append(ReActStep(type=StepType.FINAL_ANSWER, content=final_answer))
            task["status"] = "interrupted" if result.interrupted else "completed"
            task["graph"] = {
                "nodes": [
                    {
                        "id": node.id,
                        "tool": node.tool_name,
                        "status": str(node.status),
                        "result": node.result,
                        "error": node.error,
                    }
                    for node in graph.nodes.values()
                ]
            }
            return final_answer, steps, task
        finally:
            self._cancel_registry.set_task(None)

    def _generate_final_answer(self, query: str, steps: List[ReActStep], mem_prefix: str, token=None, on_event=None) -> str:
        steps_str = "\n".join(f"{s.type}: {s.content}" for s in steps)
        prompt = f"""基于以下推理过程，给出最终答案。

任务: {query}

记忆上下文:
{mem_prefix or '（无）'}

推理过程:
{steps_str}

请用自然语言总结最终答案，不要包含 Action/Final 等关键字。
"""
        messages = [Message(role="user", content=prompt)]
        return self._chat_llm("你是一个总结助手，能够基于推理过程给出简洁的最终答案。", messages, token, on_event)

    def _save_agent_snapshot(self, query: str, resp: Response):
        """每 N 轮把 agent 整体状态序列化到 PG（含路由 mode/计数/偏好）。"""
        snapshot = {
            "task_id": f"agent_{int(time.time())}",
            "query": query,
            "mode": resp.mode,
            "short_term_count": resp.short_term_count,
            "long_term_count": resp.long_term_count,
            "preferences": resp.preferences,
            "timestamp": time.time(),
        }
        try:
            self.inf.repo.snapshot.save(snapshot["task_id"], json.dumps(snapshot, ensure_ascii=False))
        except Exception as e:
            logger.warning("⚠️  agent 快照写入失败: %s", e)

    # ── 生命周期 ────────────────────────────────────────────────────────────

    def close(self):
        try:
            self.memory_writer.stop()
        except Exception:
            pass


def _param_string(params: Dict[str, Any], key: str) -> str:
    if not isinstance(params, dict):
        return ""
    value = params.get(key)
    if value is None:
        return ""
    return str(value).strip()


def _param_string_default(params: Dict[str, Any], key: str, fallback: str) -> str:
    value = _param_string(params, key)
    return value if value else fallback


def _param_bool(params: Dict[str, Any], key: str) -> bool:
    if not isinstance(params, dict):
        return False
    value = params.get(key)
    if isinstance(value, bool):
        return value
    if isinstance(value, str):
        return value.strip().lower() in {"true", "1", "yes", "y", "on"}
    return bool(value)


def _json_string(value: Any) -> str:
    return json.dumps(_to_jsonable(value), ensure_ascii=False, indent=2)


def _emit(on_event, event_type: str, data: Any) -> None:
    if on_event is None:
        return
    on_event({"type": event_type, "data": _to_jsonable(data)})


def _to_jsonable(value: Any) -> Any:
    if isinstance(value, (datetime, date)):
        return value.isoformat()
    if isinstance(value, list):
        return [_to_jsonable(item) for item in value]
    if isinstance(value, tuple):
        return [_to_jsonable(item) for item in value]
    if isinstance(value, dict):
        return {str(k): _to_jsonable(v) for k, v in value.items()}
    if hasattr(value, "__dataclass_fields__"):
        return _to_jsonable(asdict(value))
    return value
