"""Task 23：graph_aware_consolidate + maybe_consolidate_memory 路由分支测试。

覆盖 SubTask 23.2：finalize 根据 graph_memory 切 GraphAwareConsolidate / 普通 Consolidate。
"""
from types import SimpleNamespace

from internal.agent import memory_writer
from internal.memory.graph_memory import GraphMemory


class _Cfg:
    memory_consolidation_similarity = 0.85
    memory_consolidation_dedup = 0.95
    memory_consolidation_ttl_days = 30
    memory_consolidation_decay_rate = 0.99
    memory_consolidation_min_import = 0.1
    memory_consolidation_trigger = 5
    kg_max_hops = 1


class _NeoStub:
    def __init__(self, real=True):
        self._real = real
        self.cypher_calls = []

    def is_real(self):
        return self._real

    def run_cypher(self, q, params=None, **kwargs):
        self.cypher_calls.append((q, params))
        return []


class _Result:
    def __init__(self, delete=None, update=None):
        self.delete_from_db = list(delete or [])
        self.update_in_db = list(update or [])


class _LtmStub:
    def __init__(self, *, need=True, result=None):
        self._need = need
        self._result = result if result is not None else _Result()
        self.calls = []

    def need_consolidation(self):
        self.calls.append("need_consolidation")
        return self._need

    def consolidate(self):
        self.calls.append("consolidate")
        return self._result

    def last_id(self):
        return -1


class _LtmRepo:
    def __init__(self):
        self.deleted = []
        self.updated = []

    def delete(self, ids):
        self.deleted.append(list(ids))

    def update(self, item_id, content, importance, embedding_json):
        self.updated.append((item_id, content, importance, embedding_json))


# ─── graph_aware_consolidate ───────────────────────────────────────────────


def test_graph_aware_consolidate_protects_high_centrality(monkeypatch):
    """delete_from_db 中入度 >= 3 的节点被剔除。"""
    ltm = _LtmStub(result=_Result(delete=[1, 2, 3]))
    gm = GraphMemory(_Cfg(), _NeoStub(), ltm=ltm)

    # 2 是高中心度节点 → 应保护
    monkeypatch.setattr(gm, "_get_high_centrality_ids",
                        lambda candidates, threshold: [2])
    deletions = []
    monkeypatch.setattr(gm, "_delete_memory_node",
                        lambda mem_id: deletions.append(mem_id))

    res = gm.graph_aware_consolidate()
    assert res.delete_from_db == [1, 3]


def test_graph_aware_consolidate_no_neo4j_passthrough():
    """Neo4j 不可用时直接返回 ltm.consolidate 结果。"""
    ltm = _LtmStub(result=_Result(delete=[5]))
    gm = GraphMemory(_Cfg(), _NeoStub(real=False), ltm=ltm)
    res = gm.graph_aware_consolidate()
    assert res.delete_from_db == [5]


def test_graph_aware_consolidate_no_ltm_returns_none():
    gm = GraphMemory(_Cfg(), _NeoStub())
    assert gm.graph_aware_consolidate() is None


# ─── maybe_consolidate_memory 路由分支 ─────────────────────────────────────


def test_maybe_consolidate_memory_uses_graph_aware_when_available():
    """有 graph_memory 时走 graph_aware_consolidate。"""
    calls = []

    class _GM:
        def graph_aware_consolidate(self):
            calls.append("graph_aware")
            return _Result(delete=[7])

    ltm = _LtmStub(need=True)
    repo = SimpleNamespace(ltm=_LtmRepo())
    agent = SimpleNamespace(
        ltm=ltm,
        graph_memory=_GM(),
        inf=SimpleNamespace(repo=repo),
    )
    memory_writer.maybe_consolidate_memory(agent)
    # 走 graph_aware，不应再调 ltm.consolidate
    assert calls == ["graph_aware"]
    assert "consolidate" not in ltm.calls
    assert repo.ltm.deleted == [[7]]


def test_maybe_consolidate_memory_falls_back_to_ltm_consolidate():
    """没有 graph_memory 时走纯 ltm.consolidate。"""
    ltm = _LtmStub(need=True, result=_Result(delete=[3]))
    repo = SimpleNamespace(ltm=_LtmRepo())
    agent = SimpleNamespace(
        ltm=ltm,
        graph_memory=None,
        inf=SimpleNamespace(repo=repo),
    )
    memory_writer.maybe_consolidate_memory(agent)
    assert "consolidate" in ltm.calls
    assert repo.ltm.deleted == [[3]]


def test_maybe_consolidate_memory_skipped_when_below_threshold():
    """need_consolidation 返回 False 时整个流程不触发。"""
    ltm = _LtmStub(need=False)
    repo = SimpleNamespace(ltm=_LtmRepo())
    agent = SimpleNamespace(
        ltm=ltm,
        graph_memory=None,
        inf=SimpleNamespace(repo=repo),
    )
    memory_writer.maybe_consolidate_memory(agent)
    assert ltm.calls == ["need_consolidation"]
    assert repo.ltm.deleted == []
