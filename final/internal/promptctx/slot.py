"""promptctx.slot — 认知槽位类别、过滤器与装填结果数据结构。

每轮推理前，根据当前 Mode 选取一个 RuntimeContextSchema（认知槽位编排），
并通过注册到 ContextAssembler 的 ContextSource 填充各槽位：

    Long-term Profile  — 用户稳定身份与偏好
    Planner State      — 当前任务规划/阶段
    Task Memory        — 当前任务的步骤观察缓存
    Tool State         — 可用工具与近期调用结果
    Constraints        — 沙箱政策、硬性约束
    Recall Memory      — 受 SlotFilter 约束的语义召回（兜底）
"""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import Dict, List


# SlotKind 是认知槽位的类别标识
SlotProfile = "profile"
SlotPlanner = "planner"
SlotTaskMem = "task_memory"
SlotToolState = "tool_state"
SlotConstraints = "constraints"
SlotRecall = "recall_memory"

SlotKind = str  # 类型别名，便于阅读


@dataclass
class SlotFilter:
    """SlotFilter 描述 Source 在填充槽位时的过滤约束。"""

    categories: List[str] = field(default_factory=list)   # 命中其一即可，空表示不限
    require_tags: List[str] = field(default_factory=list)  # 必须全部包含
    min_score: float = 0.0    # 召回综合分阈值
    top_k: int = 0            # 单槽位最多返回项数（0 表示不截断）
    max_age_hours: int = 0    # 最大年龄（小时），0 表示不限
    token_budget: int = 0     # 单槽位字符预算（粗略以字符数近似 token）


@dataclass
class Slot:
    """Schema 中的单个认知槽位定义。"""

    kind: SlotKind
    required: bool = False                          # Required 槽位即使为空也会渲染占位
    filter: SlotFilter = field(default_factory=SlotFilter)
    template: str = ""                              # render 时模板键，留空时使用 kind


@dataclass
class ContextItem:
    """单条已装入槽位的内容。"""

    text: str
    score: float = 0.0
    source: str = ""                                 # 调试用：标记来自哪个 ContextSource
    meta: Dict[str, str] = field(default_factory=dict)  # 调试用元数据


@dataclass
class FilledSlot:
    """装配后的单个槽位结果。"""

    kind: SlotKind
    items: List[ContextItem] = field(default_factory=list)
    skipped: bool = False    # 因预算或无数据被跳过
    reason: str = ""         # 跳过原因（debug）
