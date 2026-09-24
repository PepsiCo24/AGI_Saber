"""promptctx.source_profile — 装填 Long-term Profile 槽位。

数据来源：用户偏好仓库（高优先级，稳定身份信息）+ LTM 中
category=identity|preference 的条目。通过 typing.Protocol 声明所需依赖，
便于测试时注入 mock。
"""
from __future__ import annotations

from typing import Dict, List, Optional, Protocol, runtime_checkable

from .slot import ContextItem, Slot, SlotKind, SlotProfile
from .source import ContextSource, Query


@runtime_checkable
class PreferenceSnapshotProvider(Protocol):
    """偏好仓库需暴露 snapshot()，返回偏好键值对快照。"""

    def snapshot(self) -> Dict[str, str]: ...


@runtime_checkable
class LongTermCategoryFilter(Protocol):
    """LTM 需暴露 filter_by_category()，按类别返回 LTM Item。

    返回的对象需具备 content / importance / category 三个属性。
    """

    def filter_by_category(self, categories: List[str], limit: int) -> List[object]: ...


class ProfileSource(ContextSource):
    """ProfileSource 装填 Long-term Profile 槽位。"""

    def __init__(
        self,
        pref: Optional[PreferenceSnapshotProvider] = None,
        ltm: Optional[LongTermCategoryFilter] = None,
    ) -> None:
        self.pref = pref
        self.ltm = ltm

    def id(self) -> str:
        return "profile"

    def supports(self, kind: SlotKind) -> bool:
        return kind == SlotProfile

    def fetch(self, slot: Slot, q: Query) -> List[ContextItem]:
        items: List[ContextItem] = []

        if self.pref is not None:
            try:
                # 拿一次性快照，避免遍历期间被并发写入打断
                data = self.pref.snapshot() or {}
            except Exception:
                data = {}
            if data:
                keys = sorted(data.keys())  # 稳定顺序，避免每轮 prompt 抖动
                for k in keys:
                    items.append(
                        ContextItem(
                            text=f"{k}: {data[k]}",
                            score=1.0,  # 偏好是确定性事实
                            source=self.id(),
                        )
                    )

        if self.ltm is not None and slot.filter.categories:
            limit = slot.filter.top_k
            if limit <= 0:
                limit = 10
            try:
                ltm_items = self.ltm.filter_by_category(slot.filter.categories, limit) or []
            except Exception:
                ltm_items = []
            for ltm_item in ltm_items:
                content = getattr(ltm_item, "content", "")
                importance = float(getattr(ltm_item, "importance", 0.0) or 0.0)
                category = getattr(ltm_item, "category", "")
                items.append(
                    ContextItem(
                        text=content,
                        score=importance,
                        source=self.id(),
                        meta={"category": category} if category else {},
                    )
                )

        return items
