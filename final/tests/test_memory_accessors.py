"""LongTerm + GraphMemory 访问器对齐 main 分支的单元测试。

覆盖 Task 19：
- LongTerm.snapshot / find_by_id / last_id / last_item / sync_last_item_pg_id /
  set_consolidation_config
- GraphMemory.sync_prev_id / set_consolidation_config / need_consolidation
"""
import time
from types import SimpleNamespace

from internal.memory.graph_memory import GraphMemory
from internal.memory.memory import Item, LongTerm


class _LtmRepo:
    def load(self):
        return []

    def save(self, *args, **kwargs):
        return 0

    def update_classified(self, *args, **kwargs):
        pass

    def update(self, *args, **kwargs):
        pass


class _Cfg:
    memory_consolidation_similarity = 0.85
    memory_consolidation_dedup = 0.95
    memory_consolidation_ttl_days = 30
    memory_consolidation_decay_rate = 0.99
    memory_consolidation_min_import = 0.1
    memory_consolidation_trigger = 5
    kg_max_hops = 1


def _ltm() -> LongTerm:
    return LongTerm(_Cfg(), SimpleNamespace(repo=SimpleNamespace(ltm=_LtmRepo())))


def _seed(ltm: LongTerm):
    now = time.time()
    ltm.items = [
        Item(content="A", importance=0.5, embedding=[1.0, 0.0], id=10,
             created_at=now - 60, last_accessed=now, tags=["x"]),
        Item(content="B", importance=0.7, embedding=[0.0, 1.0], id=20,
             created_at=now - 30, last_accessed=now, tags=["y"]),
    ]
    ltm._next_id = 21


# ─── LongTerm 访问器 ──────────────────────────────────────────────────────


def test_snapshot_returns_independent_items():
    ltm = _ltm()
    _seed(ltm)
    snap = ltm.snapshot()
    assert len(snap) == 2
    # 修改 snapshot 不应影响内部 items
    snap[0].content = "MUTATED"
    snap[0].importance = 9.99
    assert ltm.items[0].content == "A"
    assert ltm.items[0].importance == 0.5


def test_find_by_id_hit_and_miss():
    ltm = _ltm()
    _seed(ltm)
    item, ok = ltm.find_by_id(20)
    assert ok
    assert item.content == "B"
    # 修改返回的拷贝不影响内部
    item.content = "Z"
    assert ltm.items[1].content == "B"

    miss, ok2 = ltm.find_by_id(999)
    assert not ok2
    assert miss is None


def test_last_id_and_last_item():
    ltm = _ltm()
    assert ltm.last_id() == -1
    none_item, ok = ltm.last_item()
    assert not ok
    assert none_item is None

    _seed(ltm)
    assert ltm.last_id() == 20
    last, ok = ltm.last_item()
    assert ok
    assert last.id == 20
    assert last.content == "B"
    # 拷贝独立
    last.importance = 0.0
    assert ltm.items[-1].importance == 0.7


def test_sync_last_item_pg_id_overwrites_id_and_advances_next():
    ltm = _ltm()
    _seed(ltm)
    # 模拟 PG RETURNING 拿到真实主键 100
    ltm.sync_last_item_pg_id(100)
    assert ltm.items[-1].id == 100
    assert ltm._next_id == 101

    # pg_id <= 0 直接 no-op
    ltm.sync_last_item_pg_id(0)
    ltm.sync_last_item_pg_id(-1)
    assert ltm.items[-1].id == 100
    assert ltm._next_id == 101


def test_sync_last_item_pg_id_empty_items_noop():
    ltm = _ltm()
    ltm.sync_last_item_pg_id(50)
    assert ltm._next_id == 0
    assert ltm.items == []


def test_set_consolidation_config_replaces_cfg():
    ltm = _ltm()
    new_cfg = SimpleNamespace(
        memory_consolidation_similarity=0.5,
        memory_consolidation_dedup=0.6,
        memory_consolidation_ttl_days=7,
        memory_consolidation_decay_rate=1.0,
        memory_consolidation_min_import=0.2,
        memory_consolidation_trigger=2,
    )
    ltm.set_consolidation_config(new_cfg)
    assert ltm.cfg is new_cfg
    # need_consolidation 立刻按新阈值
    ltm._items_since_last = 2
    assert ltm.need_consolidation() is True
    ltm._items_since_last = 1
    assert ltm.need_consolidation() is False

    # None 不覆盖
    ltm.set_consolidation_config(None)
    assert ltm.cfg is new_cfg


# ─── GraphMemory 代理 ────────────────────────────────────────────────────


class _NeoStub:
    def is_real(self):
        return True

    def run_cypher(self, *_args, **_kwargs):
        return []


def test_graph_memory_sync_prev_id_pulls_from_ltm():
    ltm = _ltm()
    _seed(ltm)
    gm = GraphMemory(_Cfg(), _NeoStub(), ltm=ltm)
    assert gm.prev_id == -1
    gm.sync_prev_id()
    assert gm.prev_id == 20

    # 没有 LTM 时 no-op
    gm2 = GraphMemory(_Cfg(), _NeoStub())
    gm2.prev_id = 7
    gm2.sync_prev_id()
    assert gm2.prev_id == 7


def test_graph_memory_set_consolidation_config_proxies():
    ltm = _ltm()
    gm = GraphMemory(_Cfg(), _NeoStub(), ltm=ltm)
    new_cfg = SimpleNamespace(
        memory_consolidation_similarity=0.5,
        memory_consolidation_dedup=0.6,
        memory_consolidation_ttl_days=7,
        memory_consolidation_decay_rate=1.0,
        memory_consolidation_min_import=0.2,
        memory_consolidation_trigger=3,
    )
    gm.set_consolidation_config(new_cfg)
    assert ltm.cfg is new_cfg


def test_graph_memory_need_consolidation_proxies():
    ltm = _ltm()
    gm = GraphMemory(_Cfg(), _NeoStub(), ltm=ltm)
    ltm._items_since_last = 0
    assert gm.need_consolidation() is False
    ltm._items_since_last = 5
    assert gm.need_consolidation() is True

    gm_no_ltm = GraphMemory(_Cfg(), _NeoStub())
    assert gm_no_ltm.need_consolidation() is False


def test_set_graph_memory_back_injects_ltm():
    ltm = _ltm()
    gm = GraphMemory(_Cfg(), _NeoStub())
    assert gm.ltm is None
    ltm.set_graph_memory(gm)
    assert gm.ltm is ltm
    # 解除
    ltm.set_graph_memory(None)
    assert ltm.graph_memory is None
