import json
from types import SimpleNamespace

from internal.agent.agent import UnifiedAgent
from internal.document.library import Document, DocumentVersion, WriteRequest, WriteResult


def test_agent_registers_document_tools():
    agent = object.__new__(UnifiedAgent)
    tools = []
    agent.tool_executor = SimpleNamespace(add_tool=lambda tool: tools.append(tool))

    agent._register_document_tools()

    assert {t.name for t in tools} >= {
        "write_document",
        "list_documents",
        "read_document",
        "ingest_document",
    }


def test_write_document_tool_calls_real_repository_and_rag():
    store = RecordingDocumentStore()
    agent = object.__new__(UnifiedAgent)
    agent.inf = SimpleNamespace(repo=SimpleNamespace(documents=store))
    agent.rag = SimpleNamespace(ingest=lambda content: 3)

    result_json = agent._write_document_tool().func(
        {
            "title": "分析报告",
            "content_md": "# 内容",
            "doc_type": "report",
            "ingest_to_rag": "true",
        }
    )
    result = json.loads(result_json)

    assert store.writes[0].title == "分析报告"
    assert store.writes[0].content_md == "# 内容"
    assert result["created"] is True
    assert result["document"]["id"] == "doc_1"
    assert result["ingest"]["chunk_count"] == 3
    assert result["ingest"]["document_id"] == "doc_1"
    assert result["ingest"]["version_id"] == "ver_1"


def test_document_tools_list_read_and_ingest_latest_version():
    store = RecordingDocumentStore()
    agent = object.__new__(UnifiedAgent)
    agent.inf = SimpleNamespace(repo=SimpleNamespace(documents=store))
    agent.rag = SimpleNamespace(ingest=lambda content: 2)

    docs = json.loads(agent._list_documents_tool().func({}))
    read = json.loads(agent._read_document_tool().func({"document_id": "doc_1"}))
    ingest = json.loads(agent._ingest_document_tool().func({"document_id": "doc_1"}))

    assert docs["documents"][0]["id"] == "doc_1"
    assert read["version"]["content_md"] == "# 内容"
    assert ingest["chunk_count"] == 2
    assert ingest["section"] == "document"


class RecordingDocumentStore:
    def __init__(self):
        self.doc = Document(
            id="doc_1",
            title="分析报告",
            doc_type="report",
            source="agent_generated",
            status="active",
            created_by="agent",
            latest_version=1,
            latest_version_id="ver_1",
        )
        self.version = DocumentVersion(
            id="ver_1",
            document_id="doc_1",
            version=1,
            content_md="# 内容",
            summary="",
            metadata={},
        )
        self.writes = []

    def write(self, req: WriteRequest):
        self.writes.append(req)
        return WriteResult(document=self.doc, version=self.version, created=True)

    def list(self):
        return [self.doc]

    def get(self, document_id):
        assert document_id == "doc_1"
        return self.doc, self.version

    def get_version(self, version_id):
        assert version_id == "ver_1"
        return self.version
