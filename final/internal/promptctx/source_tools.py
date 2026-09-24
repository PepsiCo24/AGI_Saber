"""promptctx.source_tools — 装填 Tool State 槽位。"""
from __future__ import annotations

import threading
import time
from dataclasses import dataclass
from typing import Callable, Dict, List, Optional, Protocol, runtime_checkable

from .slot import ContextItem, Slot, SlotKind, SlotToolState
from .source import ContextSource, Query


@dataclass
class ToolCallTrace:
    """单次工具调用的简要记录（用于 Tool State 槽位）。"""

    tool_name: str = ""
    success: bool = False
    summary: str = ""        # 截断后的结果或错误摘要
    created_at: float = 0.0  # epoch 秒，0 表示未设置


class ToolStateTracker:
    """持有最近 N 次工具调用的环形缓冲，供 ToolStateSource 读取。"""

    def __init__(self, max_size: int = 10) -> None:
        if max_size <= 0:
            max_size = 10
        self.max = max_size
        self._buf: List[ToolCallTrace] = []
        self._lock = threading.RLock()

    def record(self, trace: ToolCallTrace) -> None:
        """追加一次工具调用记录。"""
        with self._lock:
            if not trace.created_at:
                trace.created_at = time.time()
            if len(trace.summary) > 120:
                trace.summary = trace.summary[:120] + "…"
            self._buf.append(trace)
            if len(self._buf) > self.max:
                self._buf = self._buf[len(self._buf) - self.max:]

    def snapshot(self) -> List[ToolCallTrace]:
        """返回当前调用历史的只读副本。"""
        with self._lock:
            return list(self._buf)


@runtime_checkable
class ToolLike(Protocol):
    """ToolStateSource 期望工具对象提供的最小接口。

    需具备 description 属性与可迭代的 parameters；每个 parameter 含 name/required。
    实测会兼容 internal.tools.tools.Tool（其 params 是 List[Dict[str, str]]，不带 required，
    在缺失时默认视为非必填）。
    """

    description: str


# ToolRegistryProvider 由 agent 实现，返回当前可用工具映射
ToolRegistryProvider = Callable[[], Dict[str, object]]


def _required_params(tool: object) -> List[str]:
    """从工具对象提取必填参数名列表，兼容 dataclass attr/dict 两种风格。"""
    raw_params = getattr(tool, "parameters", None)
    if raw_params is None:
        raw_params = getattr(tool, "params", None)
    if not raw_params:
        return []
    required: List[str] = []
    for p in raw_params:
        # 兼容对象 (Parameter dataclass) 与 dict
        if isinstance(p, dict):
            if p.get("required"):
                name = p.get("name") or ""
                if name:
                    required.append(name)
        else:
            if getattr(p, "required", False):
                name = getattr(p, "name", "") or ""
                if name:
                    required.append(name)
    return required


class ToolStateSource(ContextSource):
    """装填 Tool State 槽位。"""

    def __init__(
        self,
        registry: Optional[ToolRegistryProvider] = None,
        tracker: Optional[ToolStateTracker] = None,
    ) -> None:
        self.registry = registry
        self.tracker = tracker

    def id(self) -> str:
        return "tool_state"

    def supports(self, kind: SlotKind) -> bool:
        return kind == SlotToolState

    def fetch(self, slot: Slot, q: Query) -> List[ContextItem]:
        items: List[ContextItem] = []

        if self.registry is not None:
            try:
                tool_map = self.registry() or {}
            except Exception:
                tool_map = {}
            names = sorted(tool_map.keys())
            for name in names:
                t = tool_map[name]
                required = _required_params(t)
                param_hint = ""
                if required:
                    param_hint = "（必填 " + ", ".join(required) + "）"
                description = getattr(t, "description", "") or ""
                items.append(
                    ContextItem(
                        text=f"{name} — {description}{param_hint}",
                        source=self.id(),
                        meta={"tool": name},
                    )
                )

        if self.tracker is not None:
            try:
                traces = self.tracker.snapshot()
            except Exception:
                traces = []
            top_k = slot.filter.top_k
            if top_k > 0 and len(traces) > top_k:
                traces = traces[len(traces) - top_k:]
            for tr in traces:
                status = "成功" if tr.success else "失败"
                items.append(
                    ContextItem(
                        text=f"近期调用 {tr.tool_name} [{status}]: {tr.summary}",
                        source=self.id(),
                        meta={"tool": tr.tool_name, "status": status},
                    )
                )

        return items
