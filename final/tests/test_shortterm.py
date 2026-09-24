"""ShortTerm 滑动窗口 + timestamp + 并发安全的单元测试。

对齐 main 分支：
- 每条消息带 timestamp（"HH:MM:SS"）
- 超过 max_turns*2 自动淘汰最早消息（deque maxlen）
- 多线程并发 add/get/count 不抛异常、不丢一致性
"""
import re
import threading

from internal.memory.memory import ShortTerm


_TS_RE = re.compile(r"^\d{2}:\d{2}:\d{2}$")


def test_add_attaches_timestamp():
    stm = ShortTerm(max_turns=3)
    stm.add("user", "hi")
    msgs = stm.get()
    assert len(msgs) == 1
    assert msgs[0]["role"] == "user"
    assert msgs[0]["content"] == "hi"
    assert "timestamp" in msgs[0]
    assert _TS_RE.match(msgs[0]["timestamp"])


def test_sliding_window_evicts_oldest():
    stm = ShortTerm(max_turns=2)  # cap = 2 * 2 = 4
    stm.add("user", "u1")
    stm.add("assistant", "a1")
    stm.add("user", "u2")
    stm.add("assistant", "a2")
    assert stm.count() == 4

    # 第 5 条会把 u1 顶掉
    stm.add("user", "u3")
    msgs = stm.get()
    assert stm.count() == 4
    assert [m["content"] for m in msgs] == ["a1", "u2", "a2", "u3"]


def test_get_returns_independent_copy():
    stm = ShortTerm(max_turns=2)
    stm.add("user", "x")
    snap = stm.get()
    snap[0]["content"] = "MUTATED"
    assert stm.get()[0]["content"] == "x"


def test_clear_empties_window():
    stm = ShortTerm(max_turns=2)
    stm.add("user", "a")
    stm.add("assistant", "b")
    stm.clear()
    assert stm.count() == 0
    assert stm.get() == []


def test_concurrent_add_and_read_is_safe():
    """多线程并发 add/get/count 不应抛异常或破坏内部状态。"""
    stm = ShortTerm(max_turns=50)  # cap = 100
    errors: list = []

    def writer(idx: int):
        try:
            for i in range(200):
                stm.add("user" if i % 2 == 0 else "assistant", f"w{idx}-{i}")
        except Exception as e:
            errors.append(e)

    def reader():
        try:
            for _ in range(200):
                _ = stm.count()
                _ = stm.get()
        except Exception as e:
            errors.append(e)

    threads = [threading.Thread(target=writer, args=(i,)) for i in range(4)]
    threads += [threading.Thread(target=reader) for _ in range(2)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    assert errors == []
    assert stm.count() == 100  # 满载（cap = max_turns*2）
    msgs = stm.get()
    assert len(msgs) == 100
    # 每条都应该带合法字段
    for m in msgs:
        assert m["role"] in ("user", "assistant")
        assert _TS_RE.match(m["timestamp"])
