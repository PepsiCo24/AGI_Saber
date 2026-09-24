"""promptctx.source_taskmem — 装填 Task Memory 槽位。

提供 in-memory ring buffer，agent 在每步工具执行后 push；TaskMemSource 从中读取。
"""
from __future__ import annotations

import threading
import time
from dataclasses import dataclass, field
from typing import List, Optional

from .slot import ContextItem, Slot, SlotKind, SlotTaskMem
from .source import ContextSource, Query


@dataclass
class StepObservation:
    """任务执行过程中单步工具观察的快照。"""

    step_id: int = 0
    tool_name: str = ""
    result: str = ""
    error: str = ""
    success: bool = False
    created_at: float = 0.0  # epoch 秒，0 表示未设置


class TaskMemBuffer:
    """当前任务的步骤观察缓冲区（in-memory ring buffer）。

    agent 在每步工具执行后 push；TaskMemSource 从中读取。
    """

    def __init__(self, max_size: int = 20) -> None:
        if max_size <= 0:
            max_size = 20
        self.max = max_size
        self._buf: List[StepObservation] = []
        self._lock = threading.RLock()

    def push(self, obs: StepObservation) -> None:
        """追加一条步骤观察（超出 max 时丢弃最早条目）。"""
        with self._lock:
            if not obs.created_at:
                obs.created_at = time.time()
            self._buf.append(obs)
            if len(self._buf) > self.max:
                self._buf = self._buf[len(self._buf) - self.max:]

    def reset(self) -> None:
        """清空缓冲区（新任务开始时调用）。"""
        with self._lock:
            self._buf = []

    def snapshot(self) -> List[StepObservation]:
        """返回当前全部观察的只读副本。"""
        with self._lock:
            return list(self._buf)


class TaskMemSource(ContextSource):
    """装填 Task Memory 槽位。"""

    def __init__(self, buf: Optional[TaskMemBuffer] = None) -> None:
        self.buf = buf

    def id(self) -> str:
        return "task_memory"

    def supports(self, kind: SlotKind) -> bool:
        return kind == SlotTaskMem

    def fetch(self, slot: Slot, q: Query) -> List[ContextItem]:
        if self.buf is None:
            return []
        try:
            obs = self.buf.snapshot()
        except Exception:
            obs = []
        if not obs:
            return []

        top_k = slot.filter.top_k
        if top_k > 0 and len(obs) > top_k:
            obs = obs[len(obs) - top_k:]

        items: List[ContextItem] = []
        for o in obs:
            text = f"步骤{o.step_id} [{o.tool_name}]"
            if o.success:
                r = o.result
                if len(r) > 200:
                    r = r[:200] + "…"
                text += "→" + r
            else:
                text += " 失败: " + o.error
            items.append(
                ContextItem(
                    text=text,
                    source=self.id(),
                    meta={"tool": o.tool_name},
                )
            )
        return items
