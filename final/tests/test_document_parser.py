import pytest

from internal.document.parser import parse_bytes


def test_parse_plain_text_normalizes_content():
    res = parse_bytes("note.txt", "text/plain", b"hello-\nworld\n\nAGI")

    assert res.filename == "note.txt"
    assert res.content_type == "text/plain"
    assert res.parser == "plain_text"
    assert "helloworld" in res.content
    assert res.text_chars > 0
    assert res.needs_ocr is False


def test_parse_plain_text_removes_nul_characters():
    res = parse_bytes("resume.txt", "text/plain", b"Name\x00\nExperience\x00ByteDance")

    assert "\x00" not in res.content
    assert "Name" in res.content
    assert "Experience" in res.content


def test_parse_empty_text_rejects_document():
    with pytest.raises(ValueError, match="empty"):
        parse_bytes("empty.txt", "text/plain", b"   ")
