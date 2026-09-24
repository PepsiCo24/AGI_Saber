"""promptctx.source_recall — 装填 Recall 槽位（兜底语义召回）。

通过 typing.Protocol 抽象 LongTerm / GraphMemory 共有的过滤召回能力。
"""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import List, Optional, Protocol, runtime_checkable

from .slot import ContextItem, Slot, SlotKind, SlotRecall
from .source import ContextSource, Query


@dataclass
class RecallFilter:
    """传给 Recaller 的召回过滤参数（与 memory.RecallFilter 字段对齐）。"""

    categories: List[str] = field(default_factory=list)
    require_tags: List[str] = field(default_factory=list)
    min_score: float = 0.0
    top_k: int = 0
    max_age_hours: int = 0


@runtime_checkable
class Recaller(Protocol):
    """抽象 LongTerm / GraphMemory 共有的过滤召回能力。

    返回的 Item 需具备 content / importance / score / category / slot_hint 属性。
    """

    def recall_by_filter(
        self,
        query: str,
        query_embedding: List[float],
        filter: RecallFilter,
    ) -> List[object]: ...


class RecallSource(ContextSource):
    """装填 Recall 槽位的语义召回 source。"""

    def __init__(self, recaller: Optional[Recaller] = None) -> None:
        self.recaller = recaller

    def id(self) -> str:
        return "recall"

    def supports(self, kind: SlotKind) -> bool:
        return kind == SlotRecall

    def fetch(self, slot: Slot, q: Query) -> List[ContextItem]:
        if self.recaller is None:
            return []
        rfilter = RecallFilter(
            categories=list(slot.filter.categories),
            require_tags=list(slot.filter.require_tags),
            min_score=slot.filter.min_score,
            top_k=slot.filter.top_k,
            max_age_hours=slot.filter.max_age_hours,
        )
        try:
            hits = self.recaller.recall_by_filter(q.text, q.embedding, rfilter) or []
        except Exception:
            hits = []
        if not hits:
            return []
        items: List[ContextItem] = []
        for h in hits:
            content = getattr(h, "content", "")
            importance = float(getattr(h, "importance", 0.0) or 0.0)
            score = float(getattr(h, "score", 0.0) or 0.0)
            category = getattr(h, "category", "") or ""
            slot_hint = getattr(h, "slot_hint", "") or ""
            meta: dict = {}
            if category:
                meta["category"] = category
            if slot_hint:
                meta["slot_hint"] = slot_hint
            items.append(
                ContextItem(
                    text=f"{content}（重要性={importance:.2f}, 综合分={score:.2f}）",
                    score=score,
                    source=self.id(),
                    meta=meta,
                )
            )
        return items
