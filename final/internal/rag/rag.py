# rag — 检索增强生成（RAG）：Milvus 语义 + ES BM25 + Neo4j 图 + 三路 RRF 融合
import json
import logging
import hashlib
from dataclasses import dataclass
from typing import Callable, Dict, List, Optional, Tuple

from config.config import APIConfig
from internal.graph.kgstore import KGStore
from internal.graph.types import ChunkRef
from internal.infra.infra import Infrastructure, RAG_COLLECTION
from internal.llm.llm import Client as LLMClient
from internal.rag.hybrid import HybridStore
from internal.rag.rewriter import HistoryMessage
from internal.rag.splitter import Chunk, RecursiveSplitter

logger = logging.getLogger(__name__)


class Engine:
    """RAG 引擎：切分 → 入库（PG/Milvus/ES） → 检索（RRF 融合） → LLM 合成。"""

    def __init__(self, cfg: APIConfig, inf: Infrastructure, llm: Optional[LLMClient] = None):
        self.cfg = cfg
        self.inf = inf
        parent_size = max(cfg.chunk_size * 4, 600)
        parent_overlap = cfg.chunk_overlap * 2
        self.parent_splitter = RecursiveSplitter(parent_size, parent_overlap)
        self.child_splitter = RecursiveSplitter(cfg.chunk_size, cfg.chunk_overlap)
        self.loaded = False
        self._generate_fn: Optional[Callable[[str, str], str]] = None
        self._rewriter = None
        self._reranker = None
        # 复用 agent 注入的 LLM 客户端，避免重复实例
        self._llm = llm if llm is not None else LLMClient(cfg)
        # 知识图谱存储（由 restore.init_knowledge_graph 注入）。
        # ingest 时同步写入 Neo4j，使 hybrid._fetch_kg 能搜到内容。
        self._kg: Optional[KGStore] = None
        # 三路混合检索（Milvus + ES + KG），由 cfg.enable_hybrid_search 控制是否启用
        self._hybrid: Optional[HybridStore] = None
        self._hybrid = HybridStore(cfg, inf, embed_fn=self._llm.embed)
        self._check_existing_chunks()

    def set_generate_fn(self, fn: Callable[[str, str], str]):
        self._generate_fn = fn

    def set_rewriter(self, rewriter) -> None:
        self._rewriter = rewriter

    def set_reranker(self, reranker) -> None:
        self._reranker = reranker
        if self._hybrid is not None:
            self._hybrid.set_reranker(reranker)

    def set_kg_store(self, kg: Optional[KGStore]) -> None:
        """注入知识图谱存储。

        Engine 自身持一份引用，用于 ingest 时同步写入 Neo4j；同时转发给 hybrid，
        供 enable_hybrid_search=True 时的图路检索使用。
        """
        self._kg = kg
        if self._hybrid is not None:
            self._hybrid.set_kg_store(kg)

    def _check_existing_chunks(self):
        try:
            if self.inf.ready.postgresql == "connected" and self.inf.repo.ragchunk.count() > 0:
                self.loaded = True
                logger.info("✅ 检测到知识库中已有文档")
        except Exception as e:
            logger.error("检查知识库文档失败: %s", e)

    # ── 入库 ────────────────────────────────────────────────────────────────

    def ingest(self, doc: str) -> int:
        parents = self.parent_splitter.split(doc)
        chunks: List[Chunk] = []
        child_parents: List[str] = []
        for parent in parents:
            for child in self.child_splitter.split(parent.content):
                child.id = len(chunks)
                chunks.append(child)
                child_parents.append(parent.content)
        if not chunks:
            return 0

        doc_hash = hashlib.sha256(doc.encode("utf-8")).hexdigest()[:16]
        contents = [chunk.content for chunk in chunks]
        embeddings: List[List[float]] = []
        for i, chunk in enumerate(chunks):
            embedding: List[float] = []
            try:
                embedding = self._llm.embed(chunk.content)
            except Exception as e:
                logger.warning("⚠️  RAG chunk 向量化失败，跳过 Milvus 写入 (idx=%d): %s", i, e)
            embeddings.append(embedding)

        if self._hybrid is not None:
            pg_ids = self._hybrid.index_with_parents(doc_hash, contents, child_parents, embeddings)
        else:
            pg_ids = []
            for i, chunk in enumerate(chunks):
                parent_content = child_parents[i] if i < len(child_parents) else ""
                pg_id = self.inf.repo.ragchunk.save_pg_with_parent(
                    doc_hash, i, chunk.content, parent_content, json.dumps(embeddings[i] if i < len(embeddings) else [])
                )
                if pg_id > 0:
                    pg_ids.append(pg_id)

        self.loaded = True
        self.inf.repo.events.publish("rag.ingest", json.dumps({
            "chunk_count": len(chunks),
            "parent_count": len(parents),
            "doc_hash": doc_hash,
        }))
        return len(chunks)

    # ── 检索 ────────────────────────────────────────────────────────────────

    def query(self, question: str) -> Tuple[str, List[dict]]:
        return self.query_with_history(question, [])

    def query_with_history(self, question: str, history: Optional[List[HistoryMessage]] = None) -> Tuple[str, List[dict]]:
        if not self.loaded:
            return "知识库为空，请先上传文档。", []
        if self.inf.ready.postgresql != "connected":
            return "PostgreSQL 未连接，无法查询知识库。", []

        top_k = max(1, self.cfg.top_k)
        queries = [question]
        if self._rewriter is not None:
            rewritten = self._rewriter.rewrite(question, history or [])
            if rewritten:
                queries = rewritten

        # 统一走 HybridStore.search_multi；基础设施可用性与模式切换由 HybridStore 内部决定。
        if self._hybrid is not None:
            hybrid_hits = self._hybrid.search_multi(queries, top_k)
            fused = [
                {
                    "pg_id": h.pg_id,
                    "content": h.parent or h.content,
                    "score": h.score,
                    "source": h.source,
                }
                for h in hybrid_hits
            ]
            ask_query = queries[0] if queries else question
            return self._compose_answer(ask_query, fused)

        if self._hybrid is not None:
            hybrid_hits = self._hybrid.search_multi(queries, top_k)
            fused = [
                {
                    "pg_id": h.pg_id,
                    "content": h.parent or h.content,
                    "score": h.score,
                    "source": h.source,
                }
                for h in hybrid_hits
            ]
            ask_query = queries[0] if queries else question
            return self._compose_answer(ask_query, fused)

        return self._compose_answer(question, [])

    def _compose_answer(self, question: str, fused: List[dict]) -> Tuple[str, List[dict]]:
        fused = self._dedupe_results_by_content(fused)
        if not fused:
            return "知识库中未找到相关内容。", []

        context = "\n\n".join(r["content"] for r in fused if r.get("content"))
        if not context:
            return "知识库中未找到相关内容。", []

        if self._generate_fn:
            system_prompt = (
                "你是一个基于知识库回答问题的助手。请仅根据提供的上下文内容回答问题，"
                "不要编造信息。如果上下文不足以回答，请说明。"
            )
            user_msg = f"上下文：\n{context}\n\n问题：{question}"
            return self._generate_fn(system_prompt, user_msg), fused

        return f"【知识库检索结果】\n{context}", fused

    def _dedupe_results_by_content(self, results: List[dict]) -> List[dict]:
        seen = set()
        deduped: List[dict] = []
        for item in results:
            content = (item.get("content") or "").strip()
            if not content:
                continue
            if content in seen:
                continue
            seen.add(content)
            deduped.append(item)
        return deduped
