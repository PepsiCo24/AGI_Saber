"""GraphMemory.add_to_graph 异步化 + panic recover 单元测试（Task 16）。

验证：
1) add_to_graph 不在主线程内执行 cypher，而是 daemon 线程；
2) cypher 异常被 _go_safe 吞下，不冒泡到 caller。
"""
import threading
import time
from types import SimpleNamespace
from typing import List

from internal.memory.graph_memory import GraphMemory
from internal.memory.memory import Item


class _RecordingNeo:
    def __init__(self, raise_on_first: bool = False):
        self.calls: List[tuple] = []
        self.thread_names: List[str] = []
        self._raise = raise_on_first
        self._lock = threading.Lock()
        self._done = threading.Event()

    def is_real(self) -> bool:
        return True

    def run_cypher(self, query, params):
        with self._lock:
            self.calls.append((query, params))
            self.thread_names.append(threading.current_thread().name)
            if self._raise:
                self._raise = False
                self._done.set()
                raise RuntimeError("simulated neo4j failure")
            self._done.set()
        return []


class _Cfg:
    kg_max_hops = 2


def test_add_to_graph_runs_cypher_in_background_thread():
    neo = _RecordingNeo()
    gm = GraphMemory(_Cfg(), neo)
    item = Item(content="x", importance=0.5, embedding=[1.0, 0.0], id=1)

    main_thread = threading.current_thread().name
    mid = gm.add_to_graph(item)

    assert mid == 1
    assert gm.prev_id == 1  # 主线程立即可见
    # 等后台线程完成
    assert neo._done.wait(timeout=2.0), "background thread did not run"
    # 给 thread 足够时间走完
    time.sleep(0.05)
    assert neo.calls, "no cypher executed"
    assert all(name != main_thread for name in neo.thread_names), (
        f"cypher ran on main thread: {neo.thread_names}"
    )


def test_add_to_graph_swallows_panic():
    """cypher 抛异常时不应冒泡，主线程继续；后续 add_to_graph 仍能正常工作。"""
    neo = _RecordingNeo(raise_on_first=True)
    gm = GraphMemory(_Cfg(), neo)
    item1 = Item(content="boom", importance=0.5, embedding=[1.0, 0.0], id=1)

    # 不应抛
    gm.add_to_graph(item1)
    assert neo._done.wait(timeout=2.0)
    time.sleep(0.05)

    neo._done.clear()
    item2 = Item(content="ok", importance=0.5, embedding=[1.0, 0.0], id=2)
    gm.add_to_graph(item2)
    assert neo._done.wait(timeout=2.0)
    time.sleep(0.05)
    assert any("boom" in str(c[1]) or c[1].get("content") == "boom" for c in neo.calls)
