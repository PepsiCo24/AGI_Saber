import json
import logging
import re
from typing import Callable, List, Optional

logger = logging.getLogger(__name__)

GenerateFn = Callable[[str, str], str]


class LLMReranker:
    """用一次 LLM listwise 调用对候选 chunk 精排，失败时回退原顺序。"""

    def __init__(self, generate_fn: Optional[GenerateFn], preview_len: int = 200):
        self.generate_fn = generate_fn
        self.preview_len = preview_len if preview_len > 0 else 200

    def rerank(self, query: str, results: List, top_k: int) -> List:
        if not results:
            return []
        if self.generate_fn is None or len(results) == 1:
            return _truncate(results, top_k)

        try:
            raw = self.generate_fn(self._system_prompt(), self._user_msg(query, results))
            scores = _parse_scores(raw)
        except Exception as e:
            logger.warning("⚠️  Rerank 失败，回退 RRF 顺序: %s", e)
            return _truncate(results, top_k)
        if not scores:
            return _truncate(results, top_k)

        score_map = {idx: score for idx, score in scores if 0 <= idx < len(results)}
        if len(score_map) != len(results):
            logger.warning(
                "⚠️  Rerank scores 数量(%d) != 候选数量(%d)，缺失项补 0、越界项截断",
                len(score_map), len(results),
            )
            for i in range(len(results)):
                score_map.setdefault(i, 0.0)
        ordered = []
        for idx, result in enumerate(results):
            llm_score = score_map.get(idx, 0.0)
            ordered.append((llm_score, getattr(result, "score", 0.0), idx, result))
        ordered.sort(key=lambda item: (item[0], item[1], -item[2]), reverse=True)

        out = []
        for llm_score, _rrf_score, _idx, result in ordered:
            if llm_score >= 0:
                result.score = llm_score / 10.0
            result.source = f"{getattr(result, 'source', '')}+rerank"
            out.append(result)
        return _truncate(out, top_k)

    def _system_prompt(self) -> str:
        return (
            "你是检索系统的精排器。给定用户问题和若干候选段落（每条带编号 idx），"
            "判断每条段落对回答该问题的**相关性 + 信息密度**，给 0~10 的整数分。\n\n"
            "打分准则：\n"
            "- 10：直接回答了问题\n"
            "- 7~9：包含明确相关事实 / 线索\n"
            "- 4~6：弱相关 / 部分相关\n"
            "- 1~3：仅出现共现关键词，不能用来回答\n"
            "- 0：无关 / 噪声\n\n"
            "输出**严格 JSON**，不要任何说明文字、不要 markdown 代码块：\n"
            "{\"scores\": [{\"idx\": 0, \"score\": 9}, {\"idx\": 1, \"score\": 3}]}\n\n"
            "约束：\n"
            "- scores 数量严格等于候选数量\n"
            "- score 是 0~10 的整数\n"
            "- 不依赖你自己的知识，只看给出的段落"
        )

    def _user_msg(self, query: str, results: List) -> str:
        lines = [f"用户问题：{query}", "", "候选段落："]
        for idx, result in enumerate(results):
            content = _result_content(result)
            if len(content) > self.preview_len:
                content = content[:self.preview_len] + "..."
            lines.append(f"[{idx}] {content}")
        return "\n".join(lines)


def _result_content(result) -> str:
    if hasattr(result, "content"):
        return str(result.content)
    chunk = getattr(result, "chunk", None)
    if chunk is not None and hasattr(chunk, "content"):
        return str(chunk.content)
    return ""


def _parse_scores(raw: str) -> List[tuple]:
    raw = _strip_json_fence(raw)
    data = json.loads(raw)
    items = data.get("scores", []) if isinstance(data, dict) else []
    scores = []
    for item in items:
        try:
            scores.append((int(item.get("idx")), float(item.get("score"))))
        except Exception:
            continue
    return scores


def _strip_json_fence(raw: str) -> str:
    raw = (raw or "").strip()
    raw = re.sub(r"^```json\s*", "", raw)
    raw = re.sub(r"^```\s*", "", raw)
    raw = re.sub(r"\s*```$", "", raw)
    return raw.strip()


def _truncate(results: List, top_k: int) -> List:
    if top_k > 0 and len(results) > top_k:
        return results[:top_k]
    return results
