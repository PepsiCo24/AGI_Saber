# mem_stack — 三层记忆 + 偏好的聚合容器，并提供 ConsolidationConfig 数据类。
#
# 与 main 分支 internal/application/chat/mem_stack.go 对齐：
#   - MemoryStack 把 stm（短期）/ ltm（长期）/ graph_memory（图增强）/ preference
#     四个独立组件装进一个对象，UnifiedAgent 不再铺 4 个字段。
#   - graph_memory 是延后注入的（KGStore 就绪后才创建），调用前需 nil 检查。
#
# ConsolidationConfig 与 main longterm.ConsolidationConfig 字段一一对应（6 字段），
# 额外保留 1 个兼容字段 ``source``（指向原始 APIConfig），共 7 字段，便于
# 既有 ``LongTerm.set_consolidation_config`` 的 duck-typed ``getattr`` 路径继续
# 读取 ``memory_consolidation_*`` 属性名而不必修改 LongTerm 内部实现。
from dataclasses import dataclass, field
from typing import Any, Optional


_DEFAULTS = {
    "similarity_threshold": 0.80,
    "dedup_threshold": 0.95,
    "ttl_days": 30,
    "decay_rate": 0.995,
    "min_importance": 0.3,
    "trigger_interval": 5,
}

# main → python 字段名映射，用于 __getattr__ 兼容 LongTerm 当前读取路径。
_LEGACY_ALIAS = {
    "memory_consolidation_similarity": "similarity_threshold",
    "memory_consolidation_dedup": "dedup_threshold",
    "memory_consolidation_ttl_days": "ttl_days",
    "memory_consolidation_decay_rate": "decay_rate",
    "memory_consolidation_min_import": "min_importance",
    "memory_consolidation_trigger": "trigger_interval",
}


@dataclass
class ConsolidationConfig:
    """记忆合并配置（与 main longterm.ConsolidationConfig 对齐）。

    7 字段 = 6 业务字段 + 1 兼容字段：
      - similarity_threshold: 合并相似度阈值（>= 触发合并）
      - dedup_threshold: 去重相似度阈值（>= 视为重复）
      - ttl_days: 过期天数（0 = 永不过期）
      - decay_rate: 每日衰减系数（如 0.995 表示每天保留 99.5%）
      - min_importance: 低于此重要性且超 TTL 的条目会被淘汰
      - trigger_interval: 每存入 N 条新记忆后触发合并
      - source: 兼容字段，原始 APIConfig 引用（可为 None）

    通过 ``__getattr__`` 暴露 ``memory_consolidation_*`` 别名，使其能直接喂给
    现有的 ``LongTerm.set_consolidation_config`` 实现。
    """

    similarity_threshold: float = _DEFAULTS["similarity_threshold"]
    dedup_threshold: float = _DEFAULTS["dedup_threshold"]
    ttl_days: int = _DEFAULTS["ttl_days"]
    decay_rate: float = _DEFAULTS["decay_rate"]
    min_importance: float = _DEFAULTS["min_importance"]
    trigger_interval: int = _DEFAULTS["trigger_interval"]
    source: Optional[Any] = field(default=None, repr=False, compare=False)

    @classmethod
    def from_api_config(cls, cfg: Any) -> "ConsolidationConfig":
        """从 APIConfig 抽取 6 个 memory_consolidation_* 字段构造配置。"""
        if cfg is None:
            return cls()
        return cls(
            similarity_threshold=float(getattr(cfg, "memory_consolidation_similarity", _DEFAULTS["similarity_threshold"]) or _DEFAULTS["similarity_threshold"]),
            dedup_threshold=float(getattr(cfg, "memory_consolidation_dedup", _DEFAULTS["dedup_threshold"]) or _DEFAULTS["dedup_threshold"]),
            ttl_days=int(getattr(cfg, "memory_consolidation_ttl_days", _DEFAULTS["ttl_days"]) or _DEFAULTS["ttl_days"]),
            decay_rate=float(getattr(cfg, "memory_consolidation_decay_rate", _DEFAULTS["decay_rate"]) or _DEFAULTS["decay_rate"]),
            min_importance=float(getattr(cfg, "memory_consolidation_min_import", _DEFAULTS["min_importance"]) or _DEFAULTS["min_importance"]),
            trigger_interval=int(getattr(cfg, "memory_consolidation_trigger", _DEFAULTS["trigger_interval"]) or _DEFAULTS["trigger_interval"]),
            source=cfg,
        )

    @classmethod
    def default(cls) -> "ConsolidationConfig":
        return cls()

    def __getattr__(self, name: str):
        """兼容 LongTerm 现有 ``getattr(cfg, 'memory_consolidation_*')`` 读取路径。"""
        target = _LEGACY_ALIAS.get(name)
        if target is None:
            raise AttributeError(name)
        return getattr(self, target)


@dataclass
class MemoryStack:
    """三层记忆 + 偏好的聚合容器（对应 main memoryStack）。

    stm/ltm/preference 必填；graph_memory 启动期为 None，KGStore 就绪后由
    ``attach_graph`` 注入。
    """

    stm: Any
    ltm: Any
    preference: Any
    graph_memory: Optional[Any] = None

    def attach_graph(self, graph_memory: Any) -> None:
        """KGStore 就绪后注入图增强记忆层（对应 main attachGraph）。"""
        self.graph_memory = graph_memory
