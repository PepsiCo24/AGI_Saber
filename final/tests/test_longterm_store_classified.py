from types import SimpleNamespace

from internal.memory.memory import LongTerm


class _LtmRepo:
    def __init__(self):
        self.saves = []
        self.updates = []
        self._next_id = 1

    def load(self):
        return []

    def save(self, content, importance, emb_json,
             created_at=None, last_accessed=None,
             category="", tags=None, slot_hint="", score=0.0):
        self.saves.append({
            "content": content,
            "importance": importance,
            "category": category,
            "tags": list(tags or []),
            "slot_hint": slot_hint,
        })
        rid = self._next_id
        self._next_id += 1
        return rid

    def update_classified(self, item_id, importance, tags, category, slot_hint, last_accessed):
        self.updates.append({
            "id": item_id,
            "importance": importance,
            "tags": list(tags),
            "category": category,
            "slot_hint": slot_hint,
        })


class _Cfg:
    memory_consolidation_dedup = 0.95
    memory_consolidation_trigger = 5


def _make_ltm():
    repo = _LtmRepo()
    inf = SimpleNamespace(repo=SimpleNamespace(ltm=repo))
    return LongTerm(_Cfg(), inf), repo


def test_store_classified_dedup_updates_existing_item_in_place():
    ltm, repo = _make_ltm()
    emb = [1.0, 0.0, 0.0]

    assert ltm.store_classified("hello", 0.4, emb, "fact", ["t1"], "memory_facts") is True
    # 几乎同向（极小扰动），cosine ~ 1.0 > 0.95
    near_emb = [1.0, 0.0001, 0.0]
    assert ltm.store_classified("hello v2", 0.7, near_emb, "preference", ["t2"], "preference") is False

    assert len(ltm.items) == 1
    item = ltm.items[0]
    assert item.importance == 0.7
    assert item.tags == ["t1", "t2"]
    # category/slot_hint 不覆盖已有非空非 general 值
    assert item.category == "fact"
    assert item.slot_hint == "memory_facts"
    assert len(repo.saves) == 1
    assert len(repo.updates) == 1
    assert repo.updates[0]["importance"] == 0.7
    assert repo.updates[0]["tags"] == ["t1", "t2"]


def test_store_classified_low_similarity_inserts_new_row():
    ltm, repo = _make_ltm()
    assert ltm.store_classified("a", 0.5, [1.0, 0.0, 0.0], "fact", [], "") is True
    # 正交向量 cosine = 0
    assert ltm.store_classified("b", 0.5, [0.0, 1.0, 0.0], "fact", [], "") is True

    assert len(ltm.items) == 2
    assert len(repo.saves) == 2
    assert repo.updates == []


def test_store_classified_without_embedding_skips_dedup():
    ltm, repo = _make_ltm()
    assert ltm.store_classified("hello", 0.5, None, "fact", [], "") is True
    assert ltm.store_classified("hello", 0.5, None, "fact", [], "") is True

    assert len(ltm.items) == 2
    assert len(repo.saves) == 2
    assert repo.updates == []


def test_mark_superseded_marks_existing_conflicting_memory():
    ltm, _repo = _make_ltm()
    assert ltm.store_classified("用户姓名: 张三", 0.7, [1.0, 0.0], "identity", ["name"], "profile") is True
    assert ltm.store_classified("用户姓名: 李四", 0.7, [0.7, 0.7], "identity", ["name"], "profile") is True

    old_id = ltm.items[0].id
    new_id = ltm.items[1].id
    marked = ltm.mark_superseded([old_id], new_id)

    assert marked == [old_id]
    assert ltm.items[0].status == "superseded"
    assert ltm.items[0].superseded_by == new_id
    assert ltm.items[1].status == "active"
    assert [item.content for item in ltm.active_items()] == ["用户姓名: 李四"]
