import pytest

from internal.document.library import WriteRequest
from internal.repo.documentrepo import Store


def test_document_repo_requires_pg():
    store = Store(None)

    with pytest.raises(RuntimeError, match="document library not configured"):
        store.list()


def test_document_repo_write_validates_content():
    class PG:
        conn = object()

        def is_real(self):
            return True

    store = Store(PG())

    with pytest.raises(ValueError, match="title is required"):
        store.write(WriteRequest(content_md="body"))


def test_document_repo_write_and_get_use_postgres_transaction():
    pg = RecordingPG()
    store = Store(pg)

    result = store.write(
        WriteRequest(
            title="研究报告",
            content_md="# 内容",
            summary="摘要",
            metadata={"source_file": "report.md"},
        )
    )

    assert result.created is True
    assert result.document.id.startswith("doc_")
    assert result.document.latest_version == 1
    assert result.version.id.startswith("ver_")
    assert result.version.content_md == "# 内容"
    assert result.version.metadata == {"source_file": "report.md"}
    assert pg.conn.commits == 1
    assert any("INSERT INTO documents" in sql for sql, _ in pg.executed)
    assert any("INSERT INTO document_versions" in sql for sql, _ in pg.executed)
    assert any("LEFT JOIN LATERAL" in sql for sql, _ in pg.executed)


def test_document_repo_update_increments_version():
    pg = RecordingPG(existing_document_id="doc_existing")
    store = Store(pg)

    result = store.write(
        WriteRequest(
            document_id="doc_existing",
            title="更新报告",
            content_md="v2",
        )
    )

    assert result.created is False
    assert result.document.id == "doc_existing"
    assert result.document.latest_version == 2
    assert result.version.version == 2
    assert any("COALESCE(MAX(version), 0) + 1" in sql for sql, _ in pg.executed)
    assert any("UPDATE documents" in sql for sql, _ in pg.executed)


class RecordingPG:
    def __init__(self, existing_document_id=""):
        self.conn = RecordingConn(existing_document_id)
        self.executed = self.conn.executed
        self.commits = 0

    def is_real(self):
        return True


class RecordingConn:
    def __init__(self, existing_document_id=""):
        self.existing_document_id = existing_document_id
        self.executed = []
        self.commits = 0
        self.document = None
        self.version = None
        if existing_document_id:
            self.document = {
                "id": existing_document_id,
                "title": "旧报告",
                "doc_type": "note",
                "source": "agent_generated",
                "status": "active",
                "created_by": "agent",
                "created_at": "2026-06-17T00:00:00+00:00",
                "updated_at": "2026-06-17T00:00:00+00:00",
            }

    def cursor(self):
        return RecordingCursor(self)

    def commit(self):
        self.commits += 1

    def rollback(self):
        pass


class RecordingCursor:
    def __init__(self, conn):
        self.conn = conn
        self._row = None

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, tb):
        return False

    def execute(self, sql, params=()):
        self.conn.executed.append((sql, params))
        compact = " ".join(sql.split())
        if compact.startswith("INSERT INTO documents"):
            doc_id, title, doc_type, source, status, created_by = params
            self.conn.document = {
                "id": doc_id,
                "title": title,
                "doc_type": doc_type,
                "source": source,
                "status": status,
                "created_by": created_by,
                "created_at": "2026-06-17T00:00:00+00:00",
                "updated_at": "2026-06-17T00:00:00+00:00",
            }
            self._row = None
            return
        if compact.startswith("SELECT COALESCE(MAX(version), 0) + 1"):
            self._row = (2,)
            return
        if compact.startswith("UPDATE documents"):
            title, doc_type, source, status, doc_id = params
            self.conn.document.update(
                {
                    "id": doc_id,
                    "title": title,
                    "doc_type": doc_type,
                    "source": source,
                    "status": status,
                    "updated_at": "2026-06-17T00:00:01+00:00",
                }
            )
            self._row = None
            return
        if compact.startswith("INSERT INTO document_versions"):
            version_id, doc_id, version, content_md, summary, metadata = params
            self.conn.version = {
                "id": version_id,
                "document_id": doc_id,
                "version": version,
                "content_md": content_md,
                "summary": summary,
                "metadata": metadata,
                "created_at": "2026-06-17T00:00:00+00:00",
            }
            self._row = None
            return
        if "FROM documents d LEFT JOIN LATERAL" in compact:
            d = self.conn.document
            v = self.conn.version
            self._row = (
                d["id"],
                d["title"],
                d["doc_type"],
                d["source"],
                d["status"],
                d["created_by"],
                d["created_at"],
                d["updated_at"],
                v["version"],
                v["id"],
            )
            return
        if compact.startswith("SELECT id, document_id, version"):
            v = self.conn.version
            self._row = (
                v["id"],
                v["document_id"],
                v["version"],
                v["content_md"],
                v["summary"],
                v["metadata"],
                v["created_at"],
            )
            return
        raise AssertionError(f"unexpected SQL: {sql}")

    def fetchone(self):
        return self._row

    def fetchall(self):
        return []
