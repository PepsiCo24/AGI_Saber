"""promptctx.source_constraints — 装填 Constraints 槽位。

数据来源：sandbox 的静态安全政策（启动时一次性快照，运行期不变）。
本模块不依赖具体的 sandbox 实现，通过 Policy 数据类承载所需字段，
并约定 RISK_BLOCK / RISK_WARN 两个等级常量。
"""
from __future__ import annotations

from dataclasses import dataclass
from typing import List

from .slot import ContextItem, Slot, SlotKind, SlotConstraints
from .source import ContextSource, Query


# 风险级别常量（与 Go 端 sandbox.RiskBlock / RiskWarn 对齐）
RISK_BLOCK = "block"
RISK_WARN = "warn"


@dataclass
class Policy:
    """沙箱静态安全政策的最小字段集。"""

    pattern: str = ""
    reason: str = ""
    level: str = RISK_WARN  # "block" 或 "warn"


class ConstraintsSource(ContextSource):
    """装填 Constraints 槽位。来源通常是 sandbox.policy_snapshot()。"""

    def __init__(self, policies: List[Policy]) -> None:
        # 拷贝一份避免外部修改影响 source
        self.policies: List[Policy] = list(policies) if policies else []

    def id(self) -> str:
        return "constraints"

    def supports(self, kind: SlotKind) -> bool:
        return kind == SlotConstraints

    def fetch(self, slot: Slot, q: Query) -> List[ContextItem]:
        if not self.policies:
            return []

        # 按 Level 拆分：Block 在前
        blocks: List[Policy] = []
        warns: List[Policy] = []
        for p in self.policies:
            if p.level == RISK_BLOCK:
                blocks.append(p)
            else:
                warns.append(p)

        ordered = blocks + warns
        top_k = slot.filter.top_k
        if top_k > 0 and len(ordered) > top_k:
            ordered = ordered[:top_k]

        items: List[ContextItem] = []
        for p in ordered:
            if p.level == RISK_BLOCK:
                level = "禁止"
                score = 1.0
            else:
                level = "告警"
                score = 0.5
            items.append(
                ContextItem(
                    text=f"[{level}] {p.reason}",
                    score=score,
                    source=self.id(),
                    meta={"level": str(p.level), "pattern": p.pattern},
                )
            )
        return items
