"""promptctx.source — Query 与 ContextSource 抽象基类。"""
from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from typing import List

from .slot import ContextItem, Slot, SlotKind


@dataclass
class Query:
    """装配一次上下文时的输入快照。"""

    text: str = ""                                  # 用户当前输入
    embedding: List[float] = field(default_factory=list)  # 已计算的 query embedding（可为空）
    task_id: str = ""                               # 当前任务 ID（用于 Task Memory）
    mode: str = ""                                  # chat / tool / react / rag


class ContextSource(ABC):
    """某类认知槽位的数据提供者。

    一个 source 可声明支持多个 SlotKind（例如 Profile source 同时填 Profile/Recall 都行）。
    """

    @abstractmethod
    def id(self) -> str:
        """返回 source 标识。"""

    @abstractmethod
    def supports(self, kind: SlotKind) -> bool:
        """判断是否支持指定 SlotKind。"""

    @abstractmethod
    def fetch(self, slot: Slot, q: Query) -> List[ContextItem]:
        """在不超过 slot.filter.token_budget 的前提下，返回适合该槽位的 ContextItem。

        实现需自己做 TopK 截断与 budget 裁剪。失败时降级返回空列表，不抛异常。
        """
