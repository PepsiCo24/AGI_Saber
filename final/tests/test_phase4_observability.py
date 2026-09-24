import json
from types import SimpleNamespace

from internal.agent import memory_writer
from internal.agent.status import status
from internal.memory.memory import Item, LongTerm


class _Cfg:
    llm_model = "chat-model"
    embedding_model = "embed-model"
    memory_consolidation_similarity = 0.8
    memory_consolidation_dedup = 0.95
    memory_consolidation_ttl_days = 30
    memory_consolidation_decay_rate = 1.0
    memory_consolidation_min_import = 0.1
    memory_consolidation_trigger = 1

    def is_real_llm(self):
        return False


class _Events:
    def __init__(self):
        self.published = []

    def publish(self, event_type, payload):
        self.published.append((event_type, json.loads(payload)))


class _LtmRepo:
    def load(self):
        return []

    def save(self, *args, **kwargs):
        return 101

    def update_classified(self, *args, **kwargs):
        pass

    def update(self, *args, **kwargs):
        pass

    def delete(self, *args, **kwargs):
        pass


def test_agent_status_exposes_main_observability_fields():
    agent = SimpleNamespace(
        cfg=_Cfg(),
        inf=SimpleNamespace(
            ready=SimpleNamespace(
                milvus="connected",
                postgresql="connected",
                elasticsearch="disconnected",
                kafka="connected",
            )
        ),
        rag=SimpleNamespace(
            loaded=True,
            mode=lambda: "hybrid",
            chunks=lambda: [{"id": 1, "content": "x" * 80}],
        ),
        stm=SimpleNamespace(count=lambda: 2),
        ltm=SimpleNamespace(items=[object(), object(), object()]),
        preference=SimpleNamespace(get_all=lambda: {"city": "上海"}),
        get_tools=lambda: [{"name": "a"}, {"name": "b"}],
    )

    out = status(agent)

    assert out["rag_loaded"] is True
    assert out["rag_mode"] == "hybrid"
    assert out["rag_chunks"][0]["id"] == 1
    assert len(out["rag_chunks"][0]["content"]) <= 63
    assert out["short_term_count"] == 2
    assert out["long_term_count"] == 3
    assert out["llm_model"] == "chat-model"
    assert out["embedding_model"] == "embed-model"
    assert out["is_mock"] is True
    assert out["infrastructure"]["postgresql"] == "connected"


def test_longterm_add_publishes_audit_event():
    events = _Events()
    ltm = LongTerm(_Cfg(), SimpleNamespace(repo=SimpleNamespace(ltm=_LtmRepo(), events=events)))

    ltm.add("hello", importance=0.7)

    assert events.published
    event_type, payload = events.published[-1]
    assert event_type == "memory.longterm.add"
    assert payload["content"] == "hello"
    assert payload["importance"] == 0.7


def test_sync_consolidation_to_db_publishes_audit_event():
    events = _Events()
    repo = SimpleNamespace(ltm=_LtmRepo(), events=events)
    agent = SimpleNamespace(inf=SimpleNamespace(repo=repo))
    result = SimpleNamespace(
        delete_from_db=[7],
        update_in_db=[Item(content="merged", importance=0.8, embedding=[1.0], id=8)],
    )

    memory_writer.sync_consolidation_to_db(agent, result)

    event_types = [event for event, _payload in events.published]
    assert "memory.consolidate.delete" in event_types
    assert "memory.consolidate.update" in event_types
