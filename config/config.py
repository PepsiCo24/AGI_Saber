# config — 配置管理（环境变量读取 + 默认值）
import os
import yaml
import logging

logger = logging.getLogger(__name__)


class APIConfig:
    """整合所有阶段的 API + 基础设施配置"""

    def __init__(self):
        # ===== LLM 聊天模型 API =====
        self.llm_api_url = ""
        self.llm_api_key = ""
        self.llm_model = ""
        self.temperature = 0.7

        # ===== Embedding 向量化模型 API =====
        self.embedding_api_url = ""
        self.embedding_api_key = ""
        self.embedding_model = ""

        # ===== Milvus 向量数据库 =====
        self.milvus_host = ""
        self.milvus_port = 19530

        # ===== PostgreSQL 关系型数据库 =====
        self.pg_host = ""
        self.pg_port = 5432
        self.pg_user = ""
        self.pg_password = ""
        self.pg_database = ""

        # ===== Elasticsearch =====
        self.es_addresses = []
        self.es_username = ""
        self.es_password = ""

        # ===== Kafka =====
        self.kafka_brokers = []
        self.kafka_topic = ""

        # ===== RAG 配置 =====
        self.chunk_size = 200
        self.chunk_overlap = 50
        self.top_k = 3
        self.rrf_constant_k = 60
        self.semantic_weight = 0.7
        self.enable_hybrid_search = False
        self.rag_milvus_dim = 1024

        # ===== Memory 配置 =====
        self.short_term_max_turns = 10
        self.long_term_top_k = 3
        self.memory_consolidation_similarity = 0.80
        self.memory_consolidation_dedup = 0.95
        self.memory_consolidation_ttl_days = 30
        self.memory_consolidation_decay_rate = 0.995
        self.memory_consolidation_min_import = 0.3
        self.memory_consolidation_trigger = 5

        # ===== Harness 配置 =====
        self.max_retries = 3
        self.retry_delay_ms = 1000
        self.step_timeout_ms = 30000
        self.max_iterations = 10

        # ===== 搜索 API（可选，支持 Tavily 等）=====
        self.search_api_key = ""
        self.search_api_url = ""

        # ===== 通用配置 =====
        self.server_port = "8080"

    def is_real_llm(self) -> bool:
        return self.llm_api_key != ""

    def is_real_embedding(self) -> bool:
        return self.embedding_api_key != ""

    def pg_dsn(self) -> str:
        """返回 PostgreSQL 连接串"""
        return f"postgres://{self.pg_user}:{self.pg_password}@{self.pg_host}:{self.pg_port}/{self.pg_database}?sslmode=disable"

    def milvus_addr(self) -> str:
        """返回 Milvus 地址"""
        return f"{self.milvus_host}:{self.milvus_port}"


def default_config() -> APIConfig:
    """从 config/config.yaml 加载配置"""
    c = APIConfig()
    
    try:
        with open("config/config.yaml", "r", encoding="utf-8") as f:
            data = yaml.safe_load(f)
    except Exception as e:
        logger.warning(f"读取 config/config.yaml 失败，使用默认值: {e}")
        return c

    # LLM 配置
    if "llm" in data:
        llm = data["llm"]
        c.llm_api_url = llm.get("api_url", "")
        c.llm_api_key = llm.get("api_key", "")
        c.llm_model = llm.get("model", "")
        c.temperature = llm.get("temperature", 0.7)

    # Embedding 配置
    if "embedding" in data:
        emb = data["embedding"]
        c.embedding_api_url = emb.get("api_url", "")
        c.embedding_api_key = emb.get("api_key", "")
        c.embedding_model = emb.get("model", "")

    # Milvus 配置
    if "milvus" in data:
        mil = data["milvus"]
        c.milvus_host = mil.get("host", "")
        c.milvus_port = mil.get("port", 19530)

    # PostgreSQL 配置
    if "postgres" in data:
        pg = data["postgres"]
        c.pg_host = pg.get("host", "")
        c.pg_port = pg.get("port", 5432)
        c.pg_user = pg.get("user", "")
        c.pg_password = pg.get("password", "")
        c.pg_database = pg.get("database", "")

    # Elasticsearch 配置
    if "elasticsearch" in data:
        es = data["elasticsearch"]
        c.es_addresses = es.get("addresses", [])
        c.es_username = es.get("username", "")
        c.es_password = es.get("password", "")

    # Kafka 配置
    if "kafka" in data:
        kafka = data["kafka"]
        c.kafka_brokers = kafka.get("brokers", [])
        c.kafka_topic = kafka.get("topic", "")

    # RAG 配置
    if "rag" in data:
        rag = data["rag"]
        c.chunk_size = rag.get("chunk_size", 200)
        c.chunk_overlap = rag.get("chunk_overlap", 50)
        c.top_k = rag.get("top_k", 3)
        c.rrf_constant_k = rag.get("rrf_constant_k", 60)
        c.semantic_weight = rag.get("semantic_weight", 0.7)
        c.enable_hybrid_search = rag.get("enable_hybrid_search", False)
        c.rag_milvus_dim = rag.get("rag_milvus_dim", 1024)

    # Memory 配置
    if "memory" in data:
        mem = data["memory"]
        c.short_term_max_turns = mem.get("short_term_max_turns", 10)
        c.long_term_top_k = mem.get("long_term_top_k", 3)
        
        if "consolidation" in mem:
            cons = mem["consolidation"]
            c.memory_consolidation_similarity = cons.get("similarity_threshold", 0.80)
            c.memory_consolidation_dedup = cons.get("dedup_threshold", 0.95)
            c.memory_consolidation_ttl_days = cons.get("ttl_days", 30)
            c.memory_consolidation_decay_rate = cons.get("decay_rate", 0.995)
            c.memory_consolidation_min_import = cons.get("min_importance", 0.3)
            c.memory_consolidation_trigger = cons.get("trigger_interval", 5)

    # Harness 配置
    if "harness" in data:
        har = data["harness"]
        c.max_retries = har.get("max_retries", 3)
        c.retry_delay_ms = har.get("retry_delay_ms", 1000)
        c.step_timeout_ms = har.get("step_timeout_ms", 30000)
        c.max_iterations = har.get("max_iterations", 10)

    # Search 配置
    if "search" in data:
        search = data["search"]
        c.search_api_key = search.get("api_key", "")
        c.search_api_url = search.get("api_url", "")

    # Server 配置
    if "server" in data:
        c.server_port = data["server"].get("port", "8080")

    # RAG 混合检索默认值
    if c.rrf_constant_k <= 0:
        c.rrf_constant_k = 60
    if c.semantic_weight <= 0:
        c.semantic_weight = 0.7
    if c.rag_milvus_dim <= 0:
        c.rag_milvus_dim = 1024

    # 记忆合并默认值
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

    return c
