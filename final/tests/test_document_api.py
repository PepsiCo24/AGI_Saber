import asyncio
import json
from types import SimpleNamespace

from config.config import APIConfig
from internal.document.library import Document, DocumentVersion
from internal.handler.handler import setup_routes


def test_documents_api_lists_writes_reads_and_ingests():
    agent = DocumentAPIAgent()
    app = _client(agent)

    status, payload = _request(app, "GET", "/api/documents")
    assert status == 200
    listed = json.loads(payload)["documents"][0]
    assert listed["id"] == "doc_1"
    assert listed["latest_metadata"]["text_chars"] == 128
    assert listed["latest_parser"] == "plain_text"

    status, payload = _request(
        app,
        "POST",
        "/api/documents",
        json.dumps(
            {
                "title": "新报告",
                "content_md": "# 正文",
                "doc_type": "report",
                "ingest_to_rag": True,
            }
        ).encode(),
    )
    assert status == 200
    written = json.loads(payload)
    assert agent.written["title"] == "新报告"
    assert written["document"]["id"] == "doc_1"
    assert written["ingest"]["chunk_count"] == 2

    status, payload = _request(app, "GET", "/api/documents/doc_1")
    assert status == 200
    assert json.loads(payload)["version"]["id"] == "ver_1"

    status, payload = _request(
        app,
        "POST",
        "/api/documents/doc_1/ingest",
        json.dumps({"version_id": "ver_1"}).encode(),
    )
    assert status == 200
    assert json.loads(payload)["version_id"] == "ver_1"


def test_upload_returns_parser_metadata_and_document_fields():
    agent = DocumentAPIAgent()
    app = _client(agent)

    status, payload = _request(app, "POST", "/api/upload", json.dumps({"content": "hello rag"}).encode())

    assert status == 200
    data = json.loads(payload)
    assert data["filename"] == "upload.txt"
    assert data["content_type"] == "text/plain"
    assert data["parser"] == "plain_text"
    assert data["text_chars"] > 0
    assert data["needs_ocr"] is False
    assert data["chunk_count"] == 2
    assert data["doc_hash"]
    assert data["document"]["id"] == "doc_1"
    assert data["version"]["id"] == "ver_1"


def test_upload_strips_nul_before_writing_document():
    agent = DocumentAPIAgent()
    app = _client(agent)

    status, _payload = _request(
        app,
        "POST",
        "/api/upload",
        json.dumps({"content": "简历\x00内容"}).encode(),
    )

    assert status == 200
    assert "\x00" not in agent.written["content_md"]
    assert "简历" in agent.written["content_md"]


class DocumentAPIAgent:
    def __init__(self):
        self.doc = Document(
            id="doc_1",
            title="报告",
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
            content_md="# 正文",
            metadata={"filename": "报告.md", "parser": "plain_text", "text_chars": 128},
        )
        self.written = {}

    def list_documents(self):
        return [self.doc]

    def write_document(self, req, ingest_to_rag=False):
        self.written = {
            "title": req.title,
            "content_md": req.content_md,
            "ingest_to_rag": ingest_to_rag,
        }
        result = {"document": self.doc, "version": self.version, "created": True}
        if ingest_to_rag:
            result["ingest"] = self.rag_ingest(req.content_md, self.doc.id, self.version.id)
        return result

    def get_document(self, document_id):
        assert document_id == "doc_1"
        return {"document": self.doc, "version": self.version}

    def ingest_document(self, document_id, version_id=""):
        return self.rag_ingest("# 正文", document_id, version_id or "ver_1")

    def rag_ingest(self, document, document_id="", version_id=""):
        return 2


class _SnapshotRepo:
    def list(self, limit=50):
        return []


class _DocumentRepo:
    def __init__(self, agent):
        self.agent = agent

    def get_version(self, version_id):
        assert version_id == "ver_1"
        return self.agent.version


class _Infra:
    ready = SimpleNamespace(
        milvus="connected",
        postgresql="connected",
        elasticsearch="connected",
        kafka="disconnected",
    )

    def __init__(self, agent):
        self.repo = SimpleNamespace(snapshot=_SnapshotRepo(), documents=_DocumentRepo(agent))


def _client(agent):
    cfg = APIConfig()
    cfg.llm_model = "llm"
    cfg.embedding_model = "embedding"
    return setup_routes(agent, _Infra(agent), cfg)


def _request(app, method, path, body=b"", content_type="application/json"):
    async def _run():
        sent = False
        messages = []
        scope = {
            "type": "http",
            "asgi": {"version": "3.0"},
            "http_version": "1.1",
            "method": method,
            "path": path,
            "raw_path": path.encode(),
            "query_string": b"",
            "headers": [(b"content-type", content_type.encode())],
            "client": ("test", 1),
            "server": ("testserver", 80),
            "scheme": "http",
        }

        async def receive():
            nonlocal sent
            if sent:
                return {"type": "http.disconnect"}
            sent = True
            return {"type": "http.request", "body": body, "more_body": False}

        async def send(message):
            messages.append(message)

        await app(scope, receive, send)
        status = next(m["status"] for m in messages if m["type"] == "http.response.start")
        payload = b"".join(m.get("body", b"") for m in messages if m["type"] == "http.response.body")
        return status, payload

    return asyncio.run(_run())
