# extractor — 通过 LLM 从文本中抽取实体和关系
import json
import logging
from typing import Callable, Optional

from .types import (
    Entity,
    ExtractResult,
    Relation,
    ENTITY_UNKNOWN,
    ENTITY_PERSON,
    ENTITY_ORG,
    ENTITY_LOCATION,
    ENTITY_CONCEPT,
    ENTITY_EVENT,
    ENTITY_PRODUCT,
)

logger = logging.getLogger(__name__)


# LLM 回调签名：(system_prompt, user_msg) -> str
LLMFn = Callable[[str, str], str]


EXTRACT_SYSTEM_PROMPT = """你是一个信息抽取专家。从给定文本中抽取命名实体和实体间关系。

实体类型（type 字段只能用以下值）：
- Person（人物）
- Organization（组织/公司/机构）
- Location（地点/地区）
- Concept（概念/技术/思想）
- Event（事件）
- Product（产品/工具）
- Unknown（其他）

关系类型（rel_type 字段只能用以下值）：
- RELATES_TO（相关）
- PART_OF（属于/是...的一部分）
- CAUSES（导致/引发）
- DESCRIBES（描述/介绍）
- MENTIONS（提及）
- WORKS_FOR（工作于）
- LOCATED_IN（位于）

输出格式（只输出 JSON，不加任何说明）：
{
  "entities": [{"name":"实体名","type":"类型"}],
  "relations": [{"from":"实体A","to":"实体B","rel_type":"关系类型"}]
}

如果文本中没有可抽取的实体，输出 {"entities":[],"relations":[]}"""


_VALID_ENTITY_TYPES = {
    ENTITY_PERSON,
    ENTITY_ORG,
    ENTITY_LOCATION,
    ENTITY_CONCEPT,
    ENTITY_EVENT,
    ENTITY_PRODUCT,
    ENTITY_UNKNOWN,
}

_VALID_REL_TYPES = {
    "RELATES_TO",
    "PART_OF",
    "CAUSES",
    "DESCRIBES",
    "MENTIONS",
    "WORKS_FOR",
    "LOCATED_IN",
}


class Extractor:
    """通过注入的 LLM 回调从文本中抽取实体和关系"""

    def __init__(self, llm_fn: Optional[LLMFn]):
        self.llm_fn = llm_fn

    def extract(self, text: str) -> ExtractResult:
        """从单段文本中抽取实体和关系；LLM 不可用或解析失败时返回空结果（不抛异常）。"""
        if self.llm_fn is None or not text.strip():
            return ExtractResult()

        try:
            raw = self.llm_fn(EXTRACT_SYSTEM_PROMPT, "文本：\n" + text)
        except Exception as e:
            logger.warning("⚠️  实体关系抽取 LLM 调用失败: %s", e)
            return ExtractResult()

        raw = raw.strip()
        if raw.startswith("```json"):
            raw = raw[len("```json"):]
        elif raw.startswith("```"):
            raw = raw[len("```"):]
        if raw.endswith("```"):
            raw = raw[:-3]
        raw = raw.strip()

        try:
            parsed = json.loads(raw)
        except Exception as e:
            logger.warning("⚠️  实体关系抽取解析失败: %s（原始输出: %.100s）", e, raw)
            return ExtractResult()

        if not isinstance(parsed, dict):
            return ExtractResult()

        # 清洗：去除空名称，规范 type
        cleaned = ExtractResult()
        seen: set = set()

        for ent_raw in parsed.get("entities") or []:
            if not isinstance(ent_raw, dict):
                continue
            name = str(ent_raw.get("name") or "").strip()
            ent_type = ent_raw.get("type") or ENTITY_UNKNOWN
            if not name or name in seen:
                continue
            if ent_type not in _VALID_ENTITY_TYPES:
                ent_type = ENTITY_UNKNOWN
            cleaned.entities.append(Entity(name=name, type=ent_type))
            seen.add(name)

        for rel_raw in parsed.get("relations") or []:
            if not isinstance(rel_raw, dict):
                continue
            from_name = str(rel_raw.get("from") or "").strip()
            to_name = str(rel_raw.get("to") or "").strip()
            rel_type = str(rel_raw.get("rel_type") or "")
            if not from_name or not to_name or not rel_type:
                continue
            if rel_type not in _VALID_REL_TYPES:
                rel_type = "RELATES_TO"
            cleaned.relations.append(Relation(
                from_name=from_name,
                to_name=to_name,
                rel_type=rel_type,
            ))

        return cleaned
