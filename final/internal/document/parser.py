import mimetypes
import os
import re
import shutil
import subprocess
import tempfile
from dataclasses import dataclass
from pathlib import Path
from typing import Optional, Tuple


MIN_USEFUL_PDF_TEXT_RUNES = 80
_HYPHEN_LINE_BREAK_RE = re.compile(r"([A-Za-z])-\n([A-Za-z])")


@dataclass
class ParseResult:
    filename: str = ""
    content_type: str = ""
    parser: str = ""
    content: str = ""
    pages: int = 0
    text_chars: int = 0
    needs_ocr: bool = False


def parse_bytes(filename: str, content_type: str, data: bytes) -> ParseResult:
    filename = filename or ""
    content_type = _normalize_content_type(filename, content_type)
    ext = Path(filename).suffix.lower()
    if content_type == "application/pdf" or ext == ".pdf":
        return _parse_pdf(filename, content_type, data)

    text = _normalize_text(_decode_text(data))
    if not text.strip():
        raise ValueError("uploaded document is empty")
    return ParseResult(
        filename=filename,
        content_type=content_type,
        parser="plain_text",
        content=text,
        text_chars=_rune_len(text),
    )


def _parse_pdf(filename: str, content_type: str, data: bytes) -> ParseResult:
    for parser in (_extract_pdf_with_pdfplumber, _extract_pdf_with_pypdf2, _extract_pdf_with_pdftotext):
        text, pages, parser_name = parser(data)
        if text.strip():
            text = _normalize_text(text)
            chars = _rune_len(text)
            return ParseResult(
                filename=filename,
                content_type=content_type,
                parser=parser_name,
                content=text,
                pages=pages,
                text_chars=chars,
                needs_ocr=pages > 0 and chars < MIN_USEFUL_PDF_TEXT_RUNES,
            )
    raise ValueError("pdf contains no extractable text; OCR is required")


def _extract_pdf_with_pdfplumber(data: bytes) -> Tuple[str, int, str]:
    try:
        import pdfplumber  # type: ignore
    except Exception:
        return "", 0, "pdfplumber"
    try:
        with tempfile.NamedTemporaryFile(suffix=".pdf", delete=False) as f:
            f.write(data)
            path = f.name
        try:
            texts = []
            with pdfplumber.open(path) as pdf:
                pages = len(pdf.pages)
                for idx, page in enumerate(pdf.pages, 1):
                    text = page.extract_text(x_tolerance=1, y_tolerance=3) or ""
                    if text.strip():
                        texts.append(f"--- page {idx} ---\n{text}")
            return "\n\n".join(texts), pages, "pdfplumber"
        finally:
            _safe_unlink(path)
    except Exception:
        return "", 0, "pdfplumber"


def _extract_pdf_with_pypdf2(data: bytes) -> Tuple[str, int, str]:
    try:
        from PyPDF2 import PdfReader
    except Exception:
        return "", 0, "pdf_text"
    try:
        import io

        reader = PdfReader(io.BytesIO(data))
        texts = []
        for idx, page in enumerate(reader.pages, 1):
            text = page.extract_text() or ""
            if text.strip():
                texts.append(f"--- page {idx} ---\n{text}")
        return "\n\n".join(texts), len(reader.pages), "pdf_text"
    except Exception:
        return "", 0, "pdf_text"


def _extract_pdf_with_pdftotext(data: bytes) -> Tuple[str, int, str]:
    exe = shutil.which("pdftotext")
    if not exe:
        return "", 0, "pdftotext"
    try:
        with tempfile.NamedTemporaryFile(suffix=".pdf", delete=False) as f:
            f.write(data)
            path = f.name
        try:
            out = subprocess.check_output(
                [exe, "-layout", "-enc", "UTF-8", path, "-"],
                timeout=30,
            )
            return out.decode("utf-8", errors="ignore"), 0, "pdftotext"
        finally:
            _safe_unlink(path)
    except Exception:
        return "", 0, "pdftotext"


def _normalize_content_type(filename: str, content_type: str) -> str:
    content_type = (content_type or "").split(";")[0].strip().lower()
    if content_type:
        return content_type
    guessed, _ = mimetypes.guess_type(filename or "")
    return (guessed or "text/plain").lower()


def _decode_text(data: bytes) -> str:
    if isinstance(data, str):
        return data
    for encoding in ("utf-8", "gbk", "latin-1"):
        try:
            return data.decode(encoding)
        except Exception:
            continue
    return data.decode("utf-8", errors="ignore")


def _normalize_text(text: str) -> str:
    text = text.replace("\r\n", "\n").replace("\r", "\n")
    text = text.replace("\x00", "")
    text = _HYPHEN_LINE_BREAK_RE.sub(r"\1\2", text)
    lines = [line.rstrip() for line in text.split("\n")]
    normalized = "\n".join(lines)
    normalized = re.sub(r"\n{3,}", "\n\n", normalized)
    return normalized.strip()


def _rune_len(text: str) -> int:
    return len(text or "")


def _safe_unlink(path: Optional[str]) -> None:
    if not path:
        return
    try:
        os.remove(path)
    except OSError:
        pass
