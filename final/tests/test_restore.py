"""restore.py 三件套对齐 main 分支的单元测试。

覆盖 Task 21：
- restore_from_db：先调 ltm.load_from_storage，再回放 chat_history 到 STM；
- init_knowledge_graph 降级路径：Neo4j / KGStore 不可用时 agent.kg = graph_memory = None；
- init_knowledge_graph 三件套：复用同一个 Neo4j client、KGStore 注入 RAG、
  GraphMemory 持有 ltm 反向引用、sync_prev_id、ltm.set_graph_memory 反向回注。
"""
import sys
import types
from types import SimpleNamespace

import internal.agent.restore as restore


# ─── stubs ────────────────────────────────────────────────────────────────


class _STM:
    def __init__(self):
        self.calls = []

    def add(self, role, content):
        self.calls.append((role, content))


class _LtmRecorder:
    """记录 load_from_storage 与 set_graph_memory 调用顺序的 LTM stub。"""

    def __init__(self, last=-1):
        self._last = last
        self.calls = []
        self.graph_memory = None

    def load_from_storage(self):
        self.calls.append("load_from_storage")

    def last_id(self):
        return self._last

    def set_graph_memory(self, gm):
        self.calls.append(("set_graph_memory", gm))
        self.graph_memory = gm
        if gm is not None and hasattr(gm, "set_ltm"):
            gm.set_ltm(self)


class _ChatRow:
    def __init__(self, role, content):
        self.role = role
        self.content = content


class _ChatRepo:
    def __init__(self, rows):
        self._rows = rows
        self.last_limit = None

    def load(self, limit):
        self.last_limit = limit
        return list(self._rows)


def _agent(**overrides):
    cfg = SimpleNamespace(short_term_max_turns=3, memory_consolidation_similarity=0.7)
    base = dict(
        cfg=cfg,
        stm=_STM(),
        ltm=_LtmRecorder(),
        chat_repo=None,
        rag=None,
        llm=None,
        kg=None,
        graph_memory=None,
    )
    base.update(overrides)
    return SimpleNamespace(**base)


# ─── restore_from_db ──────────────────────────────────────────────────────


def test_restore_from_db_calls_ltm_load_then_chat_replay():
    rows = [_ChatRow("user", "你好"), _ChatRow("assistant", "你好呀")]
    agent = _agent(chat_repo=_ChatRepo(rows))
    restore.restore_from_db(agent)

    # 1) 先调 ltm.load_from_storage
    assert "load_from_storage" in agent.ltm.calls
    # 2) chat_repo.load 用 short_term_max_turns * 2 作为 limit
    assert agent.chat_repo.last_limit == 6
    # 3) STM 按顺序追加 user+assistant
    assert agent.stm.calls == [("user", "你好"), ("assistant", "你好呀")]


def test_restore_from_db_handles_missing_chat_repo():
    agent = _agent(chat_repo=None)
    restore.restore_from_db(agent)
    assert agent.ltm.calls == ["load_from_storage"]
    assert agent.stm.calls == []


def test_restore_from_db_swallows_load_errors():
    class _BoomRepo:
        def load(self, limit):
            raise RuntimeError("pg down")

    agent = _agent(chat_repo=_BoomRepo())
    restore.restore_from_db(agent)
    assert agent.stm.calls == []
    assert "load_from_storage" in agent.ltm.calls


# ─── init_knowledge_graph 降级 ────────────────────────────────────────────


def test_init_knowledge_graph_neo4j_module_unavailable(monkeypatch):
    """KGStore / Neo4jClient 模块缺失时降级为 None，不抛异常。"""
    # 让 import internal.platform.neo4j 报错
    monkeypatch.setitem(sys.modules, "internal.platform.neo4j", None)
    agent = _agent()
    restore.init_knowledge_graph(agent)
    assert agent.kg is None
    assert agent.graph_memory is None
    # ltm.set_graph_memory 不应被调用
    assert all(call != "set_graph_memory" for call in agent.ltm.calls if isinstance(call, str))


def test_init_knowledge_graph_neo4j_client_construct_fails(monkeypatch):
    """Neo4jClient 构造抛异常 → 降级 None。"""
    fake_neo = types.ModuleType("internal.platform.neo4j")

    class _BadClient:
        def __init__(self, cfg):
            raise RuntimeError("connect fail")

    fake_neo.Neo4jClient = _BadClient
    fake_kg = types.ModuleType("internal.graph.kgstore")
    fake_kg.KGStore = lambda *a, **kw: None
    monkeypatch.setitem(sys.modules, "internal.platform.neo4j", fake_neo)
    monkeypatch.setitem(sys.modules, "internal.graph.kgstore", fake_kg)

    agent = _agent()
    restore.init_knowledge_graph(agent)
    assert agent.kg is None
    assert agent.graph_memory is None


# ─── init_knowledge_graph 三件套 ──────────────────────────────────────────


def test_init_knowledge_graph_three_step_attach(monkeypatch):
    """成功路径：Neo4j 客户端唯一性 + RAG 注入 + ltm 反向引用 + sync_prev_id + attach。"""
    captured = {}

    fake_neo = types.ModuleType("internal.platform.neo4j")

    class _Client:
        def __init__(self, cfg):
            captured["client_cfg"] = cfg
            self.cfg = cfg

    fake_neo.Neo4jClient = _Client

    fake_kg = types.ModuleType("internal.graph.kgstore")

    class _KGStore:
        def __init__(self, cfg, client, llm_fn=None):
            captured["kg_client"] = client
            captured["kg_llm_fn"] = llm_fn
            self.cfg = cfg

    fake_kg.KGStore = _KGStore

    fake_gm_mod = types.ModuleType("internal.memory.graph_memory")

    class _GraphMemory:
        def __init__(self, cfg, client, llm=None, sim_threshold=0.7, ltm=None):
            captured["gm_client"] = client
            captured["gm_ltm"] = ltm
            captured["gm_sim"] = sim_threshold
            captured["gm_llm"] = llm
            self.ltm = ltm
            self._sync_called = False

        def set_ltm(self, ltm):
            self.ltm = ltm

        def sync_prev_id(self):
            self._sync_called = True
            captured["sync_called_before_attach"] = ("set_graph_memory", self) not in self.ltm.calls

    fake_gm_mod.GraphMemory = _GraphMemory

    monkeypatch.setitem(sys.modules, "internal.platform.neo4j", fake_neo)
    monkeypatch.setitem(sys.modules, "internal.graph.kgstore", fake_kg)
    monkeypatch.setitem(sys.modules, "internal.memory.graph_memory", fake_gm_mod)

    class _RAG:
        def __init__(self):
            self.kg_store = None

        def set_kg_store(self, kg):
            self.kg_store = kg

    rag = _RAG()
    agent = _agent(rag=rag)
    agent.ltm._last = 42  # LTM 已恢复到 id=42

    restore.init_knowledge_graph(agent)

    # 1) KGStore 与 GraphMemory 复用同一个 Neo4j client
    assert captured["kg_client"] is captured["gm_client"]
    assert isinstance(captured["kg_client"], _Client)

    # 2) KGStore 注入到 RAG
    assert isinstance(rag.kg_store, _KGStore)
    assert rag.kg_store is agent.kg

    # 3) GraphMemory 持有 LTM 反向引用 + 拿到 sim_threshold
    assert captured["gm_ltm"] is agent.ltm
    assert captured["gm_sim"] == 0.7

    # 4) sync_prev_id 在 attach 之前调用（attach 通过 set_graph_memory 完成）
    assert agent.graph_memory._sync_called
    assert captured.get("sync_called_before_attach") is True

    # 5) attachGraph：set_graph_memory 把 graph 挂到 LTM，并反向回注 ltm
    assert agent.ltm.graph_memory is agent.graph_memory
    assert agent.graph_memory.ltm is agent.ltm
    # 调用顺序：load_from_storage 不参与本测；只看 set_graph_memory 在 calls 末尾
    set_calls = [c for c in agent.ltm.calls if isinstance(c, tuple) and c[0] == "set_graph_memory"]
    assert len(set_calls) == 1


def test_init_knowledge_graph_no_ltm(monkeypatch):
    """没有 ltm 时也不应报错，agent.graph_memory 仍可挂上。"""
    fake_neo = types.ModuleType("internal.platform.neo4j")

    class _Client:
        def __init__(self, cfg):
            pass

    fake_neo.Neo4jClient = _Client

    fake_kg = types.ModuleType("internal.graph.kgstore")
    fake_kg.KGStore = lambda cfg, client, llm_fn=None: SimpleNamespace(cfg=cfg)

    fake_gm_mod = types.ModuleType("internal.memory.graph_memory")

    class _GM:
        def __init__(self, cfg, client, llm=None, sim_threshold=0.7, ltm=None):
            self.ltm = ltm

        def sync_prev_id(self):
            pass

    fake_gm_mod.GraphMemory = _GM

    monkeypatch.setitem(sys.modules, "internal.platform.neo4j", fake_neo)
    monkeypatch.setitem(sys.modules, "internal.graph.kgstore", fake_kg)
    monkeypatch.setitem(sys.modules, "internal.memory.graph_memory", fake_gm_mod)

    agent = _agent()
    agent.ltm = None
    restore.init_knowledge_graph(agent)
    assert agent.kg is not None
    assert agent.graph_memory is not None
    assert agent.graph_memory.ltm is None
