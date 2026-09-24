from internal.document.library import (
    DOCUMENT_SOURCE_AGENT,
    DOCUMENT_STATUS_ACTIVE,
    WriteRequest,
    new_id,
    normalize_write_request,
)


def test_normalize_write_request_defaults():
    req = normalize_write_request(WriteRequest(title="  报告  ", content_md="  # 内容  "))

    assert req.title == "报告"
    assert req.content_md == "# 内容"
    assert req.doc_type == "note"
    assert req.source == DOCUMENT_SOURCE_AGENT
    assert req.created_by == "agent"
    assert req.metadata == {}


def test_new_id_uses_prefix():
    assert new_id("doc").startswith("doc_")
    assert new_id("ver").startswith("ver_")
    assert DOCUMENT_STATUS_ACTIVE == "active"
