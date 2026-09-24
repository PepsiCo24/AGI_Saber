# types — 知识图谱中的实体 / 关系 / 检索结果数据结构
from dataclasses import dataclass, field
from typing import List


# EntityType 实体类型枚举（字符串值，与 Go 版保持一致）
EntityType = str

ENTITY_PERSON: EntityType = "Person"
ENTITY_ORG: EntityType = "Organization"
ENTITY_LOCATION: EntityType = "Location"
ENTITY_CONCEPT: EntityType = "Concept"
ENTITY_EVENT: EntityType = "Event"
ENTITY_PRODUCT: EntityType = "Product"
ENTITY_UNKNOWN: EntityType = "Unknown"


@dataclass
class Entity:
    """知识图谱中的一个节点"""
    name: str = ""
    type: EntityType = ENTITY_UNKNOWN
    doc_hash: str = ""
    chunk_id: int = 0  # 文档内的 chunk idx（0-based）
    pg_id: int = 0     # PG 自增 ID（用于 RAG 检索 join 回真实 chunk）


@dataclass
class Relation:
    """两个实体之间的有向边"""
    from_name: str = ""
    to_name: str = ""
    rel_type: str = ""  # RELATES_TO / PART_OF / CAUSES / DESCRIBES / MENTIONS
    weight: float = 0.0
    doc_hash: str = ""
    chunk_id: int = 0
    pg_id: int = 0


@dataclass
class GraphSearchResult:
    """一次图检索的单条结果"""
    chunk_id: int = 0       # 文档内 idx（兼容字段）
    pg_id: int = 0          # PG 自增 ID，用于 RAG RRF 融合
    score: float = 0.0      # 基于路径跳数和匹配数量的综合分
    entities: List[str] = field(default_factory=list)  # 命中的实体名称
    hop_path: List[str] = field(default_factory=list)  # 遍历路径（可解释性）


@dataclass
class ExtractResult:
    """Extractor.extract 的输出"""
    entities: List[Entity] = field(default_factory=list)
    relations: List[Relation] = field(default_factory=list)


@dataclass
class ChunkRef:
    """KGStore 摄入时需要的 chunk 信息（避免直接依赖 rag 包形成循环）"""
    id: int = 0          # 文档内 chunk idx（0-based）
    pg_id: int = 0       # PostgreSQL 自增 ID，KG 节点上同时持久化以支持 RAG RRF 融合
    content: str = ""
