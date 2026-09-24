import json
from types import SimpleNamespace

from internal.agent.cancel import CancelToken
from internal.agent.graph_runtime import GraphConfig, GraphRuntime
from internal.agent.planner import rule_plan_nodes
from internal.agent.subagents import SubAgentTask, register_builtin_subagents
from internal.document.library import Document, DocumentVersion, WriteResult
from internal.graph.task_graph import Node, NodeStatus, NodeType, TaskGraph


def test_register_builtin_subagents_includes_document_pipeline():
    agent = SimpleNamespace(cfg=SimpleNamespace(is_real_llm=lambda: False))

    registry = register_builtin_subagents(agent)

    assert set(registry.snapshot()) >= {
        "research_agent",
        "writer_agent",
        "review_agent",
        "doc_agent",
    }


def test_doc_agent_writes_document_with_upstream_writer_output():
    agent = RecordingAgent()
    registry = register_builtin_subagents(agent)

    result = registry.get("doc_agent").run(
        SubAgentTask(
            id="n4",
            goal="保存报告",
            query="生成一份标题为《AGI周报》的报告",
            upstream={"n2_writer_agent": "# AGI周报\n\n正文", "n3_review_agent": "Review ok"},
        )
    )
    data = json.loads(result)

    assert agent.write_calls[0]["req"].title == "AGI周报"
    assert agent.write_calls[0]["req"].content_md == "# AGI周报\n\n正文"
    assert agent.write_calls[0]["req"].metadata["sub_agent"] == "doc_agent"
    assert agent.write_calls[0]["ingest"] is True
    assert data["document"]["id"] == "doc_1"


def test_rule_plan_nodes_generates_subagent_chain_for_document_tasks():
    agent = SimpleNamespace(cfg=SimpleNamespace(is_real_llm=lambda: False))

    nodes = rule_plan_nodes(agent, "调研 Neo4j 并生成报告保存到文档库", {})

    assert [node.type for node in nodes] == [NodeType.SUBAGENT] * 4
    assert [node.tool_name for node in nodes] == [
        "research_agent",
        "writer_agent",
        "review_agent",
        "doc_agent",
    ]
    assert nodes[1].depends_on == ["n1"]
    assert nodes[3].depends_on == ["n2", "n3"]


def test_graph_runtime_executes_subagent_with_upstream_outputs():
    agent = RecordingAgent()
    agent.subagents = register_builtin_subagents(agent)
    graph = TaskGraph(
        [
            Node(id="n1", type=NodeType.SUBAGENT, tool_name="writer_agent", params={"goal": "写报告"}),
            Node(id="n2", type=NodeType.SUBAGENT, tool_name="doc_agent", params={"goal": "保存"}, depends_on=["n1"]),
        ]
    )

    result = GraphRuntime(graph, agent, GraphConfig(max_parallel=1), {}, {"query": "生成标题为《测试报告》的报告"}).execute(CancelToken())

    assert result.interrupted is False
    assert graph.nodes["n2"].status == NodeStatus.DONE
    assert agent.write_calls
    assert "n1" in agent.last_subagent_task.upstream


class RecordingAgent:
    def __init__(self):
        self.cfg = SimpleNamespace(is_real_llm=lambda: False, max_retries=1, retry_delay_ms=0)
        self.write_calls = []
        self.last_subagent_task = None

    def write_document(self, req, ingest_to_rag=False):
        self.write_calls.append({"req": req, "ingest": ingest_to_rag})
        doc = Document(
            id="doc_1",
            title=req.title,
            doc_type=req.doc_type,
            source=req.source,
            status="active",
            created_by=req.created_by,
            latest_version=1,
            latest_version_id="ver_1",
        )
        ver = DocumentVersion(
            id="ver_1",
            document_id="doc_1",
            version=1,
            content_md=req.content_md,
            summary=req.summary,
            metadata=req.metadata,
        )
        return WriteResult(document=doc, version=ver, created=True)

    def _llm_generate(self, _system_prompt, user_msg):
        return user_msg
