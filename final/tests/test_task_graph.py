import pytest

from internal.graph.task_graph import (
    Node,
    NodeStatus,
    NodeType,
    TaskGraph,
)


def test_topological_levels_group_independent_nodes():
    graph = TaskGraph([
        Node(id="n1", type=NodeType.TOOL, tool_name="search"),
        Node(id="n2", type=NodeType.TOOL, tool_name="rag"),
        Node(id="n3", type=NodeType.TOOL, tool_name="summarize", depends_on=["n1", "n2"]),
    ])

    assert graph.topological_levels() == [["n1", "n2"], ["n3"]]


def test_node_type_includes_main_alignment_values():
    assert NodeType.THINK.value == "think"
    assert NodeType.AGGREGATE.value == "aggregate"


def test_validate_rejects_missing_dependency():
    graph = TaskGraph([
        Node(id="n1", type=NodeType.TOOL, tool_name="search", depends_on=["missing"]),
    ])

    with pytest.raises(ValueError, match="missing"):
        graph.validate()


def test_validate_rejects_cycle():
    graph = TaskGraph([
        Node(id="n1", type=NodeType.TOOL, tool_name="a", depends_on=["n2"]),
        Node(id="n2", type=NodeType.TOOL, tool_name="b", depends_on=["n1"]),
    ])

    with pytest.raises(ValueError, match="cycle|环"):
        graph.topological_levels()


def test_race_groups_and_node_mutations():
    graph = TaskGraph([
        Node(id="n1", type=NodeType.TOOL, tool_name="search_web", race_group="search"),
        Node(id="n2", type=NodeType.TOOL, tool_name="rag_search", race_group="search"),
        Node(id="n3", type=NodeType.TOOL, tool_name="exec"),
    ])

    assert graph.race_groups() == {"search": ["n1", "n2"]}

    graph.set_node_status("n1", NodeStatus.RUNNING)
    graph.set_node_result("n1", "ok")
    graph.set_node_error("n2", "failed")
    graph.set_node_retry_count("n2", 2)

    assert graph.nodes["n1"].status == NodeStatus.DONE
    assert graph.nodes["n1"].result == "ok"
    assert graph.nodes["n2"].status == NodeStatus.FAILED
    assert graph.nodes["n2"].error == "failed"
    assert graph.nodes["n2"].retry_count == 2
    assert graph.successful_results() == ["ok"]
