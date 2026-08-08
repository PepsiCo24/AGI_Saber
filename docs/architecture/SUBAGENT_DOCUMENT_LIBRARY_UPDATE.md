# Sub-Agent And Local Document Library Update

## Summary

This update adds a sub-agent execution path and a first version of the local document library. The system can now plan research/report/document tasks as a graph of specialized agents, write generated Markdown reports into a local document library, and expose those documents in the frontend.

## What Changed

### Sub-agent runtime

- Added built-in sub-agents:
  - `research_agent`: performs agentic RAG/search-style research and evidence gathering.
  - `writer_agent`: turns upstream findings into a Markdown report.
  - `review_agent`: reviews report quality, risks, and missing evidence.
  - `doc_agent`: writes the final report into the local document library and attempts RAG ingestion.
- Extended graph nodes with `type`, `agent_name`, and `goal`.
- Updated graph runtime so `sub_agent` nodes execute through the sub-agent registry.
- Upstream node results now include executor names such as `n2:writer_agent`, allowing `doc_agent` to identify the report body cleanly.

### Planning behavior

- Research/report/document style tasks now deterministically choose the sub-agent DAG before falling back to LLM planner behavior.
- Typical report-save flow:

```text
research_agent -> writer_agent -> review_agent -> doc_agent
```

- Non-document research flow:

```text
research_agent -> writer_agent -> review_agent
```

### Local document library

- Added document domain models:
  - `documents`
  - `document_versions`
- Added local fallback persistence under `.data/documents` when PostgreSQL is unavailable.
- Added PostgreSQL schema support for document records and chunk back-references.
- Added document tools:
  - `write_document`
  - `list_documents`
  - `read_document`
  - `ingest_document`
- Added HTTP APIs:
  - `GET /api/documents`
  - `POST /api/documents`
  - `GET /api/documents/{id}`
  - `POST /api/documents/{id}/ingest`

### RAG metadata

- RAG ingestion now accepts document metadata:
  - `document_id`
  - `version_id`
  - `section`
- `rag_chunks` can point back to the source document and version.
- When PostgreSQL is unavailable, document saving still works through local fallback storage, but RAG chunk persistence is skipped.

### Frontend

- Added a left-sidebar local document library panel.
- Added refresh support for document list.
- Added a standalone document viewer modal so reading a document no longer appends content into the current chat conversation.
- Added re-ingest action for a local document.
- Fixed document title display for generated reports.

## Fixes From Testing

- Fixed a planner issue where research-style prompts could be routed to `search_web` instead of sub-agents.
- Fixed generated document titles:
  - explicit prompts like `标题为《子Agent联调测试》` now win.
  - otherwise the title is extracted from the Markdown `# H1`.
- Fixed Markdown body cleanup:
  - outer ```markdown fences are stripped before saving generated reports.
- Fixed local document viewing so it opens a document viewer instead of writing the document into chat history.

## Verification

- `go test ./...` passes.
- `/api/status` reports `sub_agents_count: 4`.
- End-to-end test request executed:

```text
生成一份标题为《子Agent联调测试》的简短报告并保存到本地文档库。
```

- Observed execution path:

```text
research_agent -> writer_agent -> review_agent -> doc_agent
```

- The test produced local document:

```text
document_id: doc_12159c3dc4a3a8a9
title: 子Agent联调测试
latest_version: 2
```

## Current Limitations

- PostgreSQL, Milvus, Elasticsearch, Kafka, and Neo4j may be disconnected in local development.
- When PostgreSQL is disconnected, local document storage works, but RAG chunk indexing reports `indexed_count=0`.
- The current `research_agent` still depends on available RAG/search behavior and can produce weak evidence if no reliable source is available.
- Generated report quality is still model-dependent; review results are stored in document metadata, not merged into the report body.

## Next Steps

- Add a delete/rename API for local documents.
- Add a version history UI in the document viewer.
- Add explicit planner controls so users can force or skip sub-agents.
- Add a stronger research trace model with citations and source confidence.
- Add integration tests for `/api/chat` report generation using a mock LLM.
