# milvus — Milvus 向量数据库平台层薄封装：连接、集合初始化、insert/search/delete。
# 失败时降级到内存模式（self._client 为 None），不阻塞应用启动。
import logging
from typing import Any, Dict, List, Optional

from config.config import APIConfig

logger = logging.getLogger(__name__)

try:
    from pymilvus import DataType, MilvusClient
    _HAS_MILVUS = True
except ImportError:
    DataType = None  # type: ignore
    MilvusClient = None  # type: ignore
    _HAS_MILVUS = False


# 默认 RAG 集合名（与 main 分支 Go 实现 internal/infrastructure/persistence/ragchunk 对齐）
DEFAULT_RAG_COLLECTION = "rag_chunks"

# 索引参数（与 Go 端 entity.NewIndexIvfFlat(entity.L2, 128) 对齐）
_RAG_INDEX_TYPE = "IVF_FLAT"
_RAG_METRIC_TYPE = "L2"
_RAG_INDEX_NLIST = 128

# content 字段最大长度（与 Go 端 TypeParams["max_length"]=4096 对齐）
_RAG_CONTENT_MAX_LEN = 4096


class MilvusClientWrapper:
    """Milvus 平台客户端：连接、集合管理、关键向量操作。

    保持平台层无业务语义；业务集合的 schema 与查询逻辑由调用方决定。
    """

    def __init__(self, cfg: APIConfig):
        self.cfg = cfg
        self._client = None
        # 与 Go 版差异：内存模式下用 "memory-mode" 显式标识降级
        self.status: str = "disconnected"
        self._connect()
        if self._client is not None:
            self._init_default_collections()

    # ─── 连接 ───
    def _connect(self) -> None:
        if not _HAS_MILVUS:
            logger.warning("⚠️  pymilvus 未安装，Milvus 不可用 (将使用内存向量库)")
            self.status = "memory-mode"
            return
        if not self.cfg.milvus_host or not self.cfg.milvus_port:
            logger.warning("⚠️  Milvus 未配置")
            self.status = "memory-mode"
            return
        try:
            uri = f"http://{self.cfg.milvus_addr()}"
            self._client = MilvusClient(uri=uri)
            self.status = "connected"
            logger.info("✅ Milvus 已连接: %s", self.cfg.milvus_addr())
        except Exception as e:
            logger.warning("⚠️  Milvus 连接失败: %s (将使用内存向量库)", e)
            self._client = None
            self.status = "memory-mode"

    # ─── 状态判断 ───
    def is_real(self) -> bool:
        return self._client is not None

    @property
    def client(self):
        return self._client

    # ─── 集合初始化 ───
    def _init_default_collections(self) -> None:
        """启动期幂等创建默认 RAG 集合，并校验维度 / 主键。"""
        if self._client is None:
            return
        dim = int(self.cfg.rag_milvus_dim or 1024)
        try:
            if self._client.has_collection(DEFAULT_RAG_COLLECTION):
                # 集合已存在：仅校验 schema，发现不一致只 warning，不删用户数据
                self._verify_rag_schema(DEFAULT_RAG_COLLECTION, dim)
                return
            self._create_rag_collection(DEFAULT_RAG_COLLECTION, dim)
        except Exception as e:
            logger.warning("⚠️  Milvus 创建集合失败: %s", e)

    def _create_rag_collection(self, collection_name: str, dim: int) -> None:
        """以显式 schema 创建 RAG 集合：pg_id/content/embedding。"""
        if self._client is None or DataType is None:
            return
        # 显式 schema：与 Go 端 entity.Schema 对齐
        schema = self._client.create_schema(auto_id=False, enable_dynamic_field=False)
        schema.add_field(
            field_name="pg_id", datatype=DataType.INT64, is_primary=True
        )
        schema.add_field(
            field_name="content",
            datatype=DataType.VARCHAR,
            max_length=_RAG_CONTENT_MAX_LEN,
        )
        schema.add_field(
            field_name="embedding", datatype=DataType.FLOAT_VECTOR, dim=dim
        )

        # 索引参数：IVF_FLAT + L2 + nlist=128
        index_params = self._client.prepare_index_params()
        index_params.add_index(
            field_name="embedding",
            index_type=_RAG_INDEX_TYPE,
            metric_type=_RAG_METRIC_TYPE,
            params={"nlist": _RAG_INDEX_NLIST},
        )

        self._client.create_collection(
            collection_name=collection_name,
            schema=schema,
            index_params=index_params,
        )
        logger.info(
            "✅ Milvus 集合 %s 已创建 (dim=%d, index=%s, metric=%s, nlist=%d)",
            collection_name, dim, _RAG_INDEX_TYPE, _RAG_METRIC_TYPE, _RAG_INDEX_NLIST,
        )

    def _verify_rag_schema(self, collection_name: str, expected_dim: int) -> None:
        """校验已存在集合的 PK 名 / 向量维度，不一致只打 warning。"""
        if self._client is None:
            return
        try:
            desc = self._client.describe_collection(collection_name)
        except Exception as e:
            logger.warning("⚠️  Milvus describe_collection(%s) 失败: %s", collection_name, e)
            return

        fields = []
        if isinstance(desc, dict):
            fields = desc.get("fields") or []
        else:
            fields = getattr(desc, "fields", []) or []

        pk_name: Optional[str] = None
        embedding_dim: Optional[int] = None
        for f in fields:
            name = f.get("name") if isinstance(f, dict) else getattr(f, "name", None)
            is_primary = (
                f.get("is_primary") if isinstance(f, dict) else getattr(f, "is_primary", False)
            )
            if is_primary:
                pk_name = name
            if name == "embedding":
                params = (
                    f.get("params") if isinstance(f, dict) else getattr(f, "params", {})
                ) or {}
                if isinstance(params, dict):
                    raw_dim = params.get("dim")
                    try:
                        embedding_dim = int(raw_dim) if raw_dim is not None else None
                    except (TypeError, ValueError):
                        embedding_dim = None

        if pk_name is not None and pk_name != "pg_id":
            logger.warning(
                "⚠️  Milvus 集合 %s 主键字段名不一致 (expected=pg_id, actual=%s)，"
                "请手动 drop 旧集合后重新 ingest 全量数据",
                collection_name, pk_name,
            )
        if embedding_dim is not None and embedding_dim != expected_dim:
            logger.warning(
                "⚠️  Milvus 集合 %s embedding 维度不一致 (expected=%d, actual=%d)，"
                "请手动 drop 旧集合后重新 ingest 全量数据",
                collection_name, expected_dim, embedding_dim,
            )

    def ensure_collection(self, collection_name: str, dimension: int,
                          auto_id: bool = True, enable_dynamic_field: bool = True) -> bool:
        """幂等创建任意业务集合。

        对默认 RAG 集合 (rag_chunks) 走显式 schema 路径以保证 schema/index 与 Go 对齐；
        其它集合走 pymilvus 的简易 schema（dimension 参数）兼容旧调用方。
        """
        if self._client is None:
            return False
        try:
            if self._client.has_collection(collection_name):
                if collection_name == DEFAULT_RAG_COLLECTION:
                    self._verify_rag_schema(collection_name, dimension)
                return True
            if collection_name == DEFAULT_RAG_COLLECTION:
                self._create_rag_collection(collection_name, dimension)
            else:
                self._client.create_collection(
                    collection_name=collection_name,
                    dimension=dimension,
                    auto_id=auto_id,
                    enable_dynamic_field=enable_dynamic_field,
                )
                logger.info("✅ Milvus 集合 %s 已创建 (dim=%d)", collection_name, dimension)
            return True
        except Exception as e:
            logger.warning("⚠️  Milvus 创建集合失败 (%s): %s", collection_name, e)
            return False

    # ─── 关键操作 ───
    def insert(self, collection_name: str, data: List[Dict[str, Any]]) -> bool:
        """向 Milvus 插入实体记录；失败返回 False。"""
        if self._client is None or not data:
            return False
        try:
            self._client.insert(collection_name=collection_name, data=data)
            return True
        except Exception as e:
            logger.warning("⚠️  Milvus 插入失败: %s", e)
            return False

    def search(self, collection_name: str, query_emb: List[float], top_k: int,
               output_fields: Optional[List[str]] = None) -> List[Dict[str, Any]]:
        """单 query 向量检索；返回 hit 列表（pg_id/content/score）。失败返回空列表。"""
        if self._client is None:
            return []
        try:
            results = self._client.search(
                collection_name=collection_name,
                data=[query_emb],
                limit=top_k,
                output_fields=output_fields or ["pg_id", "content"],
            )
            hits: List[Dict[str, Any]] = []
            if not results:
                return hits
            for result in results[0]:
                entity = result.get("entity", {}) if isinstance(result, dict) else {}
                hits.append({
                    "pg_id": entity.get("pg_id"),
                    "content": entity.get("content"),
                    "score": result.get("distance", 0.0) if isinstance(result, dict) else 0.0,
                })
            return hits
        except Exception as e:
            logger.warning("⚠️  Milvus 检索失败: %s", e)
            return []

    def delete(self, collection_name: str, filter_expr: str) -> bool:
        """按布尔表达式删除（如 'pg_id == 123'）。"""
        if self._client is None:
            return False
        try:
            self._client.delete(collection_name=collection_name, filter=filter_expr)
            return True
        except Exception as e:
            logger.warning("⚠️  Milvus 删除失败: %s", e)
            return False

    def delete_by_pg_ids(self, collection_name: str, pg_ids: List[int]) -> None:
        if self._client is None or not pg_ids:
            return
        for pid in pg_ids:
            try:
                self._client.delete(collection_name=collection_name, filter=f"pg_id == {pid}")
            except Exception as e:
                logger.warning("⚠️  Milvus 删除失败 (pg_id=%s): %s", pid, e)

    # ─── 关闭 ───
    def close(self) -> None:
        if self._client is not None:
            try:
                self._client.close()
            except Exception as e:
                logger.warning("⚠️  Milvus 关闭失败: %s", e)
            finally:
                self._client = None
                self.status = "disconnected"
