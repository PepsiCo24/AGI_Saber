"""promptctx — Schema-driven Runtime Context Assembly。

每轮推理前，根据当前 Mode 选取一个 RuntimeContextSchema（认知槽位编排），
并通过注册到 ContextAssembler 的 ContextSource 填充各槽位。
"""
from __future__ import annotations

from .assembler import ContextAssembler, SourceRegistry
from .context import RuntimeContext
from .schema import (
    CHAT_SCHEMA,
    DEFAULT_GLOBAL_TOKEN_BUDGET,
    RAG_SCHEMA,
    REACT_SCHEMA,
    RuntimeContextSchema,
    TOOL_SCHEMA,
    default_schemas,
    slot_priority,
)
from .slot import (
    ContextItem,
    FilledSlot,
    Slot,
    SlotConstraints,
    SlotFilter,
    SlotKind,
    SlotPlanner,
    SlotProfile,
    SlotRecall,
    SlotTaskMem,
    SlotToolState,
)
from .source import ContextSource, Query
from .source_constraints import ConstraintsSource, Policy, RISK_BLOCK, RISK_WARN
from .source_planner import PlannerProvider, PlannerSnapshot, PlannerSource
from .source_profile import (
    LongTermCategoryFilter,
    PreferenceSnapshotProvider,
    ProfileSource,
)
from .source_recall import RecallFilter, RecallSource, Recaller
from .source_taskmem import StepObservation, TaskMemBuffer, TaskMemSource
from .source_tools import (
    ToolCallTrace,
    ToolRegistryProvider,
    ToolStateSource,
    ToolStateTracker,
)

__all__ = [
    # assembler / context
    "ContextAssembler",
    "SourceRegistry",
    "RuntimeContext",
    # schema
    "RuntimeContextSchema",
    "CHAT_SCHEMA",
    "TOOL_SCHEMA",
    "REACT_SCHEMA",
    "RAG_SCHEMA",
    "DEFAULT_GLOBAL_TOKEN_BUDGET",
    "default_schemas",
    "slot_priority",
    # slot
    "Slot",
    "SlotFilter",
    "SlotKind",
    "SlotProfile",
    "SlotPlanner",
    "SlotTaskMem",
    "SlotToolState",
    "SlotConstraints",
    "SlotRecall",
    "ContextItem",
    "FilledSlot",
    # source base
    "ContextSource",
    "Query",
    # profile
    "ProfileSource",
    "PreferenceSnapshotProvider",
    "LongTermCategoryFilter",
    # recall
    "RecallSource",
    "Recaller",
    "RecallFilter",
    # task memory
    "TaskMemSource",
    "TaskMemBuffer",
    "StepObservation",
    # tools
    "ToolStateSource",
    "ToolStateTracker",
    "ToolCallTrace",
    "ToolRegistryProvider",
    # planner
    "PlannerSource",
    "PlannerSnapshot",
    "PlannerProvider",
    # constraints
    "ConstraintsSource",
    "Policy",
    "RISK_BLOCK",
    "RISK_WARN",
]
