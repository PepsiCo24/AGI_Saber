# hybrid — 企业级混合检索：Milvus 语义 + ES BM25 + Neo4j 知识图谱 + 三路 RRF 融合
#
# 三路 score 来自 reciprocal rank fusion（基于 rank，不依赖原始分尺度）：
#     score(d) = Σ_i  weight_i / (k + rank_i(d))
# 权重：semantic_weight、(1 - semantic_weight - kg_weight)、kg_weight，
# 任一路不可用 / 失败时跳过并把剩余权重重新归一，避免某一路因不可用拉低融合分数。
import logging
from dataclasses import dataclass, field
import json
import threading
from typing import Callable, Dict, List, Optional

from config.config import APIConfig
from internal.graph.types import ChunkRef
from internal.graph.kgstore import KGStore
from internal.infra.infra import Infrastructure

logger = logging.getLogger(__name__)


# Embedding 回调签名：text -> List[float]
EmbedFn = Callable[[str], List[float]]


@dataclass
class HybridResult:
    """混合检索的单条结果（与 Go 版 HybridResult 字段对齐）"""
    pg_id: int = 0
    content: str = ""
    score: float = 0.0
    source: str = ""  # "hybrid" | "semantic" | "keyword"
    parent: str = ""


@dataclass
class _PathHits:
    """单路检索结果（rank 顺序）+ 是否成功"""
    hits: List[dict] = field(default_factory=list)
    ok: bool = False


class HybridStore:
    """企业级混合检索：
        - Milvus 语义向量检索
        - Elasticsearch BM25 关键词检索
        - Neo4j 知识图谱实体遍历检索
        - Reciprocal Rank Fusion 三路融合

    根据基础设施可用性自动选择检索模式：
        hybrid / semantic / keyword / unavailable
    """

    def __init__(
        self,
        cfg: APIConfig,
        inf: Infrastructure,
        embed_fn: Optional[EmbedFn] = None,
        kg: Optional[KGStore] = None,
    ):
        self.cfg = cfg
        self.inf = inf
        self._embed_fn = embed_fn
        self._kg = kg
        self._reranker = None
        self.mode = self._resolve_mode()

    # ─── 注入式 setter（与 Go 版接口对齐） ─────────────────────────────────

    def set_embed_fn(self, fn: EmbedFn) -> None:
        self._embed_fn = fn

    def set_kg_store(self, kg: Optional[KGStore]) -> None:
        self._kg = kg

    def set_reranker(self, reranker) -> None:
        self._reranker = reranker

    # ─── 入库 ──────────────────────────────────────────────────────────────

    def index_with_parents(
        self,
        doc_hash: str,
        contents: List[str],
        parents: List[str],
        embeddings: List[List[float]],
    ) -> List[int]:
        """把 PG / ES / Milvus / KG 写入收敛到 HybridStore。

        Engine 只负责切片和向量化；这里负责所有持久化扇出。KG 写入在后台线程
        best-effort 执行，异常只记录日志，不阻塞主入库流程。
        """
        pg_ids: List[int] = []
        valid_contents: List[str] = []
        valid_embeddings: List[List[float]] = []
        valid_chunk_idxs: List[int] = []

        for idx, content in enumerate(contents):
            embedding = embeddings[idx] if idx < len(embeddings) else []
            parent_content = parents[idx] if idx < len(parents) else ""
            pg_id = self.inf.repo.ragchunk.save_pg_with_parent(
                doc_hash, idx, content, parent_content, json.dumps(embedding)
            )
            if pg_id <= 0:
                continue
            pg_ids.append(pg_id)
            valid_contents.append(content)
            valid_embeddings.append(embedding)
            valid_chunk_idxs.append(idx)
            if self.inf.ready.elasticsearch == "connected":
                try:
                    self.inf.repo.ragchunk.index_es(pg_id, content, doc_hash, idx)
                except Exception as e:
                    logger.warning("⚠️  RAG chunk 索引到 ES 失败 (pg_id=%s): %s", pg_id, e)

        milvus_ids: List[int] = []
        milvus_contents: List[str] = []
        milvus_embeddings: List[List[float]] = []
        if self.inf.ready.milvus == "connected":
            for pg_id, content, embedding in zip(pg_ids, valid_contents, valid_embeddings):
                if embedding and len(embedding) == self.cfg.rag_milvus_dim:
                    milvus_ids.append(pg_id)
                    milvus_contents.append(content)
                    milvus_embeddings.append(embedding)
        if milvus_ids:
            try:
                self.inf.repo.ragchunk.insert_milvus(milvus_ids, milvus_contents, milvus_embeddings)
            except Exception as e:
                logger.warning("⚠️  RAG chunks 写入 Milvus 失败: %s", e)

        if self._kg is not None and self._kg.available() and pg_ids:
            refs = [
                ChunkRef(id=idx, pg_id=pg_id, content=content)
                for idx, pg_id, content in zip(valid_chunk_idxs, pg_ids, valid_contents)
            ]
            threading.Thread(
                target=self._index_kg_safe,
                args=(doc_hash, refs),
                name="rag-kg-index",
                daemon=True,
            ).start()

        return pg_ids

    def _index_kg_safe(self, doc_hash: str, refs: List[ChunkRef]) -> None:
        try:
            if self._kg is not None and self._kg.available():
                self._kg.index_document(doc_hash, refs)
        except Exception as e:
            logger.warning("⚠️  RAG chunks 写入 Neo4j 知识图谱失败: %s", e)

    # ─── 基础设施可用性 ──────────────────────────────────────────────────

    def _milvus_ok(self) -> bool:
        return self.inf.ready.milvus == "connected"

    def _es_ok(self) -> bool:
        return self.inf.ready.elasticsearch == "connected"

    def _kg_ok(self) -> bool:
        return self._kg is not None and self._kg.available()

    def _resolve_mode(self) -> str:
        m, e = self._milvus_ok(), self._es_ok()
        if m and e:
            return "hybrid"
        if m:
            return "semantic"
        if e:
            return "keyword"
        return "unavailable"

    # ─── 入口 ───────────────────────────────────────────────────────────

    def search(self, query: str, top_k: int) -> List[HybridResult]:
        # 模式可能在运行时变化（连接恢复），每次入口重新判定
        self.mode = self._resolve_mode()
        if self.mode == "hybrid":
            return self._search_hybrid(query, top_k)
        if self.mode == "semantic":
            return self._search_semantic(query, top_k)
        if self.mode == "keyword":
            return self._search_keyword(query, top_k)
        logger.warning("⚠️  检索基础设施不可用（Milvus 和 ES 均未连接）")
        return []

    def search_multi(self, queries: List[str], top_k: int) -> List[HybridResult]:
        queries = [q for q in (queries or []) if q]
        if not queries:
            return []
        pool = self._rerank_pool(top_k)
        if len(queries) == 1:
            return self._finalize(queries[0], self.search(queries[0], pool), top_k)

        results_by_query: List[List[HybridResult]] = [[] for _ in queries]
        threads = []

        def _run(idx: int, q: str) -> None:
            results_by_query[idx] = self.search(q, pool)

        for i, q in enumerate(queries):
            t = threading.Thread(target=_run, args=(i, q), daemon=True)
            threads.append(t)
            t.start()
        for t in threads:
            t.join()

        k = self.cfg.rrf_constant_k if self.cfg.rrf_constant_k > 0 else 60
        merged: Dict[str, dict] = {}
        for query_results in results_by_query:
            for rank, result in enumerate(query_results):
                key = f"id:{result.pg_id}" if result.pg_id else f"c:{result.content[:100]}"
                score = 1.0 / float(k + rank + 1)
                if key in merged:
                    merged[key]["score"] += score
                    if result.score > merged[key]["result"].score:
                        merged[key]["result"] = result
                else:
                    merged[key] = {"score": score, "result": result}

        out: List[HybridResult] = []
        for item in merged.values():
            result = item["result"]
            result.score = item["score"]
            out.append(result)
        out.sort(key=lambda r: r.score, reverse=True)
        if len(out) > pool:
            out = out[:pool]
        return self._finalize(queries[0], out, top_k)

    def _rerank_pool(self, top_k: int) -> int:
        pool = top_k * (4 if self._reranker is not None else 2)
        return max(pool, 10)

    def _finalize(self, query: str, results: List[HybridResult], top_k: int) -> List[HybridResult]:
        if self._reranker is not None and len(results) > 1:
            return self._reranker.rerank(query, results, top_k)
        if top_k > 0 and len(results) > top_k:
            return results[:top_k]
        return results

    # ─── 混合检索：三路 RRF 融合 ─────────────────────────────────────────

    def _search_hybrid(self, query: str, top_k: int) -> List[HybridResult]:
        # 从每路取 2*top_k，给融合留候选（与 Go 版一致，下限 10）
        fetch_k = max(top_k * 2, 10)

        milvus_path = self._fetch_milvus(query, fetch_k)
        es_path = self._fetch_es(query, fetch_k)
        kg_path = self._fetch_kg(query, fetch_k)

        # 三路全失败 → 直接返回；保留 Go 版的两两降级路径
        if not milvus_path.ok and not es_path.ok and not kg_path.ok:
            logger.warning("⚠️  三路检索均失败")
            return []
        if not milvus_path.ok and not es_path.ok:
            return self._materialize_kg_only(kg_path.hits, top_k)
        if not milvus_path.ok:
            return self._search_keyword(query, top_k)
        if not es_path.ok:
            return self._search_semantic(query, top_k)

        sem_w = max(0.0, float(self.cfg.semantic_weight))
        kw_w = max(0.0, 1.0 - sem_w)
        k = self.cfg.rrf_constant_k if self.cfg.rrf_constant_k > 0 else 60
        rrf_scores: Dict[int, float] = {}

        for rank, hit in enumerate(milvus_path.hits):
            pg_id = hit.get("pg_id")
            if pg_id is None:
                continue
            rrf_scores[pg_id] = rrf_scores.get(pg_id, 0.0) + sem_w / (k + rank + 1)

        for rank, hit in enumerate(es_path.hits):
            pg_id = hit.get("pg_id")
            if pg_id is None:
                continue
            rrf_scores[pg_id] = rrf_scores.get(pg_id, 0.0) + kw_w / (k + rank + 1)

        if kg_path.ok and self._kg is not None:
            for rank, hit in enumerate(kg_path.hits):
                pg_id = hit.pg_id if hasattr(hit, "pg_id") else hit.get("pg_id", 0)
                if not pg_id:
                    continue
                rrf_scores[pg_id] = rrf_scores.get(pg_id, 0.0) + 1.0 / (k + rank + 1)

        sorted_ids = sorted(rrf_scores.items(), key=lambda x: x[1], reverse=True)
        if len(sorted_ids) > top_k:
            sorted_ids = sorted_ids[:top_k]
        if not sorted_ids:
            return []

        ids = [pid for pid, _ in sorted_ids]
        rows = self.inf.repo.ragchunk.load_by_ids_with_parent(ids)
        row_map: Dict[int, dict] = {r["id"]: r for r in rows}
        results: List[HybridResult] = []
        for pid, score in sorted_ids:
            row = row_map.get(pid)
            if row is None:
                continue
            results.append(HybridResult(
                pg_id=pid,
                content=row.get("content", ""),
                score=score,
                source="hybrid",
                parent=row.get("parent_content", "") or row.get("parent", ""),
            ))
        return results

    # ─── 单路：Milvus 语义 ───────────────────────────────────────────────

    def _search_semantic(self, query: str, top_k: int) -> List[HybridResult]:
        path = self._fetch_milvus(query, top_k)
        if not path.ok:
            return []
        ids = [h["pg_id"] for h in path.hits if h.get("pg_id") is not None]
        rows = self.inf.repo.ragchunk.load_by_ids_with_parent(ids) if ids else []
        row_map: Dict[int, dict] = {r["id"]: r for r in rows}
        results: List[HybridResult] = []
        for h in path.hits:
            pid = h.get("pg_id")
            if pid is None:
                continue
            row = row_map.get(pid, {})
            content = row.get("content") or h.get("content") or ""
            if not content:
                continue
            results.append(HybridResult(
                pg_id=pid, content=content,
                score=float(h.get("score", 0.0)), source="semantic",
                parent=row.get("parent_content", "") or row.get("parent", ""),
            ))
        return results

    # ─── 单路：ES BM25 ───────────────────────────────────────────────────

    def _search_keyword(self, query: str, top_k: int) -> List[HybridResult]:
        path = self._fetch_es(query, top_k)
        if not path.ok:
            return []
        ids = [h["pg_id"] for h in path.hits if h.get("pg_id") is not None]
        rows = self.inf.repo.ragchunk.load_by_ids_with_parent(ids) if ids else []
        row_map: Dict[int, dict] = {r["id"]: r for r in rows}
        results: List[HybridResult] = []
        for h in path.hits:
            pid = h.get("pg_id")
            if pid is None:
                continue
            row = row_map.get(pid, {})
            content = row.get("content") or h.get("content") or ""
            if not content:
                continue
            results.append(HybridResult(
                pg_id=pid, content=content,
                score=float(h.get("score", 0.0)), source="keyword",
                parent=row.get("parent_content", "") or row.get("parent", ""),
            ))
        return results

    # ─── 三路 fetch（统一 try/except，失败 → ok=False） ──────────────────

    def _fetch_milvus(self, query: str, fetch_k: int) -> _PathHits:
        if not self._milvus_ok():
            return _PathHits(ok=False)
        if self._embed_fn is None:
            logger.warning("⚠️  embed_fn 未注入，跳过 Milvus 语义路")
            return _PathHits(ok=False)
        try:
            query_emb = self._embed_fn(query)
        except Exception as e:
            logger.warning("⚠️  查询向量化失败: %s", e)
            return _PathHits(ok=False)
        if not query_emb:
            return _PathHits(ok=False)
        # 维度不匹配时跳过（与 rag.py 行为一致），避免 Milvus 服务端报错
        if self.cfg.rag_milvus_dim and len(query_emb) != self.cfg.rag_milvus_dim:
            logger.warning(
                "⚠️  embedding 维度 %d 与 rag_milvus_dim=%d 不匹配，跳过语义路",
                len(query_emb), self.cfg.rag_milvus_dim,
            )
            return _PathHits(ok=False)
        try:
            hits = self.inf.repo.ragchunk.search_milvus_dicts(query_emb, fetch_k) or []
            return _PathHits(hits=hits, ok=True)
        except Exception as e:
            logger.warning("⚠️  Milvus 检索失败: %s", e)
            return _PathHits(ok=False)

    def _fetch_es(self, query: str, fetch_k: int) -> _PathHits:
        if not self._es_ok():
            return _PathHits(ok=False)
        try:
            hits = self.inf.repo.ragchunk.search_es_dicts(query, fetch_k) or []
            return _PathHits(hits=hits, ok=True)
        except Exception as e:
            logger.warning("⚠️  ES 检索失败: %s", e)
            return _PathHits(ok=False)

    def _fetch_kg(self, query: str, fetch_k: int) -> _PathHits:
        if not self._kg_ok():
            return _PathHits(ok=False)
        try:
            hits = self._kg.search(query, fetch_k) or []
            return _PathHits(hits=hits, ok=True)
        except Exception as e:
            logger.warning("⚠️  KG 检索失败: %s", e)
            return _PathHits(ok=False)

    # ─── 权重归一 ─────────────────────────────────────────────────────────
