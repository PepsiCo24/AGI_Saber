# infra — 管理所有外部基础设施连接：Milvus / PostgreSQL / Elasticsearch / Kafka
# 每个连接失败时优雅降级，不影响应用启动。
#
# 业务持久化逻辑统一收敛到 internal.repo 包：Infrastructure 仅负责连接生命周期
# (connect / schema bootstrap / health) 与跨域装配 self.repo 仓储入口。
import json
import logging
from dataclasses import dataclass
from types import SimpleNamespace
from typing import Any, Iterable, List, Optional, Sequence, Tuple

from config.config import APIConfig
from internal.repo import chathistory, documentrepo, eventbus, longterm, preference, ragchunk, snapshot

logger = logging.getLogger(__name__)

# 尝试导入可选依赖，失败则标记不可用
try:
    import psycopg2
    _HAS_PG = True
except ImportError:
    _HAS_PG = False

try:
    from elasticsearch import Elasticsearch
    _HAS_ES = True
except ImportError:
    _HAS_ES = False

try:
    from kafka import KafkaProducer
    _HAS_KAFKA = True
except ImportError:
    _HAS_KAFKA = False

try:
    from pymilvus import DataType, MilvusClient
    _HAS_MILVUS = True
except ImportError:
    DataType = None  # type: ignore
    _HAS_MILVUS = False


# 默认 RAG 集合名（与 main 分支 Go 实现 internal/infrastructure/persistence/ragchunk 对齐）
RAG_COLLECTION = "rag_chunks"
# 索引参数（与 Go 端 entity.NewIndexIvfFlat(entity.L2, 128) 对齐）
_RAG_INDEX_TYPE = "IVF_FLAT"
_RAG_METRIC_TYPE = "L2"
_RAG_INDEX_NLIST = 128
# content 字段最大长度（与 Go 端 max_length=4096 对齐）
_RAG_CONTENT_MAX_LEN = 4096


@dataclass
class Status:
    milvus: str = "disconnected"
    postgresql: str = "disconnected"
    elasticsearch: str = "disconnected"
    kafka: str = "disconnected"


@dataclass
class LongTermRow:
    id: int
    content: str
    importance: float
    embedding: Optional[List[float]] = None


# ─────────────────────── repo 适配层 ──────────────────────────────────────
# repo 期望 internal.platform 风格的 client（含 is_real()/query/exec/conn 等）。
# 这里把 Infrastructure 已经持有的 raw 连接句柄包成符合接口的 thin adapter，
# 避免重复 connect。

class _PGAdapter:
    """把 psycopg2 raw connection 包成 PostgresClient-like 接口供 repo 使用。"""

    def __init__(self, conn):
        self._conn = conn

    def is_real(self) -> bool:
        return self._conn is not None

    @property
    def conn(self):
        return self._conn

    def query(self, sql: str, params: Optional[Sequence[Any]] = None) -> List[Tuple[Any, ...]]:
        if self._conn is None:
            return []
        try:
            with self._conn.cursor() as cur:
                cur.execute(sql, params or ())
                return list(cur.fetchall())
        except Exception as e:
            logger.warning("⚠️  PG query 失败: %s", e)
            return []

    def query_one(self, sql: str, params: Optional[Sequence[Any]] = None) -> Optional[Tuple[Any, ...]]:
        if self._conn is None:
            return None
        try:
            with self._conn.cursor() as cur:
                cur.execute(sql, params or ())
                return cur.fetchone()
        except Exception as e:
            logger.warning("⚠️  PG query_one 失败: %s", e)
            return None

    def exec(self, sql: str, params: Optional[Sequence[Any]] = None) -> int:
        if self._conn is None:
            return -1
        try:
            with self._conn.cursor() as cur:
                cur.execute(sql, params or ())
                return cur.rowcount
        except Exception as e:
            logger.warning("⚠️  PG exec 失败: %s", e)
            return -1

    def exec_many(self, sql: str, seq_of_params: Iterable[Sequence[Any]]) -> int:
        if self._conn is None:
            return -1
        try:
            with self._conn.cursor() as cur:
                cur.executemany(sql, list(seq_of_params))
                return cur.rowcount
        except Exception as e:
            logger.warning("⚠️  PG exec_many 失败: %s", e)
            return -1


class _ESAdapter:
    """把 raw Elasticsearch 客户端包成 ESClient-like 接口供 repo 使用。"""

    def __init__(self, es):
        self._es = es

    def is_real(self) -> bool:
        return self._es is not None

    @property
    def client(self):
        return self._es

    def index(self, index: str, doc_id: Any, body: dict) -> bool:
        if self._es is None:
            return False
        try:
            self._es.index(index=index, id=doc_id, body=body)
            return True
        except Exception as e:
            logger.warning("⚠️  ES 索引失败: %s", e)
            return False

    def search(self, index: str, body: dict) -> dict:
        if self._es is None:
            return {}
        try:
            resp = self._es.search(index=index, body=body)
            return dict(resp)
        except Exception as e:
            logger.warning("⚠️  ES 检索失败: %s", e)
            return {}

    def delete_many(self, index: str, doc_ids: List[Any]) -> None:
        if self._es is None:
            return
        for doc_id in doc_ids:
            try:
                self._es.delete(index=index, id=doc_id)
            except Exception as e:
                logger.warning("⚠️  ES 删除失败 (id=%s): %s", doc_id, e)


class _MilvusAdapter:
    """把 raw MilvusClient 包成 MilvusClientWrapper-like 接口供 repo 使用。"""

    def __init__(self, client):
        self._client = client

    def is_real(self) -> bool:
        return self._client is not None

    @property
    def client(self):
        return self._client

    def ensure_collection(self, collection_name: str, dimension: int,
                          auto_id: bool = True, enable_dynamic_field: bool = True) -> bool:
        # rag_chunks 集合由 _init_milvus_collections 在启动期建好；这里仅做存在性确认。
        if self._client is None:
            return False
        try:
            return bool(self._client.has_collection(collection_name))
        except Exception:
            return False

    def insert(self, collection_name: str, data: List[dict]) -> bool:
        if self._client is None or not data:
            return False
        try:
            self._client.insert(collection_name=collection_name, data=data)
            return True
        except Exception as e:
            logger.warning("⚠️  Milvus 插入失败: %s", e)
            return False

    def search(self, collection_name: str, query_emb: List[float], top_k: int,
               output_fields: Optional[List[str]] = None) -> List[dict]:
        if self._client is None:
            return []
        try:
            results = self._client.search(
                collection_name=collection_name,
                data=[query_emb],
                limit=top_k,
                output_fields=output_fields or ["pg_id", "content"],
            )
            hits: List[dict] = []
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
        if self._client is None:
            return False
        try:
            self._client.delete(collection_name=collection_name, filter=filter_expr)
            return True
        except Exception as e:
            logger.warning("⚠️  Milvus 删除失败: %s", e)
            return False


class _KafkaAdapter:
    """把 raw KafkaProducer 包成 KafkaClient-like 接口供 repo.eventbus 使用。"""

    def __init__(self, producer, cfg: APIConfig, ready: Status):
        self._producer = producer
        self._cfg = cfg
        self._ready = ready

    def is_real(self) -> bool:
        return self._producer is not None and self._ready.kafka == "connected"

    def produce(self, event_type: str, payload, topic: Optional[str] = None) -> bool:
        target_topic = topic or self._cfg.kafka_topic
        if not self.is_real():
            return False
        try:
            self._producer.send(
                target_topic,
                key=event_type.encode("utf-8"),
                value=payload,
            )
            return True
        except Exception as e:
            logger.warning("⚠️  Kafka 写入失败: %s", e)
            return False


class Infrastructure:
    """持有所有外部连接句柄"""

    def __init__(self, cfg: APIConfig):
        self.cfg = cfg
        self.ready = Status()
        self._pg = None
        self._es = None
        self._kafka_producer = None
        self._milvus = None

        self._connect_postgres()
        self._connect_es()
        self._connect_kafka()
        self._connect_milvus()

        # ─── repo 装配（统一持久化入口） ────────────────────────────────
        # 业务侧应通过 inf.repo.<domain>.<method>(...) 访问；此处不重复 connect，
        # 只把已有句柄包成符合 internal.platform 接口的 thin adapter 注入 repo。
        pg_client = _PGAdapter(self._pg)
        es_client = _ESAdapter(self._es)
        milvus_client = _MilvusAdapter(self._milvus)
        kafka_client = _KafkaAdapter(self._kafka_producer, self.cfg, self.ready)

        self.repo = SimpleNamespace(
            ragchunk=ragchunk.Store(pg_client, milvus_client, es_client),
            ltm=longterm.PGRepo(pg_client),
            chat_history=chathistory.PGRepo(pg_client),
            preference=preference.PGRepo(pg_client),
            snapshot=snapshot.PGRepo(pg_client),
            documents=documentrepo.Store(pg_client),
            events=eventbus.KafkaPublisher(kafka_client),
        )

    # ─────────────────────────────── PostgreSQL ───────────────────────────────

    def _connect_postgres(self):
        if not _HAS_PG:
            logger.warning("⚠️  psycopg2 未安装，PostgreSQL 不可用")
            return
        if not self.cfg.pg_host:
            logger.warning("⚠️  PostgreSQL 未配置")
            return
        try:
            self._pg = psycopg2.connect(self.cfg.pg_dsn())
            self._pg.autocommit = True
            with self._pg.cursor() as cur:
                cur.execute("SELECT 1")
            self.ready.postgresql = "connected"
            self._init_pg_schema()
            logger.info("✅ PostgreSQL 已连接: %s", self.cfg.pg_dsn())
        except Exception as e:
            logger.warning("⚠️  PostgreSQL 连接失败: %s", e)
            self._pg = None

    def _init_pg_schema(self):
        if not self._pg:
            return
        ddls = [
            """CREATE TABLE IF NOT EXISTS user_preferences (
                user_id    TEXT NOT NULL,
                key        TEXT NOT NULL,
                value      TEXT NOT NULL,
                updated_at TIMESTAMP DEFAULT NOW(),
                PRIMARY KEY (user_id, key)
            )""",
            """CREATE TABLE IF NOT EXISTS task_snapshots (
                task_id    TEXT PRIMARY KEY,
                state      JSONB NOT NULL,
                created_at TIMESTAMP DEFAULT NOW()
            )""",
            """CREATE TABLE IF NOT EXISTS chat_history (
                id         SERIAL PRIMARY KEY,
                role       TEXT NOT NULL,
                content    TEXT NOT NULL,
                created_at TIMESTAMP DEFAULT NOW()
            )""",
            """CREATE TABLE IF NOT EXISTS long_term_memory (
                id            SERIAL PRIMARY KEY,
                content       TEXT NOT NULL,
                importance    FLOAT NOT NULL DEFAULT 0.5,
                embedding     JSONB,
                created_at    DOUBLE PRECISION DEFAULT EXTRACT(EPOCH FROM NOW()),
                last_accessed DOUBLE PRECISION DEFAULT EXTRACT(EPOCH FROM NOW()),
                category      VARCHAR(64) NOT NULL DEFAULT '',
                tags          JSONB NOT NULL DEFAULT '[]'::jsonb,
                slot_hint     VARCHAR(64) NOT NULL DEFAULT '',
                score         DOUBLE PRECISION NOT NULL DEFAULT 0.0
            )""",
            # 老库平滑加列：每条 ALTER 独立执行，互不阻塞
            "ALTER TABLE long_term_memory ADD COLUMN IF NOT EXISTS created_at    DOUBLE PRECISION DEFAULT EXTRACT(EPOCH FROM NOW())",
            "ALTER TABLE long_term_memory ADD COLUMN IF NOT EXISTS last_accessed DOUBLE PRECISION DEFAULT EXTRACT(EPOCH FROM NOW())",
            "ALTER TABLE long_term_memory ADD COLUMN IF NOT EXISTS category      VARCHAR(64) NOT NULL DEFAULT ''",
            "ALTER TABLE long_term_memory ADD COLUMN IF NOT EXISTS tags          JSONB NOT NULL DEFAULT '[]'::jsonb",
            "ALTER TABLE long_term_memory ADD COLUMN IF NOT EXISTS slot_hint     VARCHAR(64) NOT NULL DEFAULT ''",
            "ALTER TABLE long_term_memory ADD COLUMN IF NOT EXISTS score         DOUBLE PRECISION NOT NULL DEFAULT 0.0",
            "CREATE INDEX IF NOT EXISTS idx_lti_category ON long_term_memory(category)",
            "CREATE INDEX IF NOT EXISTS idx_lti_tags     ON long_term_memory USING GIN(tags)",
            """CREATE TABLE IF NOT EXISTS rag_chunks (
                id          BIGSERIAL PRIMARY KEY,
                doc_hash    TEXT NOT NULL,
                chunk_idx   INT NOT NULL,
                content     TEXT NOT NULL,
                parent_content TEXT,
                embedding   JSONB,
                created_at  TIMESTAMP DEFAULT NOW(),
                UNIQUE (doc_hash, chunk_idx)
            )""",
            """ALTER TABLE rag_chunks ADD COLUMN IF NOT EXISTS parent_content TEXT""",
            """CREATE UNIQUE INDEX IF NOT EXISTS rag_chunks_doc_hash_chunk_idx_key
                   ON rag_chunks (doc_hash, chunk_idx)""",
            """CREATE TABLE IF NOT EXISTS documents (
                id          TEXT PRIMARY KEY,
                title       TEXT NOT NULL,
                doc_type    TEXT NOT NULL,
                source      TEXT NOT NULL,
                status      TEXT NOT NULL DEFAULT 'active',
                created_by  TEXT NOT NULL,
                created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
            )""",
            """CREATE TABLE IF NOT EXISTS document_versions (
                id          TEXT PRIMARY KEY,
                document_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
                version     INT NOT NULL,
                content_md  TEXT NOT NULL,
                summary     TEXT,
                metadata    JSONB NOT NULL DEFAULT '{}'::jsonb,
                created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                UNIQUE (document_id, version)
            )""",
            "CREATE INDEX IF NOT EXISTS idx_documents_updated_at ON documents(updated_at DESC)",
            "CREATE INDEX IF NOT EXISTS idx_document_versions_document_id_version ON document_versions(document_id, version DESC)",
        ]
        with self._pg.cursor() as cur:
            for ddl in ddls:
                try:
                    cur.execute(ddl)
                except Exception as e:
                    logger.warning("⚠️  PG 建表失败: %s", e)
        logger.info("✅ PostgreSQL 表结构已初始化")

    # ─────────────────────────────── Elasticsearch ───────────────────────────

    def _connect_es(self):
        if not _HAS_ES:
            logger.warning("⚠️  elasticsearch-py 未安装，ES 不可用")
            return
        if not self.cfg.es_addresses:
            logger.warning("⚠️  Elasticsearch 未配置")
            return
        try:
            auth = (self.cfg.es_username, self.cfg.es_password) if self.cfg.es_username else None
            self._es = Elasticsearch(
                self.cfg.es_addresses,
                basic_auth=auth,
            )
            if self._es.ping():
                self.ready.elasticsearch = "connected"
                logger.info("✅ Elasticsearch 已连接: %s", self.cfg.es_addresses)
            else:
                self._es = None
        except Exception as e:
            logger.warning("⚠️  Elasticsearch 连接失败: %s", e)
            self._es = None

    # ─────────────────────────────── Kafka ───────────────────────────────────

    def _connect_kafka(self):
        if not _HAS_KAFKA:
            logger.warning("⚠️  kafka-python 未安装，Kafka 不可用")
            return
        if not self.cfg.kafka_brokers:
            logger.warning("⚠️  Kafka 未配置")
            return
        try:
            self._kafka_producer = KafkaProducer(
                bootstrap_servers=self.cfg.kafka_brokers,
                value_serializer=lambda v: json.dumps(v).encode("utf-8"),
            )
            self.ready.kafka = "connected"
            logger.info("✅ Kafka 已连接: %s", self.cfg.kafka_brokers)
        except Exception as e:
            logger.warning("⚠️  Kafka 连接失败: %s (事件将输出到日志)", e)
            self._kafka_producer = None

    # ─────────────────────────────── Milvus ───────────────────────────────────

    def _connect_milvus(self):
        if not _HAS_MILVUS:
            logger.warning("⚠️  pymilvus 未安装，Milvus 不可用")
            self.ready.milvus = "memory-mode"
            return
        if not self.cfg.milvus_host or not self.cfg.milvus_port:
            logger.warning("⚠️  Milvus 未配置")
            self.ready.milvus = "memory-mode"
            return
        try:
            uri = f"http://{self.cfg.milvus_host}:{self.cfg.milvus_port}"
            self._milvus = MilvusClient(uri=uri)
            self.ready.milvus = "connected"
            logger.info("✅ Milvus 已连接: %s", uri)
            self._init_milvus_collections()
        except Exception as e:
            logger.warning("⚠️  Milvus 连接失败: %s (降级到内存模式)", e)
            self._milvus = None
            self.ready.milvus = "memory-mode"

    def _init_milvus_collections(self):
        if not self._milvus:
            return
        dim = int(self.cfg.rag_milvus_dim or 1024)
        try:
            if self._milvus.has_collection(RAG_COLLECTION):
                # 集合已存在：仅校验 schema，不一致只 warning，不删用户数据
                self._verify_milvus_rag_schema(RAG_COLLECTION, dim)
                return
            self._create_milvus_rag_collection(RAG_COLLECTION, dim)
        except Exception as e:
            logger.warning("⚠️  Milvus 创建集合失败: %s", e)

    def _create_milvus_rag_collection(self, collection_name: str, dim: int):
        """以显式 schema 创建 RAG 集合：pg_id (PK Int64) / content (VarChar 4096) / embedding。

        与 main 分支 Go 实现 EnsureMilvusCollection 对齐。
        """
        if not self._milvus or DataType is None:
            return
        schema = self._milvus.create_schema(auto_id=False, enable_dynamic_field=False)
        schema.add_field(field_name="pg_id", datatype=DataType.INT64, is_primary=True)
        schema.add_field(
            field_name="content",
            datatype=DataType.VARCHAR,
            max_length=_RAG_CONTENT_MAX_LEN,
        )
        schema.add_field(
            field_name="embedding", datatype=DataType.FLOAT_VECTOR, dim=dim
        )

        index_params = self._milvus.prepare_index_params()
        index_params.add_index(
            field_name="embedding",
            index_type=_RAG_INDEX_TYPE,
            metric_type=_RAG_METRIC_TYPE,
            params={"nlist": _RAG_INDEX_NLIST},
        )

        self._milvus.create_collection(
            collection_name=collection_name,
            schema=schema,
            index_params=index_params,
        )
        logger.info(
            "✅ Milvus 集合 %s 已创建 (dim=%d, index=%s, metric=%s, nlist=%d)",
            collection_name, dim, _RAG_INDEX_TYPE, _RAG_METRIC_TYPE, _RAG_INDEX_NLIST,
        )

    def _verify_milvus_rag_schema(self, collection_name: str, expected_dim: int):
        """校验已存在集合的 PK 字段名 / 向量维度，不一致只打 warning，不 drop。"""
        if not self._milvus:
            return
        try:
            desc = self._milvus.describe_collection(collection_name)
        except Exception as e:
            logger.warning("⚠️  Milvus describe_collection(%s) 失败: %s", collection_name, e)
            return

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

    # ─────────────────────────────── 生命周期 ────────────────────────────────

    def close(self):
        if self._pg:
            try:
                self._pg.close()
            except Exception as e:
                logger.warning("⚠️  PG 关闭失败: %s", e)
        if self._kafka_producer:
            try:
                self._kafka_producer.flush()
                self._kafka_producer.close()
            except Exception as e:
                logger.warning("⚠️  Kafka 关闭失败: %s", e)
        if self._es:
            try:
                self._es.close()
            except Exception as e:
                logger.warning("⚠️  ES 关闭失败: %s", e)
        if self._milvus:
            try:
                self._milvus.close()
            except Exception as e:
                logger.warning("⚠️  Milvus 关闭失败: %s", e)
