# Architecture Documentation Index

This file serves as a quick reference to the complete architectural analysis of the AGI Assistant codebase.

## 📖 Documents (Read in This Order)

### 1. **Start Here: ARCHITECTURE_README.md**
- **Purpose**: Navigation guide & quick reference
- **Best for**: Getting oriented, understanding what documents to read
- **Time to read**: 5-10 minutes
- **Key sections**:
  - Overview of all 3 analysis documents
  - Quick start guides (understand system / propose refactor / add feature)
  - Architecture at a glance (single-page visual)
  - Most interesting design decisions
  - Known limitations & opportunities

### 2. **Details: ARCHITECTURE_MAP.md** (COMPREHENSIVE)
- **Purpose**: Complete package-by-package inventory
- **Best for**: Deep dives into specific packages, understanding dependencies
- **Time to read**: 20-30 minutes (or reference as needed)
- **Sections** (one per package):
  - agent/ (1881 lines) - Core orchestrator routing through 4 modes
  - graph/ (705 lines) - Neo4j knowledge graph + entity extraction
  - handler/ (280 lines) - HTTP API routes
  - infra/ (847 lines) - Infrastructure: Milvus, PostgreSQL, ES, Kafka
  - llm/ (414 lines) - LLM client with mock fallback
  - memory/ (1053 lines) - 3-layer memory system with consolidation
  - rag/ (581 lines) - Hybrid search: semantic + BM25 + KG + RRF
  - runtime/ (1123 lines + tests) - Schema-driven System Prompt assembly
  - sandbox/ (632 lines) - Command execution: Docker/Local/Mock
  - tools/ (302 lines) - Tool registry with MCP support
  - config/ (372 lines) - Configuration loading
  
- **For each package includes**:
  - Files and line counts
  - Public types and function signatures
  - Complete import lists (dependencies)
  - Dependency graph relationships
  - One-sentence purpose statement

### 3. **Deep Dive: PDF_INGESTION_PIPELINE.md**
- **Purpose**: Current PDF upload → parsing → chunking → RAG indexing flow
- **Best for**: Optimizing document ingestion, debugging upload issues, planning OCR/document-library work
- **Time to read**: 10-15 minutes
- **Key sections**:
  - Request flow from frontend `FormData` to `/api/upload`
  - PDF parser priority: `pdfplumber` → `pdftotext` → Go fallback
  - Text normalization and OCR detection
  - Parent/child chunking strategy
  - PostgreSQL/Milvus/Elasticsearch/Neo4j indexing semantics
  - Current rerank score behavior
  - Local PDF test results
  - Next optimization steps

### 4. **Visuals: DEPENDENCY_GRAPH.md** (REFERENCE)
- **Purpose**: Visual dependency diagrams and critical paths
- **Best for**: Understanding request flows, impact analysis, tracing bugs
- **Time to read**: 10-15 minutes
- **Key diagrams**:
  - 6-layer architecture diagram (config → handler)
  - Package dependency matrix (who imports whom)
  - User message → response flow (full request trace)
  - 4 critical paths (ingest, query consolidation, command exec)
  - Dependency depth analysis (0-6 layers, no cycles!)
  - Test coverage table (4 test files out of 33)
  - Refactoring impact map

### 5. **Action: REFACTOR_READINESS.md** (ROADMAP)
- **Purpose**: Strategic refactoring prioritization & planning
- **Best for**: Planning refactoring work, assessing risks, picking next steps
- **Time to read**: 15-20 minutes
- **Sections**:
  - 5 architecture strengths (why it's well-designed)
  - 10 refactoring opportunities (ranked by priority)
  - For each opportunity: issue, refactor approach, benefits
  - Priority matrix (Impact × Effort)
  - 4-phase implementation plan (1-10+ weeks)
  - Risk assessment (Low/Medium/High per refactor)
  - Immediate actionable next steps
  - Dependency order for safe refactoring

---

## 🎯 Quick Navigation by Use Case

### I want to understand the current system
1. Read **ARCHITECTURE_README.md** (5-10 min)
2. Scan **ARCHITECTURE_MAP.md** executive summary (5 min)
3. Skim **DEPENDENCY_GRAPH.md** "Critical Paths" section (5 min)
4. Pick a package and read its full section in ARCHITECTURE_MAP.md

### I'm proposing a refactor
1. Read **REFACTOR_READINESS.md** sections 1-3 (10 min)
2. Check **DEPENDENCY_GRAPH.md** "Refactoring Impact Map" (5 min)
3. Cross-reference affected packages in **ARCHITECTURE_MAP.md** (10 min)
4. Estimate effort using the priority matrix and risk assessment

### I'm adding a new feature
1. Read **ARCHITECTURE_README.md** "If you want to add a new feature" (2 min)
2. Look up relevant packages in **ARCHITECTURE_MAP.md** (5 min)
3. Trace affected code paths in **DEPENDENCY_GRAPH.md** (5 min)
4. Check infra impacts in **ARCHITECTURE_MAP.md** infra section (5 min)

### I want to optimize PDF/RAG ingestion
1. Read **PDF_INGESTION_PIPELINE.md** current status and request flow (5 min)
2. Check **PDF_INGESTION_PIPELINE.md** next optimization steps (5 min)
3. Cross-reference **ARCHITECTURE_MAP.md** rag/handler/infra sections (10 min)
4. Use **DEPENDENCY_GRAPH.md** critical paths to estimate impact (5 min)

### I'm debugging a complex issue
1. Start in **DEPENDENCY_GRAPH.md** with relevant critical path (5 min)
2. Trace through imports in **ARCHITECTURE_MAP.md** (10 min)
3. Check test coverage in **DEPENDENCY_GRAPH.md** for related packages (5 min)
4. Read package comments in source code for edge cases

### I want to improve test coverage
1. Read **DEPENDENCY_GRAPH.md** "Testing Coverage by Layer" (5 min)
2. Check **REFACTOR_READINESS.md** opportunities #1, #2, #4 (5 min)
3. Look at existing tests in runtime/ as examples (10 min)
4. Pick a package from P1 or P2 priority list

---

## 📊 Key Numbers at a Glance

| Metric | Value | Note |
|--------|-------|------|
| **Total Go code** | 8,099 lines | Across 33 files |
| **Packages** | 10 internal + config | 11 total |
| **Test files** | 4 files | Only runtime/ has tests |
| **Test coverage** | ~0.1% | 4 test files / 33 total |
| **Circular dependencies** | 0 | Clean architecture! |
| **Dependency layers** | 6 levels | config → handler |
| **Largest file** | agent.go (1881 lines) | Refactor opportunity |
| **Most tested package** | runtime/ | 1123 lines + 4 test files |

---

## 🏗️ Architecture Layers (Bottom-Up)

```
Layer 0: config/           — Configuration loading & defaults
Layer 1: infra/, llm/      — Infrastructure & API clients
Layer 2: graph/, rag/,     — Functional capabilities
         sandbox/, tools/
Layer 3: memory/,          — Semantic layers
         runtime/
Layer 4: agent/            — Central orchestrator (all layers converge here)
Layer 5: handler/          — HTTP API routes
Layer 6: main.go           — Application entry point
```

**Direction**: Upward only (no downward dependencies) = Clean architecture ✅

---

## 🎯 Top 3 Refactoring Priorities (by impact/effort)

### P0: Split agent.go (1881L → 8 files)
- **Status**: Not started
- **Effort**: MEDIUM (1-2 weeks)
- **Impact**: HIGH
- **Enables**: Independent mode testing, MCP extensions
- **Risk**: LOW (refactor only, no logic change)
- **See**: REFACTOR_READINESS.md, section "agent.go is Too Large"

### P1: Expand runtime tests
- **Status**: 4 files exist, need expansion
- **Effort**: MEDIUM (1-2 weeks)
- **Impact**: MEDIUM-HIGH
- **Enables**: Safer schema modifications, concurrent bug detection
- **Risk**: MEDIUM (may reveal bugs to fix)
- **See**: REFACTOR_READINESS.md, section "Runtime Package is Complex but Undertested"

### P1: Version handler routes
- **Status**: Not started
- **Effort**: LOW (3-5 days)
- **Impact**: MEDIUM-HIGH (API stability)
- **Enables**: Backward compatibility, deprecation support
- **Risk**: LOW (add routes, keep old ones)
- **See**: REFACTOR_READINESS.md, section "Handler Package Too Thin"

---

## 📈 Test Coverage by Layer

| Layer | Package | LOC | Tests | Status |
|-------|---------|-----|-------|--------|
| 3 | runtime/ | 1123 | 4✓ | Partial — needs expansion |
| 2 | graph/ | 705 | 0 | ⚠️ Needs query injection + perf |
| 2 | rag/ | 581 | 0 | ⚠️ Needs RRF fusion tests |
| 2 | sandbox/ | 632 | 0 | ⚠️ Needs Docker/Local chaos |
| 1 | infra/ | 847 | 0 | ⚠️ Needs mock backends |
| 1 | llm/ | 414 | 0 | ⚠️ Needs fallback tests |
| 3 | memory/ | 1053 | 0 | ⚠️ Needs consolidation tests |
| 4 | agent/ | 1881 | 0 | ⚠️ Needs mode routing tests |
| 5 | handler/ | 280 | 0 | ⚠️ Needs route tests |
| 2 | tools/ | 302 | 0 | ⚠️ Needs tool exec tests |
| 0 | config/ | 372 | 0 | ⚠️ Needs validation tests |
| **TOTAL** | | **8099** | **4** | **~0.1%** |

**Recommendation**: Add tests to agent, rag, memory, sandbox for critical paths.

---

## 💾 Files in This Package

```
/Users/yangshujie/AGI-assistant/
├── ARCHITECTURE_DOCS_INDEX.md      ← You are here
├── ARCHITECTURE_README.md          ← Navigation guide (START HERE)
├── ARCHITECTURE_MAP.md             ← Detailed package inventory
├── PDF_INGESTION_PIPELINE.md       ← PDF upload, parsing, chunking, indexing
├── REFACTOR_READINESS.md           ← Refactoring roadmap
├── DEPENDENCY_GRAPH.md             ← Visualizations & impact
├── README.md                       (original project README)
├── main.go                         (application entry point)
├── config/
├── internal/
│   ├── agent/          (1881 lines)
│   ├── graph/          (705 lines)
│   ├── handler/        (280 lines)
│   ├── infra/          (847 lines)
│   ├── llm/            (414 lines)
│   ├── memory/         (1053 lines)
│   ├── rag/            (581 lines)
│   ├── runtime/        (1123 lines + 4 test files)
│   ├── sandbox/        (632 lines)
│   └── tools/          (302 lines)
└── [other files...]
```

---

## ✅ Checklist for Refactoring Projects

### Before starting:
- [ ] Read ARCHITECTURE_README.md
- [ ] Review ARCHITECTURE_MAP.md packages involved
- [ ] Check DEPENDENCY_GRAPH.md impact map
- [ ] Check PDF_INGESTION_PIPELINE.md if the change touches upload/RAG ingest
- [ ] Read REFACTOR_READINESS.md risk assessment
- [ ] Run `go test ./...` to establish baseline

### While working:
- [ ] Keep dependency layers intact (no downward deps)
- [ ] Add tests for new functionality
- [ ] Document public APIs
- [ ] Update diagrams in DEPENDENCY_GRAPH.md if structure changes

### After completing:
- [ ] Run full test suite
- [ ] Check for new circular dependencies (`go mod graph`)
- [ ] Verify no broken imports
- [ ] Update architecture docs if changes significant

---

## 🔗 Related Resources

- **Original README**: `/Users/yangshujie/AGI-assistant/README.md`
- **Source code**: `/Users/yangshujie/AGI-assistant/internal/`
- **Configuration**: `/Users/yangshujie/AGI-assistant/config/`
- **Main entry**: `/Users/yangshujie/AGI-assistant/main.go`

---

## 📝 Document Metadata

- **Created**: June 10, 2026
- **Total documentation lines**: 1,775 lines
- **Total documentation size**: ~60 KB
- **Analysis scope**: All Go code in internal/ + config/
- **Analysis method**: Systematic file-by-file review of imports, types, and functions

---

**Last updated**: June 10, 2026  
**Status**: ✅ Complete and ready for refactoring planning
