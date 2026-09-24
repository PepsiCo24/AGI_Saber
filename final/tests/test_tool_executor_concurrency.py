"""ToolExecutor 并发安全 + snapshot/filter 测试（Task 27）。

对齐 main 分支 toolRegistry：
- 写（add_tool）持锁
- 读（call/snapshot/filter_tools/get_tool_descriptions）走 snapshot 拷贝
- 并发 add + call 不应触发 KeyError / RuntimeError
"""

import threading

from internal.tools.tools import Tool, ToolExecutor


def _tool(name: str, value: str = "ok") -> Tool:
    return Tool(name=name, description=name, params=[], func=lambda _p, v=value: v)


def test_initial_tool_map_consistent_with_tools():
    te = ToolExecutor([_tool("a"), _tool("b")])
    assert set(te._tool_map.keys()) == {"a", "b"}
    assert {t.name for t in te.tools} == {"a", "b"}


def test_add_tool_overwrites_same_name_keeps_unique_in_list():
    te = ToolExecutor([_tool("a", "v1")])
    te.add_tool(_tool("a", "v2"))
    assert te._tool_map["a"].func({}) == "v2"
    # self.tools 不应有两条同名
    assert sum(1 for t in te.tools if t.name == "a") == 1


def test_snapshot_returns_independent_copy():
    te = ToolExecutor([_tool("a")])
    snap = te.snapshot()
    snap["b"] = _tool("b")
    # 外部修改不影响内部
    assert "b" not in te._tool_map


def test_filter_tools_subset_only_with_known_names():
    te = ToolExecutor([_tool("a"), _tool("b"), _tool("c")])
    sub = te.filter_tools(["a", "c", "missing"])
    assert set(sub.keys()) == {"a", "c"}


def test_call_unknown_tool_returns_error_not_raise():
    te = ToolExecutor([_tool("a")])
    res = te.call("nope", {})
    assert res.success is False
    assert "不存在" in (res.error or "")


def test_call_does_not_hold_lock_during_func():
    """工具 func 内部触发 add_tool 不应死锁（验证 RLock 可重入）。"""
    te = ToolExecutor()

    def chained(_p):
        te.add_tool(_tool("inner", "v"))
        return "outer"

    te.add_tool(Tool(name="outer", description="", params=[], func=chained))
    res = te.call("outer", {})
    assert res.success and res.content == "outer"
    assert "inner" in te._tool_map


def test_concurrent_add_and_call_no_race():
    te = ToolExecutor([_tool("base")])
    stop = threading.Event()
    errors = []

    def writer():
        for i in range(200):
            te.add_tool(_tool(f"t{i}", f"v{i}"))

    def reader():
        try:
            while not stop.is_set():
                snap = te.snapshot()
                for name in list(snap.keys())[:5]:
                    te.call(name, {})
        except Exception as e:
            errors.append(e)

    rt = [threading.Thread(target=reader) for _ in range(3)]
    wt = [threading.Thread(target=writer) for _ in range(2)]
    for t in rt + wt:
        t.start()
    for t in wt:
        t.join()
    stop.set()
    for t in rt:
        t.join()
    assert errors == []
    assert len(te._tool_map) >= 200


def test_get_tool_descriptions_uses_snapshot_consistent_view():
    te = ToolExecutor([_tool("a"), _tool("b")])
    desc = te.get_tool_descriptions()
    names = {d["name"] for d in desc}
    assert names == {"a", "b"}
    for d in desc:
        assert "parameters" in d and "is_mcp" in d
