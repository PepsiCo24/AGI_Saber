"""MemoryManager.recall 单元测试（Task 15：单方法 LTM → graph 1-hop 扩展）。"""
import time
from types import SimpleNamespace
from typing import List

from internal.memory.memory import Item, LongTerm, MemoryManager


class _LtmRepo:
    def load(self):
        return []

    def save(self, *args, **kwargs):
        return 0

    def update_classified(self, *args, **kwargs):
        pass

    def update(self, *args, **kwargs):
        pass


class _PrefRepo:
    def load(self, user_id):
        return {}

    def save(self, *args, **kwargs):
        pass


class _Cfg:
    short_term_max_turns = 10
    memory_consolidation_similarity = 0.85
    memory_consolidation_dedup = 0.95
    memory_consolidation_ttl_days = 30
    memory_consolidation_decay_rate = 0.99
    memory_consolidation_min_import = 0.1
    memory_consolidation_trigger = 5


class _GraphStub:
    def __init__(self, related_map):
        self._map = related_map

    def find_related(self, mem_id):
        return list(self._map.get(mem_id, []))

    # MemoryManager 在 set_graph_memory 时会透传到 LongTerm；LongTerm 在
    # consolidate / recall 中只会调它本身已 hook 的方法，这里 recall 不需要这些。
    def filter_protected(self, ids, threshold):
        return []

    def delete_from_graph(self, mem_id):
        pass

    def update_node(self, item):
        pass

    def add_to_graph(self, item, neighbors=None):
        pass

    def bulk_index(self, items):
        pass


def _make_mgr(graph=None):
    inf = SimpleNamespace(repo=SimpleNamespace(ltm=_LtmRepo(), preference=_PrefRepo()))
    return MemoryManager(_Cfg(), inf, graph_memory=graph)


def test_recall_no_graph_returns_seed_only():
    mgr = _make_mgr()
    now = time.time()
    mgr.long_term.items = [
        Item(content="a", importance=0.9, embedding=[1.0, 0.0], id=1,
             created_at=now, last_accessed=now),
        Item(content="b", importance=0.5, embedding=[0.5, 0.5], id=2,
             created_at=now, last_accessed=now),
    ]

    hits = mgr.recall("query", top_k=2, query_embedding=[1.0, 0.0])

    # 两条都被召回；按 score desc 排序：a 完全相似 → score 高
    assert [h.id for h in hits] == [1, 2]


def test_recall_graph_expands_with_score_045_and_topk():
    """种子 1 命中；图扩展引入 id=3，应得 score=0.45；top_k 截断保留高分。"""
    graph = _GraphStub(related_map={1: [3]})
    mgr = _make_mgr(graph=graph)
    now = time.time()
    mgr.long_term.items = [
        # 种子（高 sim 高分）
        Item(content="seed", importance=0.9, embedding=[1.0, 0.0], id=1,
             created_at=now, last_accessed=now, category="fact"),
        # 与 query 完全无关，但通过图与 1 关联
        Item(content="related", importance=0.5, embedding=[0.0, 1.0], id=3,
             created_at=now, last_accessed=now, category="fact"),
    ]

    hits = mgr.recall("query", top_k=2, query_embedding=[1.0, 0.0])

    by_id = {h.id: h for h in hits}
    assert 1 in by_id and 3 in by_id
    # 扩展项 score=0.45（种子项是 sim*0.7+imp*0.3）
    assert by_id[3].score == 0.45
    assert by_id[1].score >= by_id[3].score


def test_recall_graph_expansion_filtered_by_categories():
    """扩展项 category 不在 filter 内时被丢弃。"""
    graph = _GraphStub(related_map={1: [3]})
    mgr = _make_mgr(graph=graph)
    now = time.time()
    mgr.long_term.items = [
        Item(content="seed", importance=0.9, embedding=[1.0, 0.0], id=1,
             created_at=now, last_accessed=now, category="fact"),
        Item(content="other", importance=0.5, embedding=[0.0, 1.0], id=3,
             created_at=now, last_accessed=now, category="preference"),
    ]

    hits = mgr.recall("query", top_k=5, query_embedding=[1.0, 0.0],
                      categories=["fact"])

    ids = [h.id for h in hits]
    # 种子 1 命中且匹配 category；扩展 3 因 category 不命中被丢弃
    assert ids == [1]
