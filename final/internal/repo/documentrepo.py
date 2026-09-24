# documentrepo - PostgreSQL-backed document library repository.
import json
from typing import Any, Dict, List, Optional, Tuple

from internal.document.library import (
    DOCUMENT_STATUS_ACTIVE,
    Document,
    DocumentVersion,
    WriteRequest,
    WriteResult,
    new_id,
    normalize_write_request,
)


class Store:
    """文档库 PostgreSQL 实现。

    文档库必须落真实数据库；未配置 PG 时抛错，避免上传/工具链出现假成功。
    """

    def __init__(self, pg):
        self.pg = pg

    def _require_conn(self):
        if self.pg is None or not self.pg.is_real() or self.pg.conn is None:
            raise RuntimeError("document library not configured: postgres not connected")
        return self.pg.conn

    def write(self, req: WriteRequest) -> WriteResult:
        req = normalize_write_request(req)
        if not req.title:
            raise ValueError("title is required")
        if not req.content_md:
            raise ValueError("content_md is required")

        conn = self._require_conn()
        created = not bool(req.document_id)
        doc_id = req.document_id or new_id("doc")
        version = 1

        try:
            with conn.cursor() as cur:
                if created:
                    cur.execute(
                        """
                        INSERT INTO documents
                            (id, title, doc_type, source, status, created_by, created_at, updated_at)
                        VALUES (%s, %s, %s, %s, %s, %s, NOW(), NOW())
                        """,
                        (
                            doc_id,
                            req.title,
                            req.doc_type,
                            req.source,
                            DOCUMENT_STATUS_ACTIVE,
                            req.created_by,
                        ),
                    )
                else:
                    cur.execute(
                        "SELECT COALESCE(MAX(version), 0) + 1 FROM document_versions WHERE document_id = %s",
                        (doc_id,),
                    )
                    row = cur.fetchone()
                    version = int(row[0]) if row else 1
                    cur.execute(
                        """
                        UPDATE documents
                           SET title = %s,
                               doc_type = %s,
                               source = %s,
                               status = %s,
                               updated_at = NOW()
                         WHERE id = %s
                        """,
                        (
                            req.title,
                            req.doc_type,
                            req.source,
                            DOCUMENT_STATUS_ACTIVE,
                            doc_id,
                        ),
                    )

                version_id = new_id("ver")
                cur.execute(
                    """
                    INSERT INTO document_versions
                        (id, document_id, version, content_md, summary, metadata, created_at)
                    VALUES (%s, %s, %s, %s, NULLIF(%s, ''), CAST(%s AS JSONB), NOW())
                    """,
                    (
                        version_id,
                        doc_id,
                        version,
                        req.content_md,
                        req.summary,
                        json.dumps(req.metadata or {}, ensure_ascii=False),
                    ),
                )
            conn.commit()
        except Exception:
            try:
                conn.rollback()
            except Exception:
                pass
            raise

        doc, ver = self.get(doc_id)
        return WriteResult(document=doc, version=ver, created=created)

    def list(self) -> List[Document]:
        conn = self._require_conn()
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT d.id, d.title, d.doc_type, d.source, d.status, d.created_by, d.created_at, d.updated_at,
                       COALESCE(v.version, 0), COALESCE(v.id, '')
                  FROM documents d
                  LEFT JOIN LATERAL (
                    SELECT id, version
                      FROM document_versions
                     WHERE document_id = d.id
                     ORDER BY version DESC
                     LIMIT 1
                  ) v ON true
                 WHERE d.status <> 'deleted'
                 ORDER BY d.updated_at DESC
                """
            )
            rows = cur.fetchall()
        return [self._document_from_row(row) for row in rows]

    def get(self, document_id: str) -> Tuple[Document, DocumentVersion]:
        document_id = (document_id or "").strip()
        if not document_id:
            raise ValueError("document_id is required")
        conn = self._require_conn()
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT d.id, d.title, d.doc_type, d.source, d.status, d.created_by, d.created_at, d.updated_at,
                       COALESCE(v.version, 0), COALESCE(v.id, '')
                  FROM documents d
                  LEFT JOIN LATERAL (
                    SELECT id, version
                      FROM document_versions
                     WHERE document_id = d.id
                     ORDER BY version DESC
                     LIMIT 1
                  ) v ON true
                 WHERE d.id = %s
                """,
                (document_id,),
            )
            row = cur.fetchone()
        if not row:
            raise LookupError(f"document not found: {document_id}")
        doc = self._document_from_row(row)
        ver = self.get_version(doc.latest_version_id)
        return doc, ver

    def get_version(self, version_id: str) -> DocumentVersion:
        version_id = (version_id or "").strip()
        if not version_id:
            raise ValueError("version_id is required")
        conn = self._require_conn()
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT id, document_id, version, content_md, COALESCE(summary, ''),
                       COALESCE(metadata, '{}'::jsonb), created_at
                  FROM document_versions
                 WHERE id = %s
                """,
                (version_id,),
            )
            row = cur.fetchone()
        if not row:
            raise LookupError(f"version not found: {version_id}")
        return self._version_from_row(row)

    @staticmethod
    def _document_from_row(row) -> Document:
        return Document(
            id=row[0] or "",
            title=row[1] or "",
            doc_type=row[2] or "",
            source=row[3] or "",
            status=row[4] or "",
            created_by=row[5] or "",
            created_at=row[6],
            updated_at=row[7],
            latest_version=int(row[8] or 0),
            latest_version_id=row[9] or "",
        )

    @staticmethod
    def _version_from_row(row) -> DocumentVersion:
        metadata = _decode_metadata(row[5])
        return DocumentVersion(
            id=row[0] or "",
            document_id=row[1] or "",
            version=int(row[2] or 0),
            content_md=row[3] or "",
            summary=row[4] or "",
            metadata=metadata,
            created_at=row[6],
        )


def _decode_metadata(raw: Any) -> Dict[str, Any]:
    if raw is None:
        return {}
    if isinstance(raw, dict):
        return raw
    if isinstance(raw, (bytes, bytearray)):
        raw = bytes(raw).decode("utf-8", errors="ignore")
    if isinstance(raw, str):
        try:
            value = json.loads(raw)
            return value if isinstance(value, dict) else {}
        except Exception:
            return {}
    return {}
