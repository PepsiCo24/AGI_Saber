import json
import logging
import re
from dataclasses import dataclass
from typing import Callable, List, Optional

logger = logging.getLogger(__name__)

GenerateFn = Callable[[str, str], str]


@dataclass
class HistoryMessage:
    role: str
    content: str


class LLMRewriter:
    """用 LLM 做 history-aware multi-query 改写，失败时回退原 query。"""

    def __init__(self, generate_fn: Optional[GenerateFn], num_queries: int = 3):
        self.generate_fn = generate_fn
        self.num_queries = num_queries if num_queries > 0 else 3

    def rewrite(self, query: str, history: List[HistoryMessage]) -> List[str]:
        query = (query or "").strip()
        if not query:
            return []
        if self.generate_fn is None or self.num_queries <= 1:
            return [query]

        user_msg = self._build_user_msg(query, history)
        system_prompt = (
            "你是检索系统的查询改写助手。给定用户当前问题和最近对话历史，你需要：\n"
            "1) 先把当前问题改写成一句**自包含的独立查询**（消除指代、补全省略，"
            "只用 query 本身就能让人看懂）。\n"
            "2) 再生成若干条**等价但措辞不同**的查询变体（同义词替换、抽象/具体切换、不同语序）。\n\n"
            "输出**严格 JSON**，不要任何说明文字、不要 markdown 代码块：\n"
            "{\"queries\": [\"独立查询\", \"变体1\", \"变体2\"]}\n\n"
            "约束：\n"
            f"- 总条数严格等于 {self.num_queries}\n"
            "- 每条不超过 50 字\n"
            "- 第一条必须可独立检索（不依赖历史）\n"
            "- 不要编造历史中未出现的人名、机构、产品名等实体\n"
            "- 只输出 JSON 数组结构，禁止任何前后说明文字"
        )
        try:
            raw = self.generate_fn(system_prompt, user_msg)
            queries = _parse_queries(raw)
        except Exception as e:
            logger.warning("⚠️  Query rewrite 失败，回退原查询: %s", e)
            return [query]
        if not queries:
            return [query]
        return _dedup_keep_order(queries + [query])[:self.num_queries]

    def _build_user_msg(self, query: str, history: List[HistoryMessage]) -> str:
        lines: List[str] = ["最近对话历史："]
        if history:
            recent = history[-6:]
            for msg in recent:
                role = msg.role or "user"
                content = (msg.content or "").strip()
                if len(content) > 200:
                    content = content[:200] + "..."
                lines.append(f"[{role}] {content}")
        else:
            lines.append("（无历史，直接改写当前问题）")
        lines.append("")
        lines.append(f"当前问题：{query}")
        return "\n".join(lines)


def _parse_queries(raw: str) -> List[str]:
    raw = _strip_json_fence(raw)
    data = json.loads(raw)
    queries = data.get("queries", []) if isinstance(data, dict) else []
    return [str(q).strip() for q in queries if str(q).strip()]


def _strip_json_fence(raw: str) -> str:
    raw = (raw or "").strip()
    raw = re.sub(r"^```json\s*", "", raw)
    raw = re.sub(r"^```\s*", "", raw)
    raw = re.sub(r"\s*```$", "", raw)
    return raw.strip()


def _dedup_keep_order(values: List[str]) -> List[str]:
    seen = set()
    out: List[str] = []
    for value in values:
        key = value.strip().lower()
        if not key or key in seen:
            continue
        seen.add(key)
        out.append(value)
    return out
