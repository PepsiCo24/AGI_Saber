"""LongTerm.consolidate 三阶段对齐 main 分支 Go 实现的单元测试。

覆盖：
  1. 阶段 1：按条目 created_at 单独指数衰减
  2. 阶段 2 - dedup：sim ≥ dedup_threshold，j 被删除并入 delete_from_db
  3. 阶段 2 - merge：similarity_threshold ≤ sim < dedup_threshold，i 被合并替换
  4. 阶段 3：双条件淘汰（days > ttl_days 且 importance < min_importance）
"""
import math
import time
from types import SimpleNamespace

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


def _make_ltm():
    inf = SimpleNamespace(repo=SimpleNamespace(ltm=_LtmRepo()))
    return LongTerm(_Cfg(), inf)


def test_decay_per_item():
    """阶段 1：每条 item 按自己的 created_at 衰减，importance *= decay^days。"""
    ltm = _make_ltm()
    now = time.time()
    # item 0：1 天前；item 1：30 天前。两者 emb 正交，确保不会被 dedup/merge。
    ltm.items = [
        Item(
            content="recent",
            importance=0.8,
            embedding=[1.0, 0.0],
            id=10,
            created_at=now - 86400.0,
            last_accessed=now,
            category="fact",
        ),
        Item(
            content="old",
            importance=0.8,
            embedding=[0.0, 1.0],
            id=11,
            created_at=now - 30 * 86400.0,
            last_accessed=now,
            category="fact",
        ),
    ]
    decay = _Cfg.memory_consolidation_decay_rate

    result = ltm.consolidate()

    # 阶段 1 应被全部应用；阶段 2/3 不动这两条
    assert result.deduped == 0
    assert result.merged == 0
    # 第二条 30 天衰减后 importance ≈ 0.8 * 0.99^30 ≈ 0.591；> min_importance=0.1，故未过期
    assert result.expired == 0

    expected_recent = 0.8 * (decay ** 1.0)
    expected_old = 0.8 * (decay ** 30.0)
    # 两条都应该还在 self.items 中
    by_id = {it.id: it for it in ltm.items}
    assert math.isclose(by_id[10].importance, expected_recent, rel_tol=1e-3, abs_tol=1e-3)
    assert math.isclose(by_id[11].importance, expected_old, rel_tol=1e-3, abs_tol=1e-3)


def test_dedup_merges_on_high_similarity():
    """阶段 2 - dedup：cosine ≥ 0.95 → j 进 delete_from_db，i 吸收 j 的 importance / tags。"""
    ltm = _make_ltm()
    now = time.time()
    # 两条几乎一致的 emb，cosine ≈ 1
    ltm.items = [
        Item(
            content="user likes coffee",
            importance=0.5,
            embedding=[1.0, 0.0, 0.0],
            id=100,
            created_at=now - 60.0,
            last_accessed=now,
            tags=["a"],
        ),
        Item(
            content="user likes coffee",
            importance=0.7,
            embedding=[1.0, 0.0001, 0.0],
            id=101,
            created_at=now - 30.0,
            last_accessed=now,
            tags=["b"],
        ),
    ]

    result = ltm.consolidate()

    assert result.deduped == 1
    assert result.merged == 0
    assert 101 in result.delete_from_db
    assert len(ltm.items) == 1
    survivor = ltm.items[0]
    assert survivor.id == 100  # 保留 i
    # i 吸收 j 的 importance：取 max（衰减后再取 max，j 的 0.7 比 i 的 0.5 大）
    assert survivor.importance >= 0.5 * 0.99 ** 1  # 至少不低于衰减后的 i
    assert survivor.tags == ["a", "b"]


def test_merge_on_mid_similarity():
    """阶段 2 - merge：0.85 ≤ sim < 0.95 → i 与 j 合并替换 i，j 进 delete_from_db。"""
    ltm = _make_ltm()
    now = time.time()
    # 构造两个 emb 让 cosine 落在 [0.85, 0.95) 区间
    # 选 a=[1,0]，b=[1,0.5]：cosine = 1 / sqrt(1.25) ≈ 0.8944
    emb_a = [1.0, 0.0]
    emb_b = [1.0, 0.5]
    sim = sum(x * y for x, y in zip(emb_a, emb_b)) / (
        math.sqrt(sum(x * x for x in emb_a)) * math.sqrt(sum(x * x for x in emb_b))
    )
    assert 0.85 <= sim < 0.95, f"emb sim {sim} not in [0.85, 0.95)"

    ltm.items = [
        Item(
            content="A",
            importance=0.6,
            embedding=list(emb_a),
            id=200,
            created_at=now - 60.0,
            last_accessed=now,
            tags=["t1"],
            category="fact",
            slot_hint="memory_facts",
        ),
        Item(
            content="B",
            importance=0.4,
            embedding=list(emb_b),
            id=201,
            created_at=now - 120.0,
            last_accessed=now,
            tags=["t2"],
            category="preference",
        ),
    ]

    result = ltm.consolidate()

    assert result.merged == 1
    assert result.deduped == 0
    assert 201 in result.delete_from_db
    assert len(ltm.items) == 1
    merged = ltm.items[0]
    # content 用 "；" 拼接，包含两条原文
    assert "A" in merged.content and "B" in merged.content
    assert "；" in merged.content
    # importance = max
    assert math.isclose(merged.importance, max(
        0.6 * 0.99 ** ((now - (now - 60.0)) / 86400.0),
        0.4 * 0.99 ** ((now - (now - 120.0)) / 86400.0),
    ), rel_tol=1e-3, abs_tol=1e-3)
    # tags 合并去重
    assert merged.tags == ["t1", "t2"]
    # category / slot_hint 取 i 优先
    assert merged.category == "fact"
    assert merged.slot_hint == "memory_facts"
    # created_at 取更早
    assert math.isclose(merged.created_at, now - 120.0, rel_tol=0, abs_tol=1e-3)
    # embedding 加权平均（每维 = (wi*ai + wj*bj) / (wi+wj)）
    # 用衰减后的 importance 作为权重
    wi = 0.6 * 0.99 ** ((now - (now - 60.0)) / 86400.0)
    wj = 0.4 * 0.99 ** ((now - (now - 120.0)) / 86400.0)
    expected = [
        (emb_a[0] * wi + emb_b[0] * wj) / (wi + wj),
        (emb_a[1] * wi + emb_b[1] * wj) / (wi + wj),
    ]
    assert merged.embedding is not None
    for got, exp in zip(merged.embedding, expected):
        assert math.isclose(got, exp, rel_tol=1e-3, abs_tol=1e-3)
    # update_in_db 含被替换为 merged 后的 i 副本（沿用 i 的 id=200）
    assert len(result.update_in_db) == 1
    assert result.update_in_db[0].id == 200


def test_expire_double_condition():
    """阶段 3：仅同时满足 days > ttl_days 且 importance < min_importance 才淘汰。"""
    ltm = _make_ltm()
    now = time.time()
    # 关闭衰减影响：用 decay=1.0，避免衰减影响 importance 判定
    ltm.cfg = SimpleNamespace(
        memory_consolidation_similarity=0.85,
        memory_consolidation_dedup=0.95,
        memory_consolidation_ttl_days=30,
        memory_consolidation_decay_rate=1.0,
        memory_consolidation_min_import=0.1,
        memory_consolidation_trigger=5,
    )
    # 三条 emb 正交，避免被合并
    ltm.items = [
        # ttl 内（10 天）+ 低 importance：不淘汰
        Item(
            content="young low",
            importance=0.05,
            embedding=[1.0, 0.0, 0.0],
            id=300,
            created_at=now - 10 * 86400.0,
            last_accessed=now,
        ),
        # ttl 外（60 天）+ 高 importance：不淘汰
        Item(
            content="old high",
            importance=0.9,
            embedding=[0.0, 1.0, 0.0],
            id=301,
            created_at=now - 60 * 86400.0,
            last_accessed=now,
        ),
        # ttl 外（60 天）+ 低 importance：淘汰
        Item(
            content="old low",
            importance=0.05,
            embedding=[0.0, 0.0, 1.0],
            id=302,
            created_at=now - 60 * 86400.0,
            last_accessed=now,
        ),
    ]

    result = ltm.consolidate()

    assert result.expired == 1
    assert result.delete_from_db == [302]
    surviving_ids = sorted(it.id for it in ltm.items)
    assert surviving_ids == [300, 301]
