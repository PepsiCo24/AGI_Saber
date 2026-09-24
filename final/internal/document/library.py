import secrets
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Any, Dict


DOCUMENT_STATUS_ACTIVE = "active"
DOCUMENT_SOURCE_AGENT = "agent_generated"
DOCUMENT_SOURCE_UPLOAD = "user_upload"


def _utc_now() -> datetime:
    return datetime.now(timezone.utc)


@dataclass
class Document:
    id: str = ""
    title: str = ""
    doc_type: str = ""
    source: str = ""
    status: str = DOCUMENT_STATUS_ACTIVE
    created_by: str = ""
    created_at: datetime = field(default_factory=_utc_now)
    updated_at: datetime = field(default_factory=_utc_now)
    latest_version: int = 0
    latest_version_id: str = ""


@dataclass
class DocumentVersion:
    id: str = ""
    document_id: str = ""
    version: int = 0
    content_md: str = ""
    summary: str = ""
    metadata: Dict[str, Any] = field(default_factory=dict)
    created_at: datetime = field(default_factory=_utc_now)


@dataclass
class WriteRequest:
    document_id: str = ""
    title: str = ""
    doc_type: str = ""
    source: str = ""
    created_by: str = ""
    content_md: str = ""
    summary: str = ""
    metadata: Dict[str, Any] = field(default_factory=dict)


@dataclass
class WriteResult:
    document: Document
    version: DocumentVersion
    created: bool = False


def normalize_write_request(req: WriteRequest) -> WriteRequest:
    req.document_id = (req.document_id or "").strip()
    req.title = (req.title or "").strip()
    req.doc_type = (req.doc_type or "").strip() or "note"
    req.source = (req.source or "").strip() or DOCUMENT_SOURCE_AGENT
    req.created_by = (req.created_by or "").strip() or "agent"
    req.content_md = (req.content_md or "").strip()
    req.summary = (req.summary or "").strip()
    if req.metadata is None:
        req.metadata = {}
    return req


def new_id(prefix: str) -> str:
    prefix = (prefix or "id").strip() or "id"
    return f"{prefix}_{secrets.token_hex(8)}"
