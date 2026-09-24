"""LongTerm.recall_by_filter 对齐 main 分支 Go RecallByFilter 的单元测试。

覆盖：
  1. top_k=2 + emb 充足 → 返回按 score desc 排序后的 2 条
  2. categories 过滤 + require_tags 过滤命中
  3. max_age_hours 过滤掉超龄条目
  4. query_embedding=None 走 TF fallback；命中时 last_accessed 被回写
"""
import math
import time
from types import SimpleNamespace

from internal.memory.memory import Item, LongTerm, RecallFilter


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


def _make_ltm():
    inf = SimpleNamespace(repo=SimpleNamespace(ltm=_LtmRepo()))
    return LongTerm(_Cfg(), inf)


def test_recall_by_filter_top_k_orders_by_score_desc():
    """top_k=2 + 充足 emb → 返回 2 条且按 score desc 排序。"""
    ltm = _make_ltm()
    now = time.time()
    # 三条 emb，与 query [1,0,0] 的 cosine 分别为 1.0 / 0.5 / 0.0
    ltm.items = [
        Item(
            content="A",
            importance=0.5,
            embedding=[1.0, 0.0, 0.0],
            id=1,
            created_at=now - 60.0,
            last_accessed=now - 60.0,
        ),
        Item(
            content="B",
            importance=0.9,
            embedding=[1.0, math.sqrt(3.0), 0.0],  # cosine ~ 0.5
            id=2,
            created_at=now - 60.0,
            last_accessed=now - 60.0,
        ),
        Item(
            content="C",
            importance=0.5,
            embedding=[0.0, 1.0, 0.0],  # cosine 0
            id=3,
            created_at=now - 60.0,
            last_accessed=now - 60.0,
        ),
    ]
    rfilter = RecallFilter(top_k=2)
    hits = ltm.recall_by_filter("any", [1.0, 0.0, 0.0], rfilter)
    # A: 1*0.7 + 0.5*0.3 = 0.85；B: 0.5*0.7 + 0.9*0.3 = 0.62；C: 0+0.15=0.15(<0.4 → 被过滤)
    assert len(hits) == 2
    assert hits[0].id == 1
    assert hits[1].id == 2
    assert hits[0].score > hits[1].score
    assert math.isclose(hits[0].score, 0.85, rel_tol=1e-3, abs_tol=1e-3)


def test_recall_by_filter_categories_and_require_tags():
    """categories 命中其一 + require_tags 必须全部包含。"""
    ltm = _make_ltm()
    now = time.time()
    ltm.items = [
        Item(  # 命中 category=fact 且 tags 完整覆盖
            content="hit",
            importance=0.9,
            embedding=[1.0, 0.0],
            id=10,
            created_at=now - 60.0,
            last_accessed=now - 60.0,
            category="fact",
            tags=["t1", "t2", "extra"],
        ),
        Item(  # category 不在 categories 内 → 滤掉
            content="wrong cat",
            importance=0.9,
            embedding=[1.0, 0.0],
            id=11,
            created_at=now - 60.0,
            last_accessed=now - 60.0,
            category="preference",
            tags=["t1", "t2"],
        ),
        Item(  # category 命中但缺 require_tag → 滤掉
            content="missing tag",
            importance=0.9,
            embedding=[1.0, 0.0],
            id=12,
            created_at=now - 60.0,
            last_accessed=now - 60.0,
            category="fact",
            tags=["t1"],
        ),
    ]
    rfilter = RecallFilter(
        categories=["fact"],
        require_tags=["t1", "t2"],
        top_k=10,
    )
    hits = ltm.recall_by_filter("any", [1.0, 0.0], rfilter)
    assert len(hits) == 1
    assert hits[0].id == 10


def test_recall_by_filter_max_age_hours_excludes_stale():
    """max_age_hours 按 created_at 计算超龄条目应被过滤掉。"""
    ltm = _make_ltm()
    now = time.time()
    ltm.items = [
        Item(  # 1 小时前，young
            content="young",
            importance=0.9,
            embedding=[1.0, 0.0],
            id=20,
            created_at=now - 3600.0,
            last_accessed=now - 3600.0,
        ),
        Item(  # 48 小时前 → 超过 24h
            content="old",
            importance=0.9,
            embedding=[1.0, 0.0],
            id=21,
            created_at=now - 48 * 3600.0,
            last_accessed=now - 48 * 3600.0,
        ),
    ]
    rfilter = RecallFilter(max_age_hours=24, top_k=10)
    hits = ltm.recall_by_filter("any", [1.0, 0.0], rfilter)
    assert len(hits) == 1
    assert hits[0].id == 20


def test_recall_by_filter_tf_fallback_writes_last_accessed():
    """query_embedding=None 走 TF fallback；命中时回写 last_accessed=now。"""
    ltm = _make_ltm()
    now = time.time()
    old_ts = now - 7200.0
    ltm.items = [
        Item(
            content="user likes coffee very much",
            importance=0.5,
            embedding=None,
            id=30,
            created_at=old_ts,
            last_accessed=old_ts,
        ),
        Item(
            content="totally unrelated text apple banana",
            importance=0.5,
            embedding=None,
            id=31,
            created_at=old_ts,
            last_accessed=old_ts,
        ),
    ]
    rfilter = RecallFilter(top_k=5)
    before = time.time()
    hits = ltm.recall_by_filter("user likes coffee", None, rfilter)
    after = time.time()

    # 至少命中第一条；第二条由于完全无重叠 token，sim≈0 → 被阈值过滤
    assert any(h.id == 30 for h in hits)
    # 命中条目的内存中原始 item 的 last_accessed 应被回写到 now
    target = next(it for it in ltm.items if it.id == 30)
    assert before <= target.last_accessed <= after
    # 未命中的条目 last_accessed 应保持原值
    miss = next(it for it in ltm.items if it.id == 31)
    assert math.isclose(miss.last_accessed, old_ts, rel_tol=0, abs_tol=1e-3)
    # 返回 Item 副本，score 字段 > 0
    hit30 = next(h for h in hits if h.id == 30)
    assert hit30.score > 0.0
