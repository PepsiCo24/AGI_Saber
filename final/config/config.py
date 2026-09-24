# config — 配置管理（与主分支 Go 版字段对齐的 Python 配置）
import logging
import os
from typing import Any, Dict, List, Optional

import yaml

logger = logging.getLogger(__name__)

# 项目根目录（final/）：本文件位于 final/config/config.py
DEFAULT_PROJECT_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def _project_root() -> str:
    return os.environ.get("AGI_PROJECT_ROOT", DEFAULT_PROJECT_ROOT)


_CONFIG_SCHEMA = {
    "llm": {"api_url", "api_key", "model", "temperature"},
    "embedding": {"api_url", "api_key", "model"},
    "milvus": {"host", "port"},
    "postgres": {"host", "port", "user", "password", "database"},
    "elasticsearch": {"addresses", "username", "password"},
    "kafka": {"brokers", "topic"},
    "neo4j": {"uri", "user", "password", "max_hops", "weight", "enabled"},
    "rag": {
        "chunk_size",
        "chunk_overlap",
        "top_k",
        "rrf_constant_k",
        "semantic_weight",
        "enable_hybrid_search",
        "rag_milvus_dim",
        "rewrite",
        "rerank",
    },
    "memory": {"short_term_max_turns", "long_term_top_k", "consolidation"},
    "harness": {"max_retries", "retry_delay_ms", "step_timeout_ms", "max_iterations"},
    "graph_runtime": {"max_parallel", "race_timeout_ms", "enable_racing"},
    "search": {"api_key", "api_url"},
    "server": {"port", "cors_origins"},
    "sandbox": {
        "enabled",
        "backend",
        "image",
        "timeout_ms",
        "max_output_bytes",
        "memory_limit_mb",
        "cpu_percent",
        "max_pids",
        "network_disabled",
        "readonly_rootfs",
    },
    "security": {"max_command_length", "allowlist_mode", "allowlist"},
}

_NESTED_CONFIG_SCHEMA = {
    ("rag", "rewrite"): {"enabled", "num_queries"},
    ("rag", "rerank"): {"enabled", "preview_len"},
    ("memory", "consolidation"): {
        "similarity_threshold",
        "dedup_threshold",
        "ttl_days",
        "decay_rate",
        "min_importance",
        "trigger_interval",
    },
}


class APIConfig:
    """整合 Python 版主干能力的运行配置（字段名与 Go 版 APIConfig 对齐）。"""

    def __init__(self):
        # ---- LLM / Embedding ----
        self.llm_api_url = ""
        self.llm_api_key = ""
        self.llm_model = ""
        self.temperature = 0.7
        self.embedding_api_url = ""
        self.embedding_api_key = ""
        self.embedding_model = ""

        # ---- Milvus ----
        self.milvus_host = ""
        self.milvus_port = 19530

        # ---- Postgres ----
        self.pg_host = ""
        self.pg_port = 5432
        self.pg_user = ""
        self.pg_password = ""
        self.pg_database = ""

        # ---- Elasticsearch ----
        self.es_addresses: List[str] = []
        self.es_username = ""
        self.es_password = ""

        # ---- Kafka ----
        self.kafka_brokers: List[str] = []
        self.kafka_topic = ""

        # ---- Neo4j（知识图谱）----
        self.neo4j_uri = ""
        self.neo4j_user = ""
        self.neo4j_password = ""
        self.kg_max_hops = 2
        self.kg_weight = 0.3
        self.kg_enabled = False

        # ---- RAG ----
        self.chunk_size = 200
        self.chunk_overlap = 50
        self.top_k = 3
        self.rrf_constant_k = 60
        self.semantic_weight = 0.7
        self.enable_hybrid_search = False
        self.rag_milvus_dim = 1024
        self.rag_rewrite_enabled = False
        self.rag_rewrite_num_queries = 3
        self.rag_rerank_enabled = False
        self.rag_rerank_preview_len = 200

        # ---- Memory ----
        self.short_term_max_turns = 5
        self.long_term_top_k = 3
        self.memory_consolidation_similarity = 0.80
        self.memory_consolidation_dedup = 0.95
        self.memory_consolidation_ttl_days = 30
        self.memory_consolidation_decay_rate = 0.995
        self.memory_consolidation_min_import = 0.3
        self.memory_consolidation_trigger = 5

        # ---- Harness ----
        self.max_retries = 3
        self.retry_delay_ms = 200
        self.step_timeout_ms = 5000
        self.max_iterations = 5

        # ---- Graph Runtime ----
        self.graph_max_parallel = 2
        self.graph_race_timeout_ms = 30000
        self.graph_enable_racing = True

        # ---- Search ----
        self.search_api_key = ""
        self.search_api_url = ""

        # ---- Server ----
        self.server_port = "8090"
        self.cors_origins: List[str] = []

        # ---- Sandbox ----
        self.sandbox_enabled = False
        self.sandbox_backend = "docker"
        self.sandbox_image = "ubuntu:22.04"
        self.sandbox_timeout_ms = 30000
        self.sandbox_max_output = 65536
        self.sandbox_memory_mb = 256
        self.sandbox_cpu_percent = 50
        self.sandbox_max_pids = 64
        self.sandbox_net_disabled = True
        self.sandbox_readonly = True

        # ---- Security ----
        self.sec_max_cmd_length = 500
        self.sec_allowlist_mode = False
        self.sec_allowlist: List[str] = []

    # ---- helpers ----
    def is_real_llm(self) -> bool:
        return bool(self.llm_api_key)

    def is_real_embedding(self) -> bool:
        return bool(self.embedding_api_key)

    def pg_dsn(self) -> str:
        return (
            f"postgres://{self.pg_user}:{self.pg_password}"
            f"@{self.pg_host}:{self.pg_port}/{self.pg_database}?sslmode=disable"
        )

    def milvus_addr(self) -> str:
        return f"{self.milvus_host}:{self.milvus_port}"


def _read_yaml(path: str) -> Dict[str, Any]:
    try:
        with open(path, "r", encoding="utf-8") as f:
            return yaml.safe_load(f) or {}
    except Exception as e:
        logger.warning("读取 %s 失败，使用默认值: %s", path, e)
        return {}


def _validate_config_schema(data: Dict[str, Any]) -> None:
    for section, value in (data or {}).items():
        if section not in _CONFIG_SCHEMA:
            raise ValueError(f"unknown config field: {section}")
        if value is None:
            continue
        if not isinstance(value, dict):
            raise ValueError(f"config section {section} must be a mapping")
        allowed = _CONFIG_SCHEMA[section]
        for key, nested in value.items():
            if key not in allowed:
                raise ValueError(f"unknown config field: {section}.{key}")
            nested_allowed = _NESTED_CONFIG_SCHEMA.get((section, key))
            if nested_allowed is None or nested is None:
                continue
            if not isinstance(nested, dict):
                raise ValueError(f"config section {section}.{key} must be a mapping")
            for nested_key in nested:
                if nested_key not in nested_allowed:
                    raise ValueError(f"unknown config field: {section}.{key}.{nested_key}")


def _resolve_config_path(explicit: Optional[str]) -> str:
    """按优先级找 config：参数 > 环境变量 > 本地配置 > 项目默认 > cwd。"""
    candidates: List[str] = []
    if explicit:
        candidates.append(explicit)
    env = os.environ.get("AGI_CONFIG")
    if env:
        candidates.append(env)
    root = _project_root()
    candidates.append(os.path.join(root, "config", "config.local.yaml"))
    candidates.append(os.path.join(root, "config", "config.yaml"))
    candidates.append("config/config.yaml")
    for p in candidates:
        if p and os.path.isfile(p):
            return p
    return candidates[-1]


def default_config(config_path: Optional[str] = None) -> APIConfig:
    """从 config.yaml 加载配置（与 Go 版 DefaultConfig 字段一一对齐）。"""
    c = APIConfig()
    path = _resolve_config_path(config_path)
    data = _read_yaml(path)
    _validate_config_schema(data)

    if llm := data.get("llm"):
        c.llm_api_url = llm.get("api_url", "")
        c.llm_api_key = llm.get("api_key", "")
        c.llm_model = llm.get("model", "")
        c.temperature = llm.get("temperature", 0.7)

    if emb := data.get("embedding"):
        c.embedding_api_url = emb.get("api_url", "")
        c.embedding_api_key = emb.get("api_key", "")
        c.embedding_model = emb.get("model", "")

    if milvus := data.get("milvus"):
        c.milvus_host = milvus.get("host", "")
        c.milvus_port = milvus.get("port", 19530)

    if pg := data.get("postgres"):
        c.pg_host = pg.get("host", "")
        c.pg_port = pg.get("port", 5432)
        c.pg_user = pg.get("user", "")
        c.pg_password = pg.get("password", "")
        c.pg_database = pg.get("database", "")

    if es := data.get("elasticsearch"):
        c.es_addresses = es.get("addresses", []) or []
        c.es_username = es.get("username", "")
        c.es_password = es.get("password", "")

    if kafka := data.get("kafka"):
        c.kafka_brokers = kafka.get("brokers", []) or []
        c.kafka_topic = kafka.get("topic", "")

    if neo4j := data.get("neo4j"):
        c.neo4j_uri = neo4j.get("uri", "")
        c.neo4j_user = neo4j.get("user", "")
        c.neo4j_password = neo4j.get("password", "")
        c.kg_max_hops = neo4j.get("max_hops", 2)
        c.kg_weight = neo4j.get("weight", 0.3)
        c.kg_enabled = bool(neo4j.get("enabled", False))

    if rag := data.get("rag"):
        c.chunk_size = rag.get("chunk_size", 200)
        c.chunk_overlap = rag.get("chunk_overlap", 50)
        c.top_k = rag.get("top_k", 3)
        c.rrf_constant_k = rag.get("rrf_constant_k", 60)
        c.semantic_weight = rag.get("semantic_weight", 0.7)
        c.enable_hybrid_search = rag.get("enable_hybrid_search", False)
        c.rag_milvus_dim = rag.get("rag_milvus_dim", 1024)
        rewrite = rag.get("rewrite", {}) or {}
        c.rag_rewrite_enabled = bool(rewrite.get("enabled", False))
        c.rag_rewrite_num_queries = rewrite.get("num_queries", 3)
        rerank = rag.get("rerank", {}) or {}
        c.rag_rerank_enabled = bool(rerank.get("enabled", False))
        c.rag_rerank_preview_len = rerank.get("preview_len", 200)

    if memory := data.get("memory"):
        c.short_term_max_turns = memory.get("short_term_max_turns", 5)
        c.long_term_top_k = memory.get("long_term_top_k", 3)
        cons = memory.get("consolidation", {}) or {}
        c.memory_consolidation_similarity = cons.get("similarity_threshold", 0.80)
        c.memory_consolidation_dedup = cons.get("dedup_threshold", 0.95)
        c.memory_consolidation_ttl_days = cons.get("ttl_days", 30)
        c.memory_consolidation_decay_rate = cons.get("decay_rate", 0.995)
        c.memory_consolidation_min_import = cons.get("min_importance", 0.3)
        c.memory_consolidation_trigger = cons.get("trigger_interval", 5)

    if harness := data.get("harness"):
        c.max_retries = harness.get("max_retries", 3)
        c.retry_delay_ms = harness.get("retry_delay_ms", 200)
        c.step_timeout_ms = harness.get("step_timeout_ms", 5000)
        c.max_iterations = harness.get("max_iterations", 5)

    if graph_runtime := data.get("graph_runtime"):
        c.graph_max_parallel = graph_runtime.get("max_parallel", 2)
        c.graph_race_timeout_ms = graph_runtime.get("race_timeout_ms", 30000)
        c.graph_enable_racing = bool(graph_runtime.get("enable_racing", True))

    if search := data.get("search"):
        c.search_api_key = search.get("api_key", "")
        c.search_api_url = search.get("api_url", "")

    if server := data.get("server"):
        c.server_port = str(server.get("port", "8090"))
        c.cors_origins = server.get("cors_origins", []) or []

    if sb := data.get("sandbox"):
        c.sandbox_enabled = bool(sb.get("enabled", False))
        c.sandbox_backend = sb.get("backend", "docker")
        c.sandbox_image = sb.get("image", "ubuntu:22.04")
        c.sandbox_timeout_ms = sb.get("timeout_ms", 30000)
        c.sandbox_max_output = sb.get("max_output_bytes", 65536)
        c.sandbox_memory_mb = sb.get("memory_limit_mb", 256)
        c.sandbox_cpu_percent = sb.get("cpu_percent", 50)
        c.sandbox_max_pids = sb.get("max_pids", 64)
        c.sandbox_net_disabled = bool(sb.get("network_disabled", True))
        c.sandbox_readonly = bool(sb.get("readonly_rootfs", True))

    if sec := data.get("security"):
        c.sec_max_cmd_length = sec.get("max_command_length", 500)
        c.sec_allowlist_mode = bool(sec.get("allowlist_mode", False))
        c.sec_allowlist = sec.get("allowlist", []) or []

    _apply_defaults(c)
    return c


def _apply_defaults(c: APIConfig) -> None:
    if c.rrf_constant_k <= 0:
        c.rrf_constant_k = 60
    if c.semantic_weight <= 0:
        c.semantic_weight = 0.7
    if c.rag_milvus_dim <= 0:
        c.rag_milvus_dim = 1024
    if c.rag_rewrite_num_queries <= 0:
        c.rag_rewrite_num_queries = 3
    if c.rag_rerank_preview_len <= 0:
        c.rag_rerank_preview_len = 200

    if c.memory_consolidation_similarity <= 0:
        c.memory_consolidation_similarity = 0.80
    if c.memory_consolidation_dedup <= 0:
        c.memory_consolidation_dedup = 0.95
    if c.memory_consolidation_ttl_days <= 0:
        c.memory_consolidation_ttl_days = 30
    if c.memory_consolidation_decay_rate <= 0:
        c.memory_consolidation_decay_rate = 0.995
    if c.memory_consolidation_min_import <= 0:
        c.memory_consolidation_min_import = 0.3
    if c.memory_consolidation_trigger <= 0:
        c.memory_consolidation_trigger = 5

    if c.kg_max_hops <= 0:
        c.kg_max_hops = 2
    if c.kg_weight <= 0:
        c.kg_weight = 0.3

    if c.graph_max_parallel <= 0:
        c.graph_max_parallel = 2
    if c.graph_race_timeout_ms <= 0:
        c.graph_race_timeout_ms = 30000

    if not c.sandbox_backend:
        c.sandbox_backend = "docker"
    if not c.sandbox_image:
        c.sandbox_image = "ubuntu:22.04"
    if c.sandbox_timeout_ms <= 0:
        c.sandbox_timeout_ms = 30000
    if c.sandbox_max_output <= 0:
        c.sandbox_max_output = 65536
    if c.sandbox_memory_mb <= 0:
        c.sandbox_memory_mb = 256
    if c.sandbox_cpu_percent <= 0:
        c.sandbox_cpu_percent = 50
    if c.sandbox_max_pids <= 0:
        c.sandbox_max_pids = 64

    if c.sec_max_cmd_length <= 0:
        c.sec_max_cmd_length = 500
