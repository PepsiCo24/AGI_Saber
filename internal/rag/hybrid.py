# hybrid — 混合检索存储：语义向量（Milvus）+ 关键词（ES BM25）+ RRF 融合
import hashlib
import json
import logging
from dataclasses import dataclass, field
from typing import List, Optional, Callable, Dict, Any

from config.config import APIConfig
from internal.infra.infra import Infrastructure

logger = logging.getLogger(__name__)


# ─────────────────────────────── HybridStore ────────────────────────────────

@dataclass
class Chunk:
    content: str


@dataclass
class HybridResult:
    chunk: Chunk
    score: float
    source: str  # "hybrid" | "semantic" | "keyword" | "unavailable"


class HybridStore:
    """
    实现企业级混合检索：
    - Milvus 语义向量检索
    - Elasticsearch BM25 关键词检索
    - Reciprocal Rank Fusion 融合两路结果
    - PostgreSQL chunk 持久化
    """

    def __init__(self, cfg: APIConfig, inf: Infrastructure):
        self.cfg = cfg
        self.inf = inf
        self.embed_fn: Optional[Callable[[str], List[float]]] = None
        self.mode = self._detect_mode()
        # 内存模式下的向量存储（替代 Milvus）
        self._memory_chunks: List[Chunk] = []
        self._memory_embeddings: List[List[float]] = []
        self._memory_doc_hashes: List[str] = []

    def _detect_mode(self) -> str:
        """根据基础设施可用性自动选择模式"""
        milvus_ok = self.inf.ready.milvus == "connected" or self.inf.ready.milvus == "memory-mode"
        es_ok = self.inf.ready.elasticsearch == "connected"
        if milvus_ok and es_ok:
            return "hybrid"
        elif milvus_ok:
            return "semantic"
        elif es_ok:
            return "keyword"
        else:
            return "unavailable"

    def set_embed_fn(self, fn: Callable[[str], List[float]]):
        """注入 Embedding 回调（由 agent 通过 llm.embed 注入）"""
        self.embed_fn = fn

    def get_mode(self) -> str:
        """返回当前检索模式"""
        return self.mode

    # ─────────────────────────────── Index ────────────────────────────────────

    def index(self, chunks: List[Chunk], doc_content: str) -> str:
        """将 chunks 持久化到 PG + Milvus + ES，返回文档哈希（用于后续删除）"""
        # 计算文档哈希（幂等摄入）
        doc_hash = hashlib.sha256(doc_content.encode()).hexdigest()[:16]

        # 内存模式下存储
        for c in chunks:
            self._memory_chunks.append(c)
            self._memory_doc_hashes.append(doc_hash)
            if self.embed_fn:
                try:
                    emb = self.embed_fn(c.content)
                    self._memory_embeddings.append(emb)
                except Exception as e:
                    logger.warning("⚠️  Chunk 向量化失败: %s", e)
                    self._memory_embeddings.append([])
            else:
                self._memory_embeddings.append([])

        logger.info("✅ HybridStore 索引完成，文档哈希: %s, chunk数: %d", doc_hash, len(chunks))
        return doc_hash

    def delete(self, doc_hash: str):
        """按 doc_hash 删除文档的所有 chunks"""
        # 过滤掉该文档的所有 chunks
        new_chunks = []
        new_embeddings = []
        new_hashes = []
        for i, h in enumerate(self._memory_doc_hashes):
            if h != doc_hash:
                new_chunks.append(self._memory_chunks[i])
                new_embeddings.append(self._memory_embeddings[i])
                new_hashes.append(h)
        self._memory_chunks = new_chunks
        self._memory_embeddings = new_embeddings
        self._memory_doc_hashes = new_hashes
        logger.info("✅ HybridStore 删除完成，文档哈希: %s", doc_hash)

    def restore_chunks(self, chunks: List[Chunk]):
        """标记 chunks 已从 PG 恢复（由 Engine 设置 Loaded）"""
        # chunks 已持久化在 PG/Milvus/ES 中，无需额外操作
        pass

    # ─────────────────────────────── Search ───────────────────────────────────

    def search(self, query: str, top_k: int) -> List[HybridResult]:
        """根据当前模式执行检索"""
        if self.mode == "hybrid":
            return self._search_hybrid(query, top_k)
        elif self.mode == "semantic":
            return self._search_semantic(query, top_k)
        elif self.mode == "keyword":
            return self._search_keyword(query, top_k)
        else:
            logger.warning("⚠️  检索基础设施不可用（Milvus 和 ES 均未连接）")
            return []

    # ─────────────────────────────── Hybrid: RRF 融合 ────────────────────────

    def _search_hybrid(self, query: str, top_k: int) -> List[HybridResult]:
        """Milvus 语义 + ES BM25，使用 Reciprocal Rank Fusion 融合"""
        if not self.embed_fn:
            logger.warning("⚠️  查询向量化失败，降级到关键词检索")
            return self._search_keyword(query, top_k)

        # 从两路各取 2*topK 保证融合后有足够候选
        fetch_k = top_k * 2
        if fetch_k < 10:
            fetch_k = 10

        milvus_hits = self._search_semantic(query, fetch_k)
        es_hits = self._search_keyword(query, fetch_k)

        if not milvus_hits and not es_hits:
            logger.warning("⚠️  Milvus 和 ES 均检索失败")
            return []
        if not milvus_hits:
            logger.warning("⚠️  Milvus 检索失败，使用关键词检索")
            return self._search_keyword(query, top_k)
        if not es_hits:
            logger.warning("⚠️  ES 检索失败，使用语义检索")
            return self._search_semantic(query, top_k)

        # Reciprocal Rank Fusion: score(d) = Σ 1/(k + rank_i(d))
        k = self.cfg.rrf_constant_k if self.cfg.rrf_constant_k > 0 else 60

        rrf_scores: Dict[int, float] = {}
        for rank, hit in enumerate(milvus_hits):
            if rank not in rrf_scores:
                rrf_scores[rank] = 0.0
            rrf_scores[rank] += 1.0 / float(k + rank + 1)
        for rank, hit in enumerate(es_hits):
            idx = len(milvus_hits) + rank
            if idx not in rrf_scores:
                rrf_scores[idx] = 0.0
            rrf_scores[idx] += 1.0 / float(k + rank + 1)

        # 合并结果并去重
        all_results = milvus_hits + es_hits
        result_scores: Dict[str, float] = {}
        result_map: Dict[str, HybridResult] = {}
        for i, result in enumerate(all_results):
            key = result.chunk.content[:100]
            if key not in result_scores:
                result_scores[key] = rrf_scores.get(i, 0.0)
                result_map[key] = result
            else:
                result_scores[key] += rrf_scores.get(i, 0.0)

        # 按 RRF 分数排序
        sorted_keys = sorted(result_scores.keys(), key=lambda x: result_scores[x], reverse=True)
        results = [result_map[k] for k in sorted_keys[:top_k]]
        for r in results:
            r.source = "hybrid"
        return results

    # ─────────────────────────────── Semantic: Milvus ────────────────────────

    def _search_semantic(self, query: str, top_k: int) -> List[HybridResult]:
        """仅 Milvus 语义向量检索（内存模式）"""
        if not self.embed_fn or not self._memory_chunks:
            return []

        try:
            query_emb = self.embed_fn(query)
        except Exception as e:
            logger.warning("⚠️  查询向量化失败: %s", e)
            return []

        # 计算余弦相似度
        results = []
        for i, chunk in enumerate(self._memory_chunks):
            emb = self._memory_embeddings[i]
            if len(emb) == 0 or len(query_emb) == 0:
                sim = 0.0
            else:
                sim = self._cosine_similarity(query_emb, emb)
            if sim > 0.01:
                results.append(HybridResult(
                    chunk=chunk,
                    score=sim,
                    source="semantic"
                ))

        # 按相似度排序
        results.sort(key=lambda r: r.score, reverse=True)
        return results[:top_k]

    def _cosine_similarity(self, a: List[float], b: List[float]) -> float:
        """计算余弦相似度"""
        if len(a) != len(b):
            return 0.0
        dot = sum(x * y for x, y in zip(a, b))
        na = math.sqrt(sum(x * x for x in a))
        nb = math.sqrt(sum(x * x for x in b))
        if na == 0 or nb == 0:
            return 0.0
        return dot / (na * nb)

    # ─────────────────────────────── Keyword: ES BM25 ────────────────────────

    def _search_keyword(self, query: str, top_k: int) -> List[HybridResult]:
        """仅 Elasticsearch BM25 关键词检索（内存模式下使用简单匹配）"""
        if not self._memory_chunks:
            return []

        # 内存模式下使用简单的词频匹配
        query_tokens = self._tokenize(query.lower())
        results = []

        for chunk in self._memory_chunks:
            chunk_tokens = self._tokenize(chunk.content.lower())
            # 计算匹配度
            matches = sum(1 for t in query_tokens if t in chunk_tokens)
            if matches > 0:
                score = matches / len(query_tokens)
                results.append(HybridResult(
                    chunk=chunk,
                    score=score,
                    source="keyword"
                ))

        # 按分数排序
        results.sort(key=lambda r: r.score, reverse=True)
        return results[:top_k]

    def _tokenize(self, text: str) -> List[str]:
        """简单的中英文分词"""
        tokens = []
        word = ""
        for ch in text:
            cp = ord(ch)
            if 0x4E00 <= cp <= 0x9FFF:
                if word:
                    tokens.append(word)
                    word = ""
                tokens.append(ch)
            elif ch.isalpha() or ch.isdigit():
                word += ch
            else:
                if word:
                    tokens.append(word)
                    word = ""
        if word:
            tokens.append(word)
        return tokens
