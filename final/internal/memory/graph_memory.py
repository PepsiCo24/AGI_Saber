# graph_memory — 长期记忆与 Neo4j 知识图谱的双向同步层。
#
# 节点类型：(:Memory {mem_id, content, importance})
# 边类型：
#   FOLLOWS      — 时序相邻（上一条对话记忆 → 当前）
#   SIMILAR_TO   — 语义相似度超阈值（Store 时自动连接）
#   CAUSES       — 因果推断（LLM 提取，可选）
#   BELONGS_TO   — 话题归属
#
# 与 Go 版本 graph_memory.go 对齐；cypher 字符串直接照抄。
# 失败降级：Neo4j 不可用时所有方法都是 no-op，不抛异常。
import logging
import math
import threading
from typing import Any, Iterable, List, Optional

from config.config import APIConfig

logger = logging.getLogger(__name__)


def _go_safe(name: str, fn) -> None:
    """启动一个带 panic recover 的后台线程（与 main 分支 goSafe 对齐）。

    任何异常都被吞下并落 logger.warning，不会冒泡到 caller，避免主路径被图写入失败拖累。
    """

    def _runner() -> None:
        try:
            fn()
        except Exception as e:  # pragma: no cover - 防御性日志
            logger.warning("⚠️  goSafe %s 异常: %s", name, e)

    threading.Thread(target=_runner, name=f"go_safe-{name}", daemon=True).start()


def _cosine(a: List[float], b: List[float]) -> float:
    if not a or not b or len(a) != len(b):
        return 0.0
    dot = sum(x * y for x, y in zip(a, b))
    na = math.sqrt(sum(x * x for x in a))
    nb = math.sqrt(sum(x * x for x in b))
    if na == 0 or nb == 0:
        return 0.0
    return dot / (na * nb)


class GraphMemory:
    """长期记忆的图增强层；持有 Neo4jClient 直接操作 :Memory 节点和边。

    Neo4jClient 不可用时所有方法静默返回，调用方无需关心降级。
    """

    def __init__(
        self,
        cfg: APIConfig,
        neo,  # internal.platform.neo4j.Neo4jClient
        llm: Optional[Any] = None,
        sim_threshold: float = 0.7,
        ltm: Optional[Any] = None,
    ):
        self.cfg = cfg
        self.neo = neo
        self.llm = llm
        self.sim_thresh = sim_threshold if sim_threshold > 0 else 0.7
        self.prev_id: int = -1
        # 可选 LTM 反向引用（与 main 分支 GraphMemory 持有 *LongTerm 对齐）。
        # 持有后：sync_prev_id / set_consolidation_config / need_consolidation
        # 都可代理到 LTM；未注入时各代理方法静默 no-op。
        self.ltm = ltm

    def set_ltm(self, ltm: Optional[Any]) -> None:
        """挂载 / 解除 LTM 反向引用。"""
        self.ltm = ltm

    # ─── 可用性 ────────────────────────────────────────────────────────────
    def _available(self) -> bool:
        return self.neo is not None and self.neo.is_real()

    def _mem_id(self, item) -> int:
        """从 Item 提取 mem_id；优先用 item.id，否则用 content hash。"""
        mid = getattr(item, "id", None)
        if mid is None:
            return hash(item.content) & 0x7FFFFFFF
        return int(mid)

    # ─── 原子图操作 ─────────────────────────────────────────────────────────

    def _upsert_memory_node(self, mem_id: int, content: str, importance: float) -> None:
        """插入或更新记忆节点（cypher 照抄 Go 版）。"""
        if not self._available():
            return
        try:
            self.neo.run_cypher(
                """MERGE (m:Memory {mem_id: $id})
                 SET m.content = $content, m.importance = $importance""",
                {"id": int(mem_id), "content": content, "importance": float(importance)},
            )
        except Exception as e:
            logger.warning("⚠️  Neo4j upsertMemoryNode 失败 (id=%s): %s", mem_id, e)

    def _add_memory_edge(self, from_id: int, to_id: int, edge_type: str, weight: float) -> None:
        """在两条记忆之间添加关系边。
        edge_type: FOLLOWS | SIMILAR_TO | CAUSES | BELONGS_TO
        """
        if not self._available():
            return
        if edge_type not in ("FOLLOWS", "SIMILAR_TO", "CAUSES", "BELONGS_TO"):
            logger.warning("⚠️  非法的边类型: %s", edge_type)
            return
        query = (
            "MATCH (a:Memory {mem_id: $from}), (b:Memory {mem_id: $to}) "
            "MERGE (a)-[r:" + edge_type + "]->(b) "
            "SET r.weight = $weight"
        )
        try:
            self.neo.run_cypher(
                query,
                {"from": int(from_id), "to": int(to_id), "weight": float(weight)},
            )
        except Exception as e:
            logger.warning("⚠️  Neo4j addMemoryEdge 失败 (%s→%s): %s", from_id, to_id, e)

    def _expand_memory_neighbors(self, seed_ids: List[int], hops: int) -> List[int]:
        """从种子记忆 ID 出发，按 hops 跳扩展邻居 ID。"""
        if not self._available() or not seed_ids:
            return []
        hop_str = "1" if hops <= 1 else "1.." + str(hops)
        query = (
            "MATCH (m:Memory) WHERE m.mem_id IN $ids "
            "MATCH (m)-[:FOLLOWS|SIMILAR_TO|CAUSES|BELONGS_TO*" + hop_str + "]-(n:Memory) "
            "WHERE NOT n.mem_id IN $ids "
            "RETURN DISTINCT n.mem_id AS id"
        )
        try:
            records = self.neo.run_cypher(query, {"ids": [int(i) for i in seed_ids]})
        except Exception as e:
            logger.warning("⚠️  Neo4j expandMemoryNeighbors 失败: %s", e)
            return []
        result: List[int] = []
        for rec in records:
            v = rec.get("id")
            if v is not None:
                try:
                    result.append(int(v))
                except (TypeError, ValueError):
                    continue
        return result

    def _delete_memory_node(self, mem_id: int) -> None:
        """删除一条记忆节点及其所有边。"""
        if not self._available():
            return
        try:
            self.neo.run_cypher(
                "MATCH (m:Memory {mem_id: $id}) DETACH DELETE m",
                {"id": int(mem_id)},
            )
        except Exception as e:
            logger.warning("⚠️  Neo4j deleteMemoryNode 失败 (id=%s): %s", mem_id, e)

    def _get_high_centrality_ids(self, candidates: List[int], threshold: int) -> List[int]:
        """在候选列表中找出图中入度 ≥ threshold 的节点。"""
        if not self._available() or not candidates:
            return []
        query = (
            "MATCH (m:Memory) WHERE m.mem_id IN $ids "
            "WITH m, size([(m)<-[]-() | 1]) AS indegree "
            "WHERE indegree >= $threshold "
            "RETURN m.mem_id AS id"
        )
        try:
            records = self.neo.run_cypher(
                query,
                {"ids": [int(i) for i in candidates], "threshold": int(threshold)},
            )
        except Exception as e:
            logger.warning("⚠️  Neo4j getHighCentrality 失败: %s", e)
            return []
        result: List[int] = []
        for rec in records:
            v = rec.get("id")
            if v is not None:
                try:
                    result.append(int(v))
                except (TypeError, ValueError):
                    continue
        return result

    # ─── 对外接口（任务约定的核心 4 个方法）────────────────────────────────

    def add_to_graph(self, item, neighbors: Optional[Iterable] = None) -> int:
        """将一条记忆同步进图（异步执行，main 分支 goSafe 模式）：

        - 主线程仅做 mem_id 计算与 prev_id 更新（保证 caller 紧接的查询能看到时序）。
        - 节点 upsert / FOLLOWS / SIMILAR_TO 边写入丢到 daemon 线程，失败不影响主路径。

        返回 mem_id；不可用时返回 -1。
        """
        if not self._available():
            return -1
        mem_id = self._mem_id(item)
        content = getattr(item, "content", "")
        importance = float(getattr(item, "importance", 0.5) or 0.5)
        prev_id = self.prev_id
        # 复制 neighbors 列表 + 拍平 emb，避免后台线程访问时与主线程的 LTM mutation 竞争
        neighbor_pairs: List[tuple] = []
        if neighbors:
            new_emb = list(getattr(item, "embedding", None) or [])
            if new_emb:
                for old in neighbors:
                    if old is item:
                        continue
                    old_id = self._mem_id(old)
                    if old_id == mem_id:
                        continue
                    old_emb = list(getattr(old, "embedding", None) or [])
                    if not old_emb:
                        continue
                    neighbor_pairs.append((old_id, old_emb, new_emb))

        def _write() -> None:
            self._upsert_memory_node(mem_id, content, importance)
            if prev_id >= 0 and prev_id != mem_id:
                self._add_memory_edge(prev_id, mem_id, "FOLLOWS", 1.0)
            for old_id, old_emb, new_emb in neighbor_pairs:
                sim = _cosine(old_emb, new_emb)
                if sim >= self.sim_thresh:
                    self._add_memory_edge(old_id, mem_id, "SIMILAR_TO", sim)

        _go_safe("graphmem.add-to-graph", _write)

        self.prev_id = mem_id
        return mem_id

    def find_related(self, item_id: int, max_hops: Optional[int] = None) -> List[int]:
        """从 item_id 出发，沿图扩展 max_hops 跳，返回关联但不在种子中的 mem_id 列表。

        max_hops 未指定时使用 cfg.kg_max_hops。
        """
        if not self._available():
            return []
        hops = max_hops if (max_hops is not None and max_hops > 0) else self.cfg.kg_max_hops
        return self._expand_memory_neighbors([int(item_id)], int(hops))

    def delete_from_graph(self, item_id: int) -> None:
        """从图中删除一条记忆节点（连同其所有边）。"""
        if not self._available():
            return
        mid = int(item_id)
        if self.prev_id == mid:
            self.prev_id = -1
        self._delete_memory_node(mid)

    def bulk_index(self, items: Iterable) -> int:
        """批量索引：把一批 Item 同步进图（启动期从 LTM 恢复时使用）。

        会按顺序建立 FOLLOWS 链；返回成功 upsert 的节点数。
        """
        if not self._available():
            return 0
        count = 0
        prev_local = self.prev_id
        items_list = list(items)
        for idx, item in enumerate(items_list):
            mem_id = self._mem_id(item)
            self._upsert_memory_node(
                mem_id,
                getattr(item, "content", ""),
                float(getattr(item, "importance", 0.5) or 0.5),
            )
            if prev_local >= 0 and prev_local != mem_id:
                self._add_memory_edge(prev_local, mem_id, "FOLLOWS", 1.0)
            prev_local = mem_id
            count += 1
        self.prev_id = prev_local
        return count

    # ─── 辅助：图中心度保护（可选用于 consolidate）────────────────────────

    def filter_protected(self, candidate_ids: List[int], indegree_threshold: int = 3) -> List[int]:
        """返回 candidate_ids 中入度 ≥ threshold 的节点列表（应豁免删除）。"""
        return self._get_high_centrality_ids(candidate_ids, indegree_threshold)

    # ─── LTM 代理（main 分支签名对齐）──────────────────────────────────────

    def sync_prev_id(self) -> None:
        """从 LTM 拉取 last_id 同步到 self.prev_id（无参数，与 main 一致）。

        未注入 LTM 时静默 no-op。
        """
        if self.ltm is None:
            return
        try:
            last = self.ltm.last_id()
        except Exception as e:
            logger.warning("⚠️  sync_prev_id 调用 ltm.last_id 失败: %s", e)
            return
        self.prev_id = int(last)

    def set_consolidation_config(self, cfg) -> None:
        """代理到 LTM.set_consolidation_config（与 main 一致）。"""
        if self.ltm is None:
            return
        try:
            self.ltm.set_consolidation_config(cfg)
        except Exception as e:
            logger.warning("⚠️  set_consolidation_config 代理失败: %s", e)

    def need_consolidation(self) -> bool:
        """代理到 LTM.need_consolidation（与 main 一致）；未注入返回 False。"""
        if self.ltm is None:
            return False
        try:
            return bool(self.ltm.need_consolidation())
        except Exception as e:
            logger.warning("⚠️  need_consolidation 代理失败: %s", e)
            return False

    def sync_last_item_pg_id(self, pg_id: int) -> None:
        """先代理 LTM 回写 PG 主键，再用最新 last_id 更新 self.prev_id。

        与 main 分支 GraphMemory.SyncLastItemPGID 对齐（缺省下 Neo4j 节点
        upsert 在 add_to_graph 中已通过 _go_safe 异步写入，不在此重复）。
        """
        if self.ltm is None or pg_id <= 0:
            return
        try:
            self.ltm.sync_last_item_pg_id(int(pg_id))
        except Exception as e:
            logger.warning("⚠️  sync_last_item_pg_id 代理失败: %s", e)
            return
        try:
            last = self.ltm.last_id()
        except Exception:
            return
        if last >= 0:
            self.prev_id = int(last)

    def update_node(self, item) -> None:
        """记忆内容/重要性变更后同步更新 Neo4j 节点。"""
        if not self._available():
            return
        self._upsert_memory_node(
            self._mem_id(item),
            getattr(item, "content", ""),
            float(getattr(item, "importance", 0.5) or 0.5),
        )

    def graph_aware_consolidate(self):
        """图感知合并：在 LTM.consolidate 基础上保护高中心度节点 + 同步删 Neo4j。

        与 main 分支 GraphMemory.GraphAwareConsolidate 对齐：
          1) 调 LTM.consolidate 拿基础结果；
          2) Neo4j 不可用时直接返回基础结果；
          3) 入度 ≥3 的节点从 delete_from_db 中剔除（保护核心记忆）；
          4) 异步删除 Neo4j 中对应被删除的节点。
        """
        if self.ltm is None:
            return None
        try:
            result = self.ltm.consolidate()
        except Exception as e:
            logger.warning("⚠️  graph_aware_consolidate: ltm.consolidate 失败: %s", e)
            return None
        if not self._available() or result is None:
            return result

        delete_ids = list(getattr(result, "delete_from_db", []) or [])
        if delete_ids:
            try:
                protected = set(self._get_high_centrality_ids(delete_ids, 3) or [])
            except Exception as e:
                logger.warning("⚠️  graph_aware_consolidate: 高中心度筛选失败: %s", e)
                protected = set()
            if protected:
                filtered = [i for i in delete_ids if i not in protected]
                logger.info(
                    "🛡️  图中心度保护：%d 条记忆免于删除（入度≥3）",
                    len(delete_ids) - len(filtered),
                )
                result.delete_from_db = filtered
                delete_ids = filtered

        if delete_ids:
            def _delete_async():
                for nid in delete_ids:
                    try:
                        self._delete_memory_node(int(nid))
                    except Exception as e:
                        logger.warning("⚠️  Neo4j 节点删除失败 (id=%s): %s", nid, e)

            try:
                from internal.agent.cancel import go_safe  # 避免循环导入
                go_safe("graphmem.consolidate-delete", _delete_async)
            except Exception:
                _delete_async()

        return result

    def close(self) -> None:
        """语义对齐 Go 版；底层 driver 由 Neo4jClient 拥有，这里仅清状态。"""
        self.prev_id = -1
