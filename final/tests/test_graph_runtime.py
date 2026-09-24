import time
from types import SimpleNamespace

from internal.agent.cancel import CancelToken
from internal.agent.graph_runtime import GraphConfig, GraphRuntime
from internal.agent.planner import llm_plan_graph, rule_plan_nodes
from internal.graph.task_graph import Node, NodeStatus, NodeType, TaskGraph
from internal.promptctx import TaskMemBuffer, ToolCallTrace, ToolStateTracker


class _Tool:
    def __init__(self, func):
        self.func = func
        self.description = "fake"
        self.params = []


class _Agent:
    def __init__(self):
        self.cfg = SimpleNamespace(max_retries=2, retry_delay_ms=1)
        self.snapshots = []
        self.task_mem = TaskMemBuffer()
        self.tool_tracker = ToolStateTracker()

    def save_snapshot(self, task):
        self.snapshots.append(task)

    def push_task_mem(self, obs):
        self.task_mem.push(obs)

    def record_tool_call(self, trace: ToolCallTrace):
        self.tool_tracker.record(trace)


def test_graph_runtime_executes_dependency_levels_and_records_context():
    calls = []
    tools = {
        "a": _Tool(lambda _params: calls.append("a") or "A"),
        "b": _Tool(lambda _params: calls.append("b") or "B"),
        "c": _Tool(lambda _params: calls.append("c") or "C"),
    }
    graph = TaskGraph([
        Node(id="n1", type=NodeType.TOOL, tool_name="a"),
        Node(id="n2", type=NodeType.TOOL, tool_name="b"),
        Node(id="n3", type=NodeType.TOOL, tool_name="c", depends_on=["n1", "n2"]),
    ])
    agent = _Agent()

    result = GraphRuntime(graph, agent, GraphConfig(max_parallel=2), tools, {"task_id": "t1"}).execute(CancelToken())

    assert result.interrupted is False
    assert graph.nodes["n3"].status == NodeStatus.DONE
    assert calls[-1] == "c"
    assert sorted(result.observations) == ["A", "B", "C"]
    assert len(agent.task_mem.snapshot()) == 3
    assert len(agent.tool_tracker.snapshot()) == 3
    assert agent.snapshots


def test_graph_runtime_race_group_first_success_wins():
    def slow(_params):
        time.sleep(0.03)
        return "slow"

    tools = {
        "slow": _Tool(slow),
        "fast": _Tool(lambda _params: "fast"),
    }
    graph = TaskGraph([
        Node(id="n1", type=NodeType.TOOL, tool_name="slow", race_group="search"),
        Node(id="n2", type=NodeType.TOOL, tool_name="fast", race_group="search"),
    ])

    result = GraphRuntime(
        graph,
        _Agent(),
        GraphConfig(max_parallel=2, enable_racing=True),
        tools,
        {"task_id": "t1"},
    ).execute(CancelToken())

    assert result.observations == ["fast"]
    assert graph.nodes["n2"].status == NodeStatus.DONE
    assert graph.nodes["n1"].status in {NodeStatus.SKIPPED, NodeStatus.CANCELLED}


def test_graph_runtime_race_group_success_does_not_cancel_request_token():
    token = CancelToken()
    tools = {
        "slow": _Tool(lambda _params: time.sleep(0.05) or "slow"),
        "fast": _Tool(lambda _params: "fast"),
    }
    graph = TaskGraph([
        Node(id="n1", type=NodeType.TOOL, tool_name="slow", race_group="search"),
        Node(id="n2", type=NodeType.TOOL, tool_name="fast", race_group="search"),
    ])

    GraphRuntime(
        graph,
        _Agent(),
        GraphConfig(max_parallel=2, enable_racing=True),
        tools,
        {"task_id": "t1"},
    ).execute(token)

    assert token.is_cancelled() is False


def test_graph_runtime_retries_and_records_failure():
    attempts = {"count": 0}

    def flaky(_params):
        attempts["count"] += 1
        raise RuntimeError("boom")

    graph = TaskGraph([Node(id="n1", type=NodeType.TOOL, tool_name="flaky")])
    result = GraphRuntime(
        graph,
        _Agent(),
        GraphConfig(max_parallel=1),
        {"flaky": _Tool(flaky)},
        {"task_id": "t1"},
    ).execute(CancelToken())

    assert attempts["count"] == 2
    assert graph.nodes["n1"].status == NodeStatus.FAILED
    assert result.node_results["n1"].error == "boom"


def test_graph_runtime_marks_pending_cancelled_when_cancelled_before_start():
    token = CancelToken()
    token.cancel()
    graph = TaskGraph([Node(id="n1", type=NodeType.TOOL, tool_name="a")])

    result = GraphRuntime(
        graph,
        _Agent(),
        GraphConfig(max_parallel=1),
        {"a": _Tool(lambda _params: "A")},
        {"task_id": "t1"},
    ).execute(token)

    assert result.interrupted is True
    assert graph.nodes["n1"].status == NodeStatus.CANCELLED


def test_rule_plan_nodes_outputs_graph_nodes_with_race_groups():
    tools = {
        "search_web": _Tool(lambda _params: "web"),
        "rag_search": _Tool(lambda _params: "rag"),
    }
    agent = SimpleNamespace(cfg=SimpleNamespace(is_real_llm=lambda: False))

    nodes = rule_plan_nodes(agent, "搜索 RAG 是什么", tools)

    assert [node.id for node in nodes] == ["n1", "n2"]
    assert {node.tool_name for node in nodes} == {"search_web", "rag_search"}
    assert all(node.race_group == "search" for node in nodes)


def test_llm_plan_graph_parses_dependencies_and_race_group():
    class _LLM:
        def chat(self, _messages, system_prompt=""):
            return (
                '[{"id":"n1","tool":"search_web","params":{"query":"q"},'
                '"reason":"搜索","depends_on":[],"race_group":"search"},'
                '{"id":"n2","tool":"rag_search","params":{"query":"q"},'
                '"reason":"知识库","depends_on":["n1"],"race_group":""}]'
            )

    agent = SimpleNamespace(cfg=SimpleNamespace(is_real_llm=lambda: True), llm=_LLM())
    nodes = llm_plan_graph(agent, "q", {"search_web": _Tool(lambda p: ""), "rag_search": _Tool(lambda p: "")}, "")

    assert [node.id for node in nodes] == ["n1", "n2"]
    assert nodes[1].depends_on == ["n1"]
    assert nodes[0].race_group == "search"
