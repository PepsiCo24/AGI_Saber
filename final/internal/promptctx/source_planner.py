"""promptctx.source_planner — 装填 Planner State 槽位。"""
from __future__ import annotations

from dataclasses import dataclass
from typing import Callable, List, Optional

from .slot import ContextItem, Slot, SlotKind, SlotPlanner
from .source import ContextSource, Query


@dataclass
class PlannerSnapshot:
    """Planner 当前状态的只读视图。

    agent 包通过 PlannerProvider 暴露 TaskState 的快照，避免 runtime 反向依赖 agent。
    """

    task_id: str = ""
    query: str = ""
    status: str = ""           # running / completed / interrupted
    phase: str = ""            # planning / executing / generating / done / interrupted
    total_steps: int = 0
    current_step: int = 0
    interrupted_at: int = 0
    next_step_name: str = ""   # 即将执行的步骤描述（若有）
    next_step_tool: str = ""


# PlannerProvider 由 agent 实现，返回当前任务的 Planner 状态。
# 没有正在执行的任务时返回 None。
PlannerProvider = Callable[[], Optional[PlannerSnapshot]]


class PlannerSource(ContextSource):
    """装填 Planner State 槽位。"""

    def __init__(self, get: Optional[PlannerProvider] = None) -> None:
        self.get = get

    def id(self) -> str:
        return "planner"

    def supports(self, kind: SlotKind) -> bool:
        return kind == SlotPlanner

    def fetch(self, slot: Slot, q: Query) -> List[ContextItem]:
        if self.get is None:
            return []
        try:
            snap = self.get()
        except Exception:
            snap = None
        if snap is None:
            return []
        items: List[ContextItem] = []
        items.append(
            ContextItem(
                text=f"任务 {snap.task_id} 状态={snap.status} 阶段={snap.phase}",
                source=self.id(),
            )
        )
        if snap.total_steps > 0:
            items.append(
                ContextItem(
                    text=f"进度：第 {snap.current_step + 1}/{snap.total_steps} 步",
                    source=self.id(),
                )
            )
        if snap.next_step_name:
            items.append(
                ContextItem(
                    text=f"下一步：{snap.next_step_name}（工具={snap.next_step_tool}）",
                    source=self.id(),
                )
            )
        if snap.status == "interrupted" and snap.interrupted_at > 0:
            items.append(
                ContextItem(
                    text=f"上次在第 {snap.interrupted_at + 1} 步被中断，可从此处恢复",
                    source=self.id(),
                )
            )
        return items
