# platform — 外部基础设施薄封装层。
# 每个 client 类负责对应资源的连接、健康检查、关键操作和优雅关闭；
# 失败时降级到 mock 模式（is_real() 返回 False），不阻塞应用启动。
from .es import ESClient
from .kafka import KafkaClient
from .milvus import MilvusClientWrapper as MilvusClient
from .neo4j import Neo4jClient
from .postgres import PostgresClient

__all__ = [
    "PostgresClient",
    "ESClient",
    "MilvusClient",
    "KafkaClient",
    "Neo4jClient",
]
