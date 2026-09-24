"""bootstrap_concurrent 4 路并发启动测试（Task 28）。

对齐 main bootstrapConcurrent：
- ragchunk.init / restore_from_db / restore_rag_from_db / init_sandbox 并发
- 任意一项失败应被吞没，不影响其它三项
- KG init 必须在并发完成后串行执行（依赖 ltm/sandbox 已就绪）

为了避免与 UnifiedAgent.__init__ 中其它无关初始化耦合，本组用例直接以
``UnifiedAgent.__new__`` 构造空壳实例，挂上最小字段后调用
``_bootstrap_concurrent``——这样测的是并发编排本身，不被 LLM/RAG/Preference
等真实模块拖累。
"""

import threading
import time
from types import SimpleNamespace
from unittest import mock

import pytest

from internal.agent import agent as agent_mod


class _Repo:
    """模拟 inf.repo.ragchunk：暴露 init(dim) 行为。"""

    def __init__(self, init_delay=0.0, init_should_raise=False):
        self.init_called = threading.Event()
        self.init_delay = init_delay
        self.init_should_raise = init_should_raise
        self.init_started_at = None
        self.init_dim = None

    def init(self, dim):
        self.init_started_at = time.time()
        self.init_dim = dim
        time.sleep(self.init_delay)
        self.init_called.set()
        if self.init_should_raise:
            raise RuntimeError("ragchunk.init explode")


def _make_agent(*, ragchunk_repo, dim=1024):
    """绕开 __init__，构造仅带 _bootstrap_concurrent 所需字段的空壳 agent。"""
    agent = agent_mod.UnifiedAgent.__new__(agent_mod.UnifiedAgent)
    cfg = mock.MagicMock()
    cfg.rag_milvus_dim = dim
    agent.cfg = cfg
    agent.inf = SimpleNamespace(repo=SimpleNamespace(ragchunk=ragchunk_repo))
    agent.sandbox = None
    return agent


def _patch_three_paths(monkeypatch, *, restore_delay=0.0, restore_raises=False,
                      restore_rag_raises=False, sandbox_raises=False):
    sandbox_started = threading.Event()
    restore_started = threading.Event()
    restore_rag_started = threading.Event()

    def _restore_from_db(agent):
        restore_started.set()
        time.sleep(restore_delay)
        if restore_raises:
            raise RuntimeError("restore_from_db boom")

    def _restore_rag_from_db(agent):
        restore_rag_started.set()
        if restore_rag_raises:
            raise RuntimeError("restore_rag boom")

    def _init_sandbox(agent):
        sandbox_started.set()
        if sandbox_raises:
            raise RuntimeError("sandbox boom")
        agent.sandbox = "SANDBOX"

    monkeypatch.setattr(agent_mod, "restore_from_db", _restore_from_db)
    monkeypatch.setattr(agent_mod, "restore_rag_from_db", _restore_rag_from_db)
    monkeypatch.setattr(agent_mod, "init_sandbox", _init_sandbox)

    return dict(
        sandbox_started=sandbox_started,
        restore_started=restore_started,
        restore_rag_started=restore_rag_started,
    )


def test_bootstrap_runs_all_four_paths(monkeypatch):
    repo = _Repo()
    evts = _patch_three_paths(monkeypatch)
    agent = _make_agent(ragchunk_repo=repo, dim=2048)

    agent._bootstrap_concurrent()

    assert repo.init_called.is_set()
    assert repo.init_dim == 2048
    assert evts["sandbox_started"].is_set()
    assert evts["restore_started"].is_set()
    assert evts["restore_rag_started"].is_set()
    assert agent.sandbox == "SANDBOX"


def test_bootstrap_isolates_one_failure(monkeypatch):
    repo = _Repo(init_should_raise=True)
    evts = _patch_three_paths(monkeypatch, restore_raises=True)
    agent = _make_agent(ragchunk_repo=repo)

    # 不应抛——所有异常都吞没在子线程内
    agent._bootstrap_concurrent()

    assert evts["sandbox_started"].is_set()
    assert evts["restore_rag_started"].is_set()
    assert agent.sandbox == "SANDBOX"


def test_bootstrap_isolates_sandbox_failure(monkeypatch):
    repo = _Repo()
    evts = _patch_three_paths(monkeypatch, sandbox_raises=True)
    agent = _make_agent(ragchunk_repo=repo)

    agent._bootstrap_concurrent()

    assert repo.init_called.is_set()
    assert evts["restore_started"].is_set()
    assert evts["restore_rag_started"].is_set()
    # sandbox 抛异常时被吞没并保持 None
    assert agent.sandbox is None


def test_bootstrap_runs_paths_concurrently(monkeypatch):
    """ragchunk.init 阻塞 0.3s，restore_from_db 阻塞 0.3s。

    若串行执行总耗时 ~0.6s；并发执行应在 ~0.3s 内完成。
    """
    repo = _Repo(init_delay=0.3)
    _patch_three_paths(monkeypatch, restore_delay=0.3)
    agent = _make_agent(ragchunk_repo=repo)

    t0 = time.time()
    agent._bootstrap_concurrent()
    elapsed = time.time() - t0

    assert elapsed < 0.55, f"bootstrap 串行执行了，elapsed={elapsed:.2f}s"


def test_bootstrap_no_ragchunk_repo(monkeypatch):
    """inf.repo.ragchunk 为 None 时不应抛，仍跑完其它三路。"""
    evts = _patch_three_paths(monkeypatch)
    agent = agent_mod.UnifiedAgent.__new__(agent_mod.UnifiedAgent)
    cfg = mock.MagicMock()
    cfg.rag_milvus_dim = 1024
    agent.cfg = cfg
    agent.inf = SimpleNamespace(repo=SimpleNamespace(ragchunk=None))
    agent.sandbox = None

    agent._bootstrap_concurrent()

    assert evts["sandbox_started"].is_set()
    assert evts["restore_started"].is_set()
    assert evts["restore_rag_started"].is_set()
