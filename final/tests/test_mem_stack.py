"""ConsolidationConfig + MemoryStack 单元测试（Task 26）。

对齐 main 分支：
- ConsolidationConfig 6 业务字段 + 1 兼容 source 字段
- 通过 memory_consolidation_* 别名兼容 LongTerm 现有 getattr 路径
- MemoryStack 聚合 stm/ltm/preference，graph_memory 由 attach_graph 后注入
"""

from types import SimpleNamespace

from internal.memory.mem_stack import ConsolidationConfig, MemoryStack


def test_default_consolidation_config_matches_main_defaults():
    cfg = ConsolidationConfig.default()
    assert cfg.similarity_threshold == 0.80
    assert cfg.dedup_threshold == 0.95
    assert cfg.ttl_days == 30
    assert cfg.decay_rate == 0.995
    assert cfg.min_importance == 0.3
    assert cfg.trigger_interval == 5
    assert cfg.source is None


def test_from_api_config_extracts_six_fields_and_keeps_source():
    api = SimpleNamespace(
        memory_consolidation_similarity=0.7,
        memory_consolidation_dedup=0.9,
        memory_consolidation_ttl_days=14,
        memory_consolidation_decay_rate=0.99,
        memory_consolidation_min_import=0.2,
        memory_consolidation_trigger=3,
    )
    cfg = ConsolidationConfig.from_api_config(api)
    assert cfg.similarity_threshold == 0.7
    assert cfg.dedup_threshold == 0.9
    assert cfg.ttl_days == 14
    assert cfg.decay_rate == 0.99
    assert cfg.min_importance == 0.2
    assert cfg.trigger_interval == 3
    assert cfg.source is api


def test_from_api_config_falls_back_to_defaults_when_zero_or_missing():
    api = SimpleNamespace(
        memory_consolidation_similarity=0,  # 0 应回退默认
        memory_consolidation_decay_rate=None,
    )
    cfg = ConsolidationConfig.from_api_config(api)
    assert cfg.similarity_threshold == 0.80
    assert cfg.decay_rate == 0.995
    # 未设置的 trigger / ttl / dedup / min_import 取默认
    assert cfg.dedup_threshold == 0.95
    assert cfg.trigger_interval == 5


def test_legacy_alias_attribute_access():
    cfg = ConsolidationConfig(
        similarity_threshold=0.5,
        dedup_threshold=0.6,
        ttl_days=7,
        decay_rate=0.9,
        min_importance=0.1,
        trigger_interval=2,
    )
    # LongTerm.consolidate 内部用 getattr(cfg, "memory_consolidation_*") 读取
    assert cfg.memory_consolidation_similarity == 0.5
    assert cfg.memory_consolidation_dedup == 0.6
    assert cfg.memory_consolidation_ttl_days == 7
    assert cfg.memory_consolidation_decay_rate == 0.9
    assert cfg.memory_consolidation_min_import == 0.1
    assert cfg.memory_consolidation_trigger == 2


def test_alias_unknown_attr_raises_attribute_error():
    cfg = ConsolidationConfig.default()
    try:
        _ = cfg.nonexistent_field
    except AttributeError:
        pass
    else:
        raise AssertionError("expected AttributeError on unknown attribute")


def test_memory_stack_attach_graph_is_idempotent():
    stack = MemoryStack(stm="STM", ltm="LTM", preference="PREF")
    assert stack.graph_memory is None
    stack.attach_graph("GM1")
    assert stack.graph_memory == "GM1"
    # 二次调用覆盖
    stack.attach_graph("GM2")
    assert stack.graph_memory == "GM2"
    # nil 注入也允许（KG 不可用降级路径）
    stack.attach_graph(None)
    assert stack.graph_memory is None


def test_consolidation_config_can_drive_longterm_set_consolidation_config():
    """LongTerm.set_consolidation_config 接受 ConsolidationConfig 后，
    后续 consolidate 通过 memory_consolidation_* 别名读取应正常工作。"""
    from internal.memory.memory import LongTerm

    class _Cfg:
        memory_consolidation_similarity = 0.85
        memory_consolidation_dedup = 0.95
        memory_consolidation_ttl_days = 30
        memory_consolidation_decay_rate = 0.99
        memory_consolidation_min_import = 0.1
        memory_consolidation_trigger = 5

    ltm = LongTerm(_Cfg(), SimpleNamespace(repo=SimpleNamespace(longterm=None)))
    new_cfg = ConsolidationConfig(
        similarity_threshold=0.5,
        dedup_threshold=0.6,
        ttl_days=7,
        decay_rate=1.0,
        min_importance=0.2,
        trigger_interval=2,
    )
    ltm.set_consolidation_config(new_cfg)
    # 经由别名读取应等于 dataclass 字段
    assert getattr(ltm.cfg, "memory_consolidation_trigger") == 2
    ltm._items_since_last = 2
    assert ltm.need_consolidation() is True
