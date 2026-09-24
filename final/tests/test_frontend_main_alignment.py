import asyncio
import json
import threading
from pathlib import Path
from types import SimpleNamespace

from config.config import APIConfig
from internal.agent.agent import Response
from internal.handler.handler import setup_routes
from internal.tools.tools import Tool


class _Agent:
    def __init__(self):
        self.cancelled = False
        self.uploaded = ""
        self.deleted = ""
        self.tool = Tool(
            name="rag_search",
            description="从私人黑洞检索",
            params=[{"name": "query", "type": "string", "description": "问题"}],
            func=lambda _args: "ok",
        )

    def process_with_options(self, message, _opts):
        return Response(query=message, answer="回答:" + message, mode="chat")

    def cancel(self):
        self.cancelled = True

    def rag_ingest(self, document):
        self.uploaded = document
        return 3, "fake-doc-hash"

    def get_tools(self):
        return [
            {
                "name": self.tool.name,
                "description": self.tool.description,
                "params": self.tool.params,
                "is_mcp": False,
            }
        ]

    def rag_query(self, question):
        return "知识库回答:" + question, [{"content": "片段", "score": 0.8, "source": "test"}]


class _StreamAgent(_Agent):
    def __init__(self):
        super().__init__()
        self.process_with_options_called = False
        self.process_stream_called = False

    def process_with_options(self, message, _opts):
        self.process_with_options_called = True
        return Response(query=message, answer="同步回答不应被拆字", mode="chat")

    def process_stream(self, message, _opts, on_event):
        self.process_stream_called = True
        on_event({"type": "route", "data": {"mode": "chat"}})
        on_event({"type": "token", "data": {"content": "真"}})
        on_event({"type": "token", "data": {"content": "流"}})
        return Response(query=message, answer="真流", mode="chat")


class _BlockingStreamAgent(_Agent):
    def __init__(self):
        super().__init__()
        self.first_token_emitted = threading.Event()
        self.release = threading.Event()

    def process_stream(self, message, _opts, on_event):
        on_event({"type": "route", "data": {"mode": "chat"}})
        on_event({"type": "token", "data": {"content": "first"}})
        self.first_token_emitted.set()
        self.release.wait(timeout=2)
        on_event({"type": "token", "data": {"content": "second"}})
        return Response(query=message, answer="firstsecond", mode="chat")


class _SnapshotRepo:
    def list(self, limit=50):
        return []


class _RagchunkRepo:
    def __init__(self, infra):
        self.infra = infra

    def delete_by_doc_hash(self, doc_hash):
        self.infra.deleted = doc_hash
        return [1]


class _Infra:
    ready = SimpleNamespace(
        milvus="connected",
        postgresql="connected",
        elasticsearch="connected",
        kafka="disconnected",
    )

    def __init__(self):
        self.deleted = ""
        self.repo = SimpleNamespace(
            snapshot=_SnapshotRepo(),
            ragchunk=_RagchunkRepo(self),
        )


def _client():
    cfg = APIConfig()
    cfg.llm_model = "llm"
    cfg.embedding_model = "embedding"
    return setup_routes(_Agent(), _Infra(), cfg)


def _client_for(agent):
    cfg = APIConfig()
    cfg.llm_model = "llm"
    cfg.embedding_model = "embedding"
    return setup_routes(agent, _Infra(), cfg)


class _LegacyUploadAgent(_Agent):
    def rag_ingest(self, document):
        self.uploaded = document
        return 3


def _legacy_upload_client():
    cfg = APIConfig()
    cfg.llm_model = "llm"
    cfg.embedding_model = "embedding"
    return setup_routes(_LegacyUploadAgent(), _Infra(), cfg)


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
            if not sent:
                sent = True
                return {"type": "http.request", "body": body, "more_body": False}
            await asyncio.Event().wait()

        async def send(message):
            messages.append(message)

        await app(scope, receive, send)
        status = next(m["status"] for m in messages if m["type"] == "http.response.start")
        payload = b"".join(m.get("body", b"") for m in messages if m["type"] == "http.response.body")
        return status, payload

    return asyncio.run(_run())


def test_tools_endpoint_matches_main_frontend_contract():
    status, payload = _request(_client(), "GET", "/api/tools")

    assert status == 200
    data = json.loads(payload)
    assert isinstance(data, list)
    assert data[0]["name"] == "rag_search"
    assert data[0]["params"][0]["name"] == "query"


def test_upload_accepts_main_frontend_json_payload():
    status, payload = _request(_client(), "POST", "/api/upload", json.dumps({"content": "hello rag"}).encode())

    assert status == 200
    data = json.loads(payload)
    assert data["chunk_count"] == 3
    assert data["doc_hash"]


def test_upload_accepts_legacy_int_rag_ingest_result():
    status, payload = _request(
        _legacy_upload_client(),
        "POST",
        "/api/upload",
        json.dumps({"content": "hello rag"}).encode(),
    )

    assert status == 200
    data = json.loads(payload)
    assert data["chunk_count"] == 3
    assert data["doc_hash"]


def test_chat_stream_emits_sse_events_for_main_frontend():
    status, payload = _request(_client(), "POST", "/api/chat/stream", json.dumps({"message": "你好"}).encode())
    body = payload.decode("utf-8")

    assert status == 200
    assert "event: start" in body
    assert "event: route" in body
    assert "event: token" in body
    assert "event: done" in body


def test_chat_stream_uses_agent_stream_callback_instead_of_splitting_answer():
    agent = _StreamAgent()
    status, payload = _request(_client_for(agent), "POST", "/api/chat/stream", json.dumps({"message": "你好"}).encode())
    body = payload.decode("utf-8")

    assert status == 200
    assert agent.process_stream_called is True
    assert agent.process_with_options_called is False
    assert 'data: {"content": "真"}' in body
    assert 'data: {"content": "流"}' in body
    assert "同步回答不应被拆字" not in body


def test_chat_stream_flushes_agent_events_before_stream_finishes():
    agent = _BlockingStreamAgent()
    app = _client_for(agent)
    messages = []

    async def _run():
        sent = False
        scope = {
            "type": "http",
            "asgi": {"version": "3.0"},
            "http_version": "1.1",
            "method": "POST",
            "path": "/api/chat/stream",
            "raw_path": b"/api/chat/stream",
            "query_string": b"",
            "headers": [(b"content-type", b"application/json")],
            "client": ("test", 1),
            "server": ("testserver", 80),
            "scheme": "http",
        }

        async def receive():
            nonlocal sent
            if not sent:
                sent = True
                return {"type": "http.request", "body": json.dumps({"message": "hi"}).encode(), "more_body": False}
            await asyncio.Event().wait()

        async def send(message):
            messages.append(message)

        await app(scope, receive, send)

    error = []

    def _serve():
        try:
            asyncio.run(_run())
        except BaseException as exc:
            error.append(exc)

    thread = threading.Thread(target=_serve, daemon=True)
    thread.start()
    try:
        assert agent.first_token_emitted.wait(timeout=1)
        for _ in range(20):
            if any(b"first" in m.get("body", b"") for m in messages if m["type"] == "http.response.body"):
                break
            threading.Event().wait(0.01)
        else:
            raise AssertionError("first token was not flushed before process_stream returned")
    finally:
        agent.release.set()
        thread.join(timeout=2)

    assert not thread.is_alive()
    assert error == []


def test_legacy_rag_query_route_removed_to_match_main_branch():
    status, _payload = _request(
        _client(),
        "POST",
        "/api/rag/query",
        json.dumps({"question": "old route"}).encode(),
    )

    assert status in {404, 405}


def test_frontend_contains_local_document_library_ui():
    html = Path("frontend/index.html").read_text(encoding="utf-8")

    assert "libraryDocList" in html
    assert "docViewer" in html
    assert "loadLibraryDocs" in html
    assert "fetchDocumentJSON('/api/documents')" in html
    assert "/api/documents/' + encodeURIComponent(id) + '/ingest" in html
    assert "function escAttr" in html


def test_frontend_refreshes_library_after_document_tool_events():
    html = Path("frontend/index.html").read_text(encoding="utf-8")

    assert "function maybeRefreshLibraryAfterTool" in html
    assert "write_document" in html
    assert "ingest_document" in html
    assert "maybeRefreshLibraryAfterTool(data.tool || data.tool_name)" in html
    assert "maybeRefreshLibraryAfterTool(data.tool || data.tool_name || (data.task && data.task.tool_name))" in html


def test_frontend_restores_upload_list_from_document_library():
    html = Path("frontend/index.html").read_text(encoding="utf-8")

    assert "localStorage.removeItem('ai_docs')" not in html
    assert "syncUploadedDocsFromLibrary" in html
    assert "d.source === 'user_upload'" in html


def test_frontend_does_not_show_document_version_as_chunk_count():
    html = Path("frontend/index.html").read_text(encoding="utf-8")

    assert "chunks: Number(d.latest_version || 0)" not in html
    assert "indexed: Number(d.latest_version || 0)" not in html
    assert "d.persisted" in html
    assert "v${d.version || 0}" in html


def test_frontend_overwrites_stale_upload_cache_from_document_library():
    html = Path("frontend/index.html").read_text(encoding="utf-8")

    assert "const uploadKey" in html
    assert "const existingKey" in html
    assert "byKey.set(uploadKey, d)" in html
    assert "byName.has(d.name)" not in html


def test_frontend_uses_completion_badges_instead_of_interrupted_for_successful_done():
    html = Path("frontend/index.html").read_text(encoding="utf-8")

    assert "function markStreamCompleted" in html
    assert "推理完成" in html
    assert "工具调用完成" in html
    assert "检索完成" in html
    assert "本次回复已取消" in html
    assert "✦ 已中断" not in html
