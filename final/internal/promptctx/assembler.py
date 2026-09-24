"""promptctx.assembler — 主装配逻辑：SourceRegistry + ContextAssembler。

根据 Mode 选 Schema，并发调各 source 填充槽位，最后做全局预算裁剪。
"""
from __future__ import annotations

import logging
import threading
from concurrent.futures import ThreadPoolExecutor
from typing import Dict, List, Optional

from .context import RuntimeContext
from .schema import (
    DEFAULT_GLOBAL_TOKEN_BUDGET,
    RuntimeContextSchema,
    default_schemas,
    slot_priority,
)
from .slot import (
    ContextItem,
    FilledSlot,
    Slot,
    SlotConstraints,
    SlotKind,
    SlotPlanner,
    SlotProfile,
    SlotRecall,
    SlotTaskMem,
    SlotToolState,
)
from .source import ContextSource, Query

logger = logging.getLogger(__name__)


_ALL_SLOT_KINDS: List[SlotKind] = [
    SlotProfile,
    SlotPlanner,
    SlotTaskMem,
    SlotToolState,
    SlotConstraints,
    SlotRecall,
]


class SourceRegistry:
    """持有按 SlotKind 分组的所有 ContextSource 注册。"""

    def __init__(self) -> None:
        self._lock = threading.RLock()
        self._sources: Dict[SlotKind, List[ContextSource]] = {}

    def register(self, source: ContextSource) -> None:
        """将 source 注册到它声明支持的所有 SlotKind。"""
        with self._lock:
            for kind in _ALL_SLOT_KINDS:
                if source.supports(kind):
                    self._sources.setdefault(kind, []).append(source)

    def for_kind(self, kind: SlotKind) -> List[ContextSource]:
        """返回支持指定 SlotKind 的全部 source（只读快照）。"""
        with self._lock:
            return list(self._sources.get(kind, []))


class ContextAssembler:
    """装配入口：根据 Mode 选 Schema，并发调各 source 填充槽位。"""

    def __init__(
        self,
        schemas: Optional[Dict[str, RuntimeContextSchema]] = None,
        registry: Optional[SourceRegistry] = None,
        global_limit: int = DEFAULT_GLOBAL_TOKEN_BUDGET,
    ) -> None:
        if not schemas:
            schemas = default_schemas()
        self.schemas = schemas
        self.registry = registry if registry is not None else SourceRegistry()
        self.global_limit = global_limit

    def assemble(self, q: Query) -> RuntimeContext:
        """构建当次推理的 RuntimeContext。"""
        schema = self.schemas.get(q.mode)
        if schema is None:
            schema = self.schemas.get("chat")
        if schema is None:
            # 极端兜底：没有任何 schema
            return RuntimeContext(schema=RuntimeContextSchema(mode=q.mode))

        rc = RuntimeContext(
            schema=schema,
            filled=[FilledSlot(kind=s.kind) for s in schema.slots],
        )

        # 并发调各槽位对应的 source
        slots = list(schema.slots)
        if slots:
            with ThreadPoolExecutor(max_workers=max(1, len(slots))) as pool:
                futures = {
                    pool.submit(self._fill_slot, slot, q): idx
                    for idx, slot in enumerate(slots)
                }
                for fut in futures:
                    idx = futures[fut]
                    try:
                        rc.filled[idx] = fut.result()
                    except Exception as e:
                        # 失败降级：返回空槽位
                        logger.warning("promptctx fill_slot failed: %s", e)
                        rc.filled[idx] = FilledSlot(
                            kind=slots[idx].kind,
                            skipped=not slots[idx].required,
                            reason=f"source error: {e}",
                        )

        # 全局预算裁剪（高优先级槽位优先保留）
        self._apply_global_budget(rc)

        return rc

    def _fill_slot(self, slot: Slot, q: Query) -> FilledSlot:
        """调用注册到该 SlotKind 的 source，并做单槽位 budget 裁剪。"""
        sources = self.registry.for_kind(slot.kind)
        if not sources:
            return FilledSlot(
                kind=slot.kind,
                skipped=slot.required,
                reason="no source registered",
            )

        all_items: List[ContextItem] = []
        for src in sources:
            try:
                items = src.fetch(slot, q) or []
            except Exception as e:
                # 失败降级：跳过这个 source
                logger.warning("promptctx source %s fetch failed: %s", src.id(), e)
                break
            all_items.extend(items)

        if not all_items:
            return FilledSlot(
                kind=slot.kind,
                skipped=not slot.required,
                reason="source returned empty",
            )

        # 单槽位 token budget 裁剪（按字符数近似）
        if slot.filter.token_budget > 0:
            all_items = _trim_by_budget(all_items, slot.filter.token_budget)

        return FilledSlot(kind=slot.kind, items=all_items)

    def _apply_global_budget(self, rc: RuntimeContext) -> None:
        """从低优先级槽位开始裁剪，直到总字符数在 global_limit 以内。"""
        total = 0
        for fs in rc.filled:
            for item in fs.items:
                total += len(item.text)
        if total <= self.global_limit:
            return

        # 按优先级从低到高排（SlotRecall 最低，SlotConstraints 最高），逐步裁剪
        order = list(range(len(rc.filled)))
        order.sort(key=lambda i: slot_priority(rc.filled[i].kind), reverse=True)

        for idx in order:
            if total <= self.global_limit:
                break
            fs = rc.filled[idx]
            while fs.items and total > self.global_limit:
                last = fs.items[-1]
                total -= len(last.text)
                fs.items = fs.items[:-1]
            if not fs.items:
                fs.skipped = not rc.schema.slots[idx].required
                fs.reason = "global budget exceeded"


def _trim_by_budget(items: List[ContextItem], budget: int) -> List[ContextItem]:
    """按字符数裁剪 ContextItem 列表，直到总长在 budget 以内。"""
    total = 0
    for i, item in enumerate(items):
        total += len(item.text)
        if total > budget:
            return items[:i]
    return items
