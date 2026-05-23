# rag — 检索增强生成（Retrieval-Augmented Generation）
# 包含：文本分割器、Milvus向量存储、混合检索、RAG引擎
import json
import math
import logging
from dataclasses import dataclass, field
from typing import List, Optional, Callable, Dict

from config.config import APIConfig
from internal.infra.infra import Infrastructure
from internal.llm.llm import Client as LLMClient

logger = logging.getLogger(__name__)


# ─────────────────────────────── 文本分割 ────────────────────────────────

@dataclass
class Chunk:
    id: int
    content: str


class TextSplitter:
    """按字符窗口将长文本切成有重叠的 Chunk"""

    def __init__(self, chunk_size: int = 200, overlap: int = 50):
        self.chunk_size = chunk_size
        self.overlap = overlap

    def split(self, text: str) -> List[Chunk]:
        step = self.chunk_size - self.overlap
        if step <= 0:
            step = self.chunk_size
        chunks: List[Chunk] = []
        idx = 0
        for i in range(0, len(text), step):
            end = i + self.chunk_size
            chunk_text = text[i:end]
            chunks.append(Chunk(id=idx, content=chunk_text))
            idx += 1
            if end >= len(text):
                break
        return chunks


# ─────────────────────────────── 搜索结果 ────────────────────────────────

@dataclass
class SearchResult:
    chunk: Chunk
    similarity: float


# ─────────────────────────────── RAG 引擎 ────────────────────────────────

class Engine:
    """整合文本分割、向量检索与答案生成，使用 Milvus 作为向量存储"""

    def __init__(self, cfg: APIConfig, inf: Infrastructure):
        self.cfg = cfg
        self.splitter = TextSplitter(cfg.chunk_size, cfg.chunk_overlap)
        self.loaded: bool = False
        self.inf = inf
        self._generate_fn: Optional[Callable] = None
        self._llm = LLMClient(cfg)
        
        # 检查知识库中是否已有文档
        self._check_existing_chunks()

    def set_generate_fn(self, fn: Callable):
        """注入 LLM 调用回调，供 Query 合成答案"""
        self._generate_fn = fn
    
    def _check_existing_chunks(self):
        """检查知识库中是否已有文档"""
        try:
            if self.inf.ready.postgresql == "connected":
                count = self.inf.count_rag_chunks()
                if count > 0:
                    self.loaded = True
                    logger.info(f"✅ 检测到知识库中已有 {count} 条文档")
        except Exception as e:
            logger.error("检查知识库文档失败: %s", e)

    def ingest(self, doc: str) -> int:
        """将文档切分并建立向量索引，返回切片数量"""
        # 1. 文本分割
        chunks = self.splitter.split(doc)
        if not chunks:
            return 0
        
        # 2. 生成嵌入向量
        contents = [c.content for c in chunks]
        embeddings = [self._llm.embed(content) for content in contents]
        
        # 3. 保存到 PostgreSQL
        pg_ids = []
        for i, chunk in enumerate(chunks):
            embedding_json = json.dumps(embeddings[i])
            pg_id = self.inf.save_rag_chunk("doc_placeholder", i, chunk.content, embedding_json)
            if pg_id > 0:
                pg_ids.append(pg_id)
        
        # 4. 插入 Milvus
        if self.inf.ready.milvus == "connected" and pg_ids:
            self.inf.insert_rag_chunks(pg_ids, contents, embeddings)
        
        # 5. 索引到 Elasticsearch
        for i, pg_id in enumerate(pg_ids):
            self.inf.index_rag_chunk(pg_id, chunks[i].content, "doc_placeholder", i)
        
        self.loaded = True
        self.inf.publish_event("rag.ingest", f'{{"chunk_count":{len(chunks)}}}')
        return len(chunks)

    def query(self, question: str) -> tuple:
        """检索知识库并返回 (answer, results)"""
        # 检查是否有数据
        if self.inf.ready.postgresql != "connected":
            return "PostgreSQL 未连接，无法查询知识库。", []
        
        # 1. 获取查询嵌入
        query_emb = self._llm.embed(question)
        
        # 2. Milvus 向量检索
        milvus_results = []
        if self.inf.ready.milvus == "connected":
            milvus_results = self.inf.milvus_search_with_scores("rag_embeddings", query_emb, self.cfg.top_k)
        
        # 3. Elasticsearch 全文检索
        es_results = []
        if self.inf.ready.elasticsearch == "connected":
            es_results = self.inf.search_rag_chunks(question, self.cfg.top_k)
        
        # 4. 融合结果（简单去重）
        all_results = {}
        for r in milvus_results:
            if r.get("pg_id"):
                all_results[r["pg_id"]] = {"content": r["content"], "score": r["score"], "source": "milvus"}
        
        for r in es_results:
            pg_id = r["pg_id"]
            if pg_id in all_results:
                # RRF 融合：取平均得分
                all_results[pg_id]["score"] = (all_results[pg_id]["score"] + r["score"] * 0.5) / 1.5
            else:
                all_results[pg_id] = {"content": r["content"], "score": r["score"], "source": "es"}
        
        # 按分数排序
        sorted_results = sorted(all_results.values(), key=lambda x: x["score"], reverse=True)
        top_results = sorted_results[:self.cfg.top_k]
        
        if not top_results:
            return "知识库中未找到相关内容。", []
        
        # 构建上下文
        context = "\n\n".join([r["content"] for r in top_results if r["score"] > 0.01])
        
        if not context:
            return "知识库中未找到相关内容。", []
        
        # 生成答案
        if self._generate_fn:
            system_prompt = "你是一个基于知识库回答问题的助手。请仅根据提供的上下文内容回答问题，不要编造信息。如果上下文不足以回答，请说明。"
            user_msg = f"上下文：\n{context}\n\n问题：{question}"
            answer = self._generate_fn(system_prompt, user_msg)
            return answer, top_results
        
        return f"【知识库检索结果】\n{context}", top_results

    def get_chunks(self) -> List[Chunk]:
        # 从 PostgreSQL 获取所有 chunks
        return []
