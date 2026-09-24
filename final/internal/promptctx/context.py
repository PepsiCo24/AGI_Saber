"""promptctx.context — RuntimeContext 容器与渲染函数。"""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import List, Optional

from .schema import RuntimeContextSchema
from .slot import (
    FilledSlot,
    SlotConstraints,
    SlotKind,
    SlotPlanner,
    SlotProfile,
    SlotRecall,
    SlotTaskMem,
    SlotToolState,
)


@dataclass
class RuntimeContext:
    """一次装配的全部结果，可通过 render 得到 System Prompt 前缀。"""

    schema: RuntimeContextSchema
    filled: List[FilledSlot] = field(default_factory=list)
    trace: List[str] = field(default_factory=list)  # debug：装配过程中的决策记录

    def slot_by_kind(self, kind: SlotKind) -> Optional[FilledSlot]:
        """取出特定槽位（不存在返回 None）。"""
        for fs in self.filled:
            if fs.kind == kind:
                return fs
        return None

    def render(self) -> str:
        """将所有非空槽位按 Schema 顺序渲染为 zh-CN 提示前缀。"""
        if not self.filled:
            return ""
        sections: List[str] = []
        for fs in self.filled:
            if fs.skipped or len(fs.items) == 0:
                continue
            rendered = _render_slot(fs)
            if rendered:
                sections.append(rendered)
        return "\n\n".join(sections)


def _render_slot(fs: FilledSlot) -> str:
    """按 SlotKind 选模板渲染单个槽位。"""
    title = _slot_title(fs.kind)
    lines: List[str] = []
    for item in fs.items:
        if not item.text or not item.text.strip():
            continue
        lines.append("- " + item.text)
    if len(lines) == 0:
        return ""
    return f"【{title}】\n" + "\n".join(lines)


def _slot_title(kind: SlotKind) -> str:
    """返回每类槽位的中文标题。"""
    if kind == SlotProfile:
        return "用户画像"
    if kind == SlotPlanner:
        return "任务规划"
    if kind == SlotTaskMem:
        return "任务记忆"
    if kind == SlotToolState:
        return "可用工具"
    if kind == SlotConstraints:
        return "硬性约束"
    if kind == SlotRecall:
        return "相关回忆"
    return str(kind)
