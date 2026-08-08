# PDF Ingestion Pipeline

Updated: 2026-06-16

This document records the current local implementation for uploading a PDF,
turning it into normalized text, splitting it for RAG, and indexing it into the
hybrid retrieval stack.

## Current Status

Implemented:

- The frontend uploads the original file with `multipart/form-data`, field name
  `file`.
- `POST /api/upload` accepts real files and still supports the older JSON
  `{ "content": "..." }` text fallback.
- PDF bytes are parsed as PDF content instead of being read as raw UTF-8 text.
- PDF extraction priority is `pdfplumber` -> `pdftotext` -> Go PDF fallback.
- Extracted text is normalized before RAG chunking.
- PDFs with too little extractable text return `needs_ocr: true` instead of
  silently entering the knowledge base.
- RAG ingest now returns `parent_count`, `chunk_count`, `indexed_count`,
  `chunk_preview`, and `doc_hash`.
- If PostgreSQL is unavailable, indexing short-circuits before embedding calls.

Not implemented yet:

- OCR for scanned/image-only PDF pages.
- A persistent document-library table for uploaded document metadata.
- A document viewer that lists local generated reports independently from RAG
  chunks.
- Per-page chunk metadata and ingest tracing.
- Sub-agent writeback into a local document library.

## Request Flow

```text
frontend/index.html
  |
  | FormData(file)
  v
POST /api/upload
  |
  v
internal/interfaces/http/handler/handler.go
  - MaxBytesReader: 64 MB
  - parseUploadDocument()
  - multipart field: file
  - legacy JSON fallback: content
  |
  v
internal/domain/document/parser.go
  - ParseBytes(filename, contentType, data)
  - PDF -> parsePDF()
  - non-PDF -> normalize plain text
  |
  v
internal/domain/rag/rag.go
  - Engine.Ingest()
  - parent RecursiveSplitter
  - child RecursiveSplitter
  |
  v
internal/domain/rag/hybrid.go
  - HybridStore.IndexWithParents()
  - PG is the first persistence gate
  - embedding + Milvus + Elasticsearch + Neo4j follow only when PG is available
```

## PDF Parser Strategy

`document.ParseBytes` detects PDF input when either condition is true:

- `Content-Type` is `application/pdf`
- Filename extension is `.pdf`

The PDF extraction order is:

1. `pdfplumber`
   - Runs through Python.
   - Candidate interpreters:
     - `PDF_EXTRACT_PYTHON`
     - `PDF_PYTHON`
     - `python3` on `PATH`
     - Codex bundled Python under
       `~/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3`
   - Emits page markers like `--- page 1 ---`.
   - Current extraction timeout: 30 seconds.

2. `pdftotext`
   - Used only when the executable exists on `PATH`.
   - Called with layout preservation and UTF-8 output.

3. Go fallback
   - Uses `github.com/ledongthuc/pdf`.
   - Tries row-based page text first, then plain text fallback.

If all PDF extraction paths produce no useful text, the API returns an error
that the PDF needs OCR. If a PDF has pages but fewer than 80 extracted runes,
the response marks `needs_ocr: true`.

## Text Normalization

All extracted text is normalized before RAG chunking:

- `\r\n` and `\r` become `\n`.
- Null bytes and soft hyphens are removed.
- Hyphenated line breaks such as `RE-\nWARDS` are repaired.
- Repeated spaces and tabs are collapsed.
- Excessive blank lines are collapsed while keeping paragraph boundaries.
- Leading/trailing whitespace is trimmed.

This keeps the splitter from producing chunks dominated by PDF layout artifacts.

## RAG Chunking

The ingest path uses a small-to-big chunking strategy:

```text
document text
  |
  v
parent splitter
  - size: max(rag.chunk_size * 4, 600)
  - overlap: rag.chunk_overlap * 2
  |
  v
child splitter
  - size: rag.chunk_size
  - overlap: rag.chunk_overlap
  |
  v
index child chunks with parent text attached
```

With the current config:

- `rag.chunk_size = 200`
- `rag.chunk_overlap = 50`
- Parent chunk size is `800`
- Parent overlap is `100`
- Child chunk size is `200`
- Child overlap is `50`

The parent chunk is used later for small-to-big context expansion: retrieval
matches a precise child chunk, but the LLM can receive the larger parent text.

## Indexing Semantics

`HybridStore.IndexWithParents()` persists child chunks and retrieval metadata.
PostgreSQL is the first required dependency because PG provides stable chunk
IDs used by the rest of the retrieval stack.

Current indexing order:

1. Check PostgreSQL availability.
2. If PG is unavailable:
   - Return the computed `doc_hash`.
   - Return zero indexed chunks.
   - Skip embedding calls.
   - Skip Milvus and Elasticsearch writes.
3. If PG is available:
   - Generate embeddings.
   - Save chunk, parent content, and embedding JSON to PG.
   - Upsert semantic vectors to Milvus when available.
   - Upsert keyword documents to Elasticsearch when available.
   - Trigger Neo4j KG indexing asynchronously when available.

`Engine.Loaded` is now set from `len(indexed) > 0`, so a parsed-but-not-indexed
upload does not make RAG query mode claim the knowledge base is ready.

## Query And Rerank

The query side remains hybrid RAG:

```text
user question
  |
  v
optional query rewrite
  |
  v
multi-query retrieval
  - semantic vector search
  - BM25 keyword search
  - knowledge graph search
  |
  v
RRF fusion
  |
  v
optional LLM listwise rerank
  |
  v
small-to-big parent expansion
  |
  v
LLM answer synthesis
```

Rerank is configured in `config/config.yaml` under `rag.rerank`.
The current implementation is an LLM listwise reranker:

- Candidates are first fused by RRF.
- The candidate pool is expanded before rerank.
- The LLM assigns each candidate a 0-10 relevance score.
- Final result score becomes `llm_score / 10.0`.
- If rerank parsing fails, the system falls back to the original RRF order.

## Upload API Response

Successful non-OCR upload responses include:

```json
{
  "filename": "paper.pdf",
  "content_type": "application/pdf",
  "parser": "pdfplumber",
  "pages": 22,
  "text_chars": 76642,
  "needs_ocr": false,
  "chunk_count": 539,
  "parent_count": 102,
  "indexed_count": 0,
  "chunk_preview": [],
  "doc_hash": "sha256...",
  "chunks": []
}
```

When `needs_ocr` is true, `chunk_count`, `parent_count`, and `indexed_count`
are all returned as zero, and the document is not ingested into RAG.

`indexed_count` can be lower than `chunk_count` when infrastructure is partially
unavailable. In the current local run, PostgreSQL, Milvus, Elasticsearch, and
Kafka were disconnected, so parsing and splitting succeeded but indexing stayed
at `0`.

## Local Test Results

The optimized parser was tested with local PDFs from the user's Downloads
folder. Personal/contact details from the extracted documents are intentionally
not copied into this document.

| File | Parser | Pages | Text chars | Parent chunks | Child chunks | Indexed | OCR |
|------|--------|-------|------------|---------------|--------------|---------|-----|
| ScholarAgent PDF | `pdfplumber` | 2 | 2,005 | 3 | 13 | 0 | false |
| RAG-DDR ICLR 2025 paper | `pdfplumber` | 22 | 76,642 | 102 | 539 | 0 | false |

Interpretation:

- PDF extraction worked for both files.
- Chinese and English text were extracted cleanly enough for chunking.
- `indexed_count = 0` was caused by unavailable local persistence services,
  not by PDF parsing failure.

## Configuration Notes

- Secrets should stay outside `config/config.yaml`.
- The local config loader expands environment variables in YAML.
- A local `.env` file can provide values such as `DEEPSEEK_API_KEY`.
- `.env` is ignored by git and must not be copied into documentation.
- Optional PDF extraction executables can be selected with:
  - `PDF_EXTRACT_PYTHON`
  - `PDF_PYTHON`

## Code References

- `frontend/index.html`
  - Builds `FormData` and posts the original file.
- `internal/interfaces/http/handler/handler.go`
  - Implements `/api/upload`, 64 MB body limit, multipart parsing, and response
    fields.
- `internal/domain/document/parser.go`
  - Owns PDF/plain-text parsing, external parser priority, normalization, and
    OCR detection.
- `internal/domain/rag/rag.go`
  - Owns parent/child splitting and `IngestResult`.
- `internal/domain/rag/splitter.go`
  - Implements recursive splitting, paragraph boundaries, sentence boundaries,
    and code block protection.
- `internal/domain/rag/hybrid.go`
  - Owns PG/Milvus/Elasticsearch/Neo4j indexing and hybrid retrieval.
- `internal/infrastructure/persistence/ragchunk/ragchunk.go`
  - Owns PG availability detection and chunk persistence.

## Next Optimization Steps

Recommended next steps:

1. Add OCR fallback for scanned PDFs.
2. Add a `documents` table for uploaded/generated document metadata.
3. Persist page number, parent chunk ID, parser name, and source filename per
   chunk.
4. Add an ingest trace endpoint so the UI can show parse/split/index stages.
5. Add a local document library UI separate from the RAG chunk preview.
6. Let sub-agents generate reports and write them into the document library.
7. Add tests for PDF parser fallback order and upload handler response shape.
