# AGI Assistant - Architecture Documentation

This directory contains architecture documents describing the current codebase, RAG ingestion flow, and refactoring roadmap.

## 📋 Documents

### 1. **ARCHITECTURE_MAP.md** (26 KB)
**Complete architectural inventory of the codebase.**

- Executive summary (patterns, test coverage)
- Initialization order (main.go)
- Configuration structure (config.go: 100+ fields organized by category)
- **Each package section includes**:
  - Files and line counts
  - Public types and function signatures
  - Complete import lists
  - Dependency graph relationships
  - One-sentence purpose statement

**Packages covered** (8,099 lines across 33 files):
- agent/ (1881 lines) — Core orchestrator routing requests through 4 modes
- graph/ (705 lines) — Neo4j knowledge graph for entity extraction + RAG
- handler/ (280 lines) — HTTP API routes
- infra/ (847 lines) — Infrastructure: Milvus, PostgreSQL, Elasticsearch, Kafka
- llm/ (414 lines) — LLM client with mock fallback
- memory/ (1053 lines) — 3-layer memory system (Short/Long/Pref) with consolidation
- rag/ (581 lines) — Hybrid search: semantic + BM25 + KG + RRF fusion
- runtime/ (1123 lines + 4 test files) — Schema-driven System Prompt assembly
- sandbox/ (632 lines) — Command execution: Docker/Local/Mock with validation
- tools/ (302 lines) — Tool registry with built-in & MCP support
- config/ (372 lines) — Configuration with graceful degradation

**Key findings**:
- Zero circular dependencies
- Clean 6-layer dependency stack (config → infra → functional → agent → handler)
- Only runtime/ package has tests (4 files; ~0.1% total coverage)
- All services degrade gracefully on connection failure

---

### 2. **REFACTOR_READINESS.md** (12 KB)
**Structured refactoring roadmap with identified pain points and opportunities.**

**10 Major Refactoring Opportunities** (prioritized):

1. **agent.go (1881L) is too large** → Split into 8 files by mode
   - Core orchestrator, router, ReAct impl, tool/rag/chat modes, snapshots, tool registry
   
2. **runtime/ is undertested** → Expand from 4 test files
   - Add concurrent slot-filling tests, schema validation, integration tests
   
3. **memory/ semantics unclear** → Extract consolidation engine
   - Clearer ownership model, single recall API, explicit embedding trait
   
4. **sandbox/ has no integration tests** → Add Docker/Local/Mock chaos tests
   - Test resource constraints, timeouts, killed processes
   
5. **handler/ routes not versioned** → Add /api/v1/ versioning
   - Separate handler files per concern, middleware layer
   
6. **graph/ needs perf audit** → Parameterized Cypher, caching, N+1 analysis
   - Query injection prevention, entity→PGID caching
   
7. **config is flat 100+ field struct** → Split by concern
   - DB config, LLM config, memory config, RAG config, sandbox config
   
8. **llm/ should support multiple models** → Model registry + routing
   - Per-task model selection, separate embedding client
   
9. **rag/ search lacks observability** → Add metrics & trace
   - Track which backend returned top-K, RRF weights
   
10. **No structured logging** → Add observability layer
    - Structured logging, trace IDs, Prometheus metrics

**Priority Matrix**:
- P0: agent split (HIGH impact, MEDIUM effort)
- P1: runtime tests, memory semantics, handler versioning (MEDIUM-HIGH impact)
- P2: sandbox tests, graph perf, config split (MEDIUM impact)
- P3: llm multi-model, rag observability, logging (LOW impact, HIGH effort)

**Recommended Phases**:
- Phase 1 (1-2 weeks): Stabilize core — split agent.go, expand runtime tests
- Phase 2 (2 weeks): Harden APIs — version routes, sandbox tests, memory extraction
- Phase 3 (2-3 weeks): Observability — logging, trace IDs, metrics
- Phase 4 (3+ weeks): Optimization — Neo4j perf, multi-model LLM, config format

**Risk Assessment**:
- Low: agent split, tests, route versioning (logic preserved)
- Medium: runtime tests, memory changes, sandbox tests (may reveal bugs)
- High: config format, multi-model LLM, Neo4j queries (logic changes)

---

### 3. **DEPENDENCY_GRAPH.md** (12 KB)
**Visual dependency diagrams and import flow analysis.**

**Visualizations**:
- High-level layer diagram (6 layers: config → handler)
- Detailed dependency matrix (package × imports)
- User message → response import flow (trace through all mode routing)
- Dependency depth analysis (0-6 layers, no cycles)
- Critical paths visualization (4 main flows: ingest, query, consolidation, exec)
- Test coverage table (4 tests/8099 lines = ~0.1%)
- Refactoring impact map (what breaks if you change each package)

**Key Insights**:
- agent/ is the hub, imports ALL functional layers
- infra/ is the foundation, gracefully degrades if services unavailable
- runtime/ implements novel schema-driven prompt assembly
- memory/ + graph/ provide semantic context to LLM prompts
- sandbox/ implements validate → execute → audit flow
- Tools have MCP support for extensibility

---

### 4. **PDF_INGESTION_PIPELINE.md**
**Current PDF upload, parsing, chunking, and RAG indexing flow.**

**What it covers**:
- Frontend `FormData` upload and `/api/upload` request handling
- Parser priority: `pdfplumber` → `pdftotext` → Go PDF fallback
- Text normalization and OCR detection
- Parent/child chunking strategy for small-to-big retrieval
- PostgreSQL-first indexing semantics
- RRF fusion plus optional LLM listwise rerank behavior
- Local PDF test results and next optimization steps

Use this document when working on PDF parsing, OCR, RAG chunk quality,
document-library persistence, or sub-agent report writeback.

---

## 🚀 Quick Start for Refactoring

### If you want to understand the current system:
1. Start with **ARCHITECTURE_MAP.md** — high-level overview of each package
2. Skip to **DEPENDENCY_GRAPH.md** — trace critical paths you care about
3. Read package-specific comments in the code itself

### If you want to propose refactoring:
1. Read **REFACTOR_READINESS.md** — identify pain points
2. Check **DEPENDENCY_GRAPH.md** "Impact Map" for what your change affects
3. Cross-reference with **ARCHITECTURE_MAP.md** imports to avoid accidental cycles
4. Propose phases that respect the dependency stack

### If you want to add a new feature:
1. **Is it a new tool?** → Add to tools/ or implement as MCP, register via handler
2. **Is it a new memory type?** → Extend memory/ (Item.Category supports categorization)
3. **Is it a new search mode?** → Add ContextSource in runtime/, update schema
4. **Is it a new LLM capability?** → Extend llm.Client, update graph extractor
5. **Is it a new execution backend?** → Implement sandbox.Executor interface

### If you want to optimize PDF/RAG ingestion:
1. Start with **PDF_INGESTION_PIPELINE.md** — current upload and indexing flow
2. Check rag/ splitter behavior before changing chunk sizes
3. Verify PostgreSQL is available before judging `indexed_count`
4. Add parser/upload tests when changing PDF extraction or API response fields

---

## 📊 Codebase Statistics

| Metric | Value |
|--------|-------|
| Total Go code | 8,099 lines |
| Packages | 10 internal + config |
| Files | 33 files (4 test files) |
| Test coverage | ~0.1% (4 test files) |
| Circular dependencies | 0 |
| Layers | 6 (config → handler) |
| Largest file | agent.go (1,881 lines) |
| Smallest package | tools/ (2 files, 302 lines) |

---

## 🔍 Architecture at a Glance

```
Request → handler → agent.UnifiedAgent.ProcessContext()
                       ├─ Assemble system prompt via runtime.Assembler
                       │   ├─ memory.stm (recent turns)
                       │   ├─ memory.ltm (semantic recall)
                       │   ├─ memory.pref (user preferences)
                       │   └─ runtime sources (profile, planner, constraints, tools)
                       ├─ Decide execution mode (ReAct, Tool, RAG, Chat)
                       ├─ Execute:
                       │   ├─ ReAct: multi-step reasoning with tools
                       │   ├─ Tool: single tool + sandbox execution
                       │   ├─ RAG: hybrid search + generation
                       │   └─ Chat: direct LLM call
                       ├─ Update memory (consolidation, decay, expiration)
                       └─ Return Response (streaming or sync)

Infrastructure (graceful degradation):
  • Milvus (vector DB) — for semantic search
  • PostgreSQL (chunk store) — for RAG chunk persistence
  • Elasticsearch (BM25) — for keyword search
  • Neo4j (KG store) — for entity relationships + memory graph
  • Kafka (audit log) — for command execution audit trail
```

---

## 📚 Related Files

- `README.md` — Project overview and setup
- `main.go` — Application entry point
- `config/config.go` — Configuration loading
- `.claude/` — Claude Code project settings

---

## 💡 Most Interesting Design Decisions

1. **Schema-Driven Context Assembly** (runtime/)
   - Pluggable ContextSource interface enables extensible System Prompt construction
   - 3 pre-built routing schemas (Chat, Tool, ReAct) with different slot priorities
   - Token budget management prevents prompt bloat

2. **Graceful Infrastructure Degradation**
   - All external services can fail independently
   - System continues with reduced capability (e.g., in-memory vector search if Milvus down)
   - Clear Ready.Status indicator for operators

3. **Hybrid Search Fusion** (rag/)
   - Combines Milvus (semantic) + ES (BM25) + Neo4j (graph) via RRF
   - Chunk persistence in PostgreSQL enables integration with KG entity IDs

4. **Multi-Layer Memory** (memory/)
   - Short-term: sliding window of recent turns
   - Long-term: semantic/TF-IDF recall with automatic consolidation
   - Preferences: extracted user preferences update prompt generation
   - Optional graph layer (memory/ + graph/) for episodic connections

5. **Sandbox Execution with Audit** (sandbox/)
   - Validator (static security checks) → Executor (isolated execution) → Audit (Kafka)
   - 3 backends: Docker (isolated), Local (fast dev), Mock (testing)

---

## ⚠️ Known Limitations & Opportunities

See **REFACTOR_READINESS.md** for detailed analysis:
- agent.go is 1881 lines (should be ~8 files)
- Only 0.1% test coverage (focus on runtime/)
- Routes not versioned (/api/v1/ missing)
- No structured logging or trace IDs
- Neo4j queries not parameterized (injection risk)
- RRF search fusion not instrumented
- Config is flat struct (hard to maintain)
- OCR and persistent document-library metadata are not implemented yet

---

Generated: June 10, 2026
