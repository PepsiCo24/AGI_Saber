# Refactoring Readiness Assessment

## Current Architecture Strengths

1. **Clean Dependency Layering**
   - Zero circular dependencies
   - Infrastructure layer (infra) is isolated
   - Clear separation: config → infra → functional layers → agent → handler
   - No spurious interdependencies

2. **Graceful Degradation Pattern**
   - All external services (Milvus, PG, ES, Kafka) have fallback modes
   - Failed connections don't crash startup
   - System continues with reduced capability
   - Well-suited for cloud deployments

3. **Single Entry Point via UnifiedAgent**
   - All requests route through `agent.UnifiedAgent`
   - Consistent output format via `agent.Response`
   - Request cancellation support built-in
   - Snapshot-based fault recovery

4. **Novel Runtime Context Assembly**
   - Schema-driven prompt construction
   - Pluggable `ContextSource` interface enables extensibility
   - Three pre-built routing modes (Chat/Tool/ReAct)
   - Token budget management baked in

5. **Modular Tool System**
   - Built-in tools (time, weather, search, exec_command)
   - MCP registration support for external tools
   - Tool discovery via HTTP API

---

## Identified Pain Points & Refactor Opportunities

### 1. **agent.go is Too Large (1881 lines)**

**Issue**: Single 1881-line file contains:
- Mode routing logic (ReAct + Harness, Tool, RAG, Chat)
- Snapshot management for fault recovery
- Tool registry with concurrent access (toolsMu)
- Per-request state (task, snapshots, cancelFns)
- All lifecycle integration

**Refactor Opportunity**:
```
agent/
├── agent.go              → Core orchestrator only (~400 lines)
├── router.go             → Mode routing logic (~300 lines)
├── react.go              → ReAct + Harness implementation (~400 lines)
├── tool_mode.go          → Tool-only routing (~200 lines)
├── rag_mode.go           → RAG-only routing (~200 lines)
├── chat_mode.go          → Direct chat routing (~100 lines)
├── snapshot.go           → Fault recovery state (~150 lines)
├── tool_registry.go      → Tool management with registration (~200 lines)
└── types.go              → All shared types (~100 lines)
```

**Benefits**:
- Each mode can be tested independently
- Easier to understand request flow
- Snapshot logic isolated for clearer recovery semantics
- Tool registry changes don't affect mode routing

---

### 2. **Runtime Package is Complex but Undertested**

**Issue**: 
- Only package with tests, but still under-covered (4 test files for 1123 lines)
- ContextSource interface is powerful but underdocumented
- Concurrent slot filling in assembler.go could race with buggy sources
- No integration tests for full schema rendering

**Refactor Opportunity**:
```
runtime/
├── assembler.go          → Keep, add source validation
├── assembler_test.go     → Expand to test concurrent bugs
├── schema.go             → Extract to schema_types.go + schema_defaults.go
├── source.go             → Add ContextSourceValidator interface
├── source_*.go           → Add individual _test files (1 each)
└── integration_test.go   → New: test 3 full schemas end-to-end
```

**Benefits**:
- Schemas become version-able (different versions per API version)
- Source implementations get tested in isolation
- Integration tests catch concurrent slot-filling races early
- Schema validation prevents invalid slot configurations

---

### 3. **Memory Package Needs Clearer Semantics**

**Issue**:
- `GraphMemory` wraps `LongTerm` but doesn't clearly own it
- Consolidation config is complex (7 params) with unclear interactions
- Three separate recall methods (Recall, RecallByFilter, RecallTop) confusing
- Embedding availability implicit, not explicit

**Refactor Opportunity**:
```
memory/
├── short_term.go         → Split short_term/memory.go
├── long_term.go          → Split long_term/memory.go
├── graph_memory.go       → Keep, rename to long_term_graph.go
├── consolidation.go      → Extract consolidation logic
├── embedding.go          → Explicit embedding availability flag
├── types.go              → Shared types + interfaces
└── memory_test.go        → New integration tests
```

**Key Changes**:
- `RecallByFilter` becomes single recall API
- `ConsolidationEngine` extracted as separate interface
- `EmbeddingProvider` explicit trait
- Each implementation auditable for correctness

---

### 4. **Sandbox Package Lacks Integration Tests**

**Issue**:
- Docker/Local/Mock backends all have different error semantics
- Validator security rules not tested comprehensively
- ResourceConstraint enforcement (CPU%, memory) untested in Docker
- No chaos test for timeout/killed scenarios

**Refactor Opportunity**:
```
sandbox/
├── executor.go           → Keep interface clean
├── docker.go             → Add _test with resource verification
├── local.go              → Add _test with timeout tests
├── validator.go          → Add _test for all RiskLevel paths
├── types.go              → Keep
└── integration_test.go   → New: test Docker + Local + Mock failures
```

---

### 5. **Handler Package Too Thin / Routes Not Versioned**

**Issue**:
- All routes at `/api/chat` etc., no versioning
- Request/response validation minimal
- Error handling inconsistent (some return JSON, some plain text)
- No OpenAPI/swagger schema

**Refactor Opportunity**:
```
handler/
├── handler.go            → Router only
├── middleware.go         → New: auth, logging, CORS, request ID
├── chat_handler.go       → New: /api/v1/chat/* routes
├── memory_handler.go     → New: /api/v1/memory/* routes
├── docs_handler.go       → New: /api/v1/docs/* routes
├── tools_handler.go      → New: /api/v1/tools/* routes
├── status_handler.go     → New: /api/v1/status/* routes
├── response.go           → New: shared response envelope
├── errors.go             → New: typed errors + HTTP mappings
└── handler_test.go       → New: route tests with mocked agent
```

---

### 6. **Graph Package Needs Performance Profiling**

**Issue**:
- Neo4j queries not parameterized (potential injection)
- N+1 queries possible in 3-hop expansion
- Extraction via LLM on every chunk = expensive
- No caching of entity → PG ID mappings

**Refactor Opportunity**:
```
graph/
├── neo4j.go              → Add prepared statement caching
├── extractor.go          → Add extraction cache (chunk hash → entities)
├── query_builder.go      → New: parameterized Cypher builder
├── cache.go              → New: entity → PGID LRU cache
└── graph_test.go         → New: query injection tests + performance bench
```

---

### 7. **RAG Hybrid Search Needs Better Observable**

**Issue**:
- RRF fusion (Reciprocal Rank Fusion) not logged
- No visibility into which backend returned top K
- Milvus/ES downgrades silent
- No metrics for search performance

**Refactor Opportunity**:
```
rag/
├── hybrid.go             → Add SearchMetrics struct
├── metrics.go            → New: track backend usage, latency
├── search_trace.go       → New: per-result backend provenance
└── rag_test.go           → New: test RRF fusion weights
```

---

### 8. **LLM Client Should Support Multiple Models**

**Issue**:
- Single LLMModel in config
- Extractor, Agent, Consolidation all use same model
- No model routing per task type
- Fallback to mock is all-or-nothing

**Refactor Opportunity**:
```
llm/
├── client.go             → Multi-model support
├── model_registry.go     → New: store model capabilities
├── chat_strategies.go    → New: per-task model selection
├── embedding.go          → New: separate embedding model client
└── llm_test.go           → New: test model fallback chains
```

---

### 9. **Config Is a Flat 100+ Field Struct**

**Issue**:
- Hard to see which configs relate to which component
- No validation of config consistency (e.g., memory.consolidationTrigger > memory.shortTermMaxTurns)
- No config versioning or migration support
- Over-exposed in all packages

**Refactor Opportunity**:
```
config/
├── config.go             → Main loader + marshaling
├── api_config.go         → Struct definition only
├── validators.go         → New: semantic validation rules
├── db_config.go          → New: {milvus,pg,es,kafka} sub-configs
├── llm_config.go         → New: LLM + embedding model configs
├── memory_config.go      → New: Memory system configs
├── rag_config.go         → New: RAG + graph configs
├── sandbox_config.go     → New: Sandbox + security configs
└── config_test.go        → New: validation + migration tests
```

---

### 10. **No Central Logging / Observability**

**Issue**:
- Each package uses `log.Printf` independently
- No structured logging (JSON)
- No trace IDs for request correlation
- No metrics (Prometheus, etc.)

**Refactor Opportunity**:
```
pkg/
├── logging/
│   ├── logger.go         → Structured logger wrapper
│   ├── fields.go         → Typed log fields
│   └── logger_test.go
├── metrics/
│   ├── metrics.go        → Prometheus metrics registry
│   ├── counters.go       → Request counts, errors, etc.
│   └── gauges.go         → Queue depths, memory sizes
└── trace/
    ├── trace.go          → Request ID / trace context
    └── propagation.go    → Context.Context injection
```

---

## Refactoring Priority Matrix

| Package | Impact | Effort | Priority | Blocker? |
|---------|--------|--------|----------|----------|
| agent (split) | HIGH | MEDIUM | P0 | No |
| runtime (tests) | MEDIUM | MEDIUM | P1 | No |
| memory (semantics) | MEDIUM | MEDIUM | P1 | No |
| handler (versioning) | HIGH | LOW | P1 | No |
| sandbox (tests) | MEDIUM | LOW | P2 | No |
| graph (perf) | MEDIUM | HIGH | P2 | No |
| config (split) | LOW | MEDIUM | P2 | No |
| llm (multi-model) | MEDIUM | HIGH | P3 | No |
| rag (observability) | LOW | MEDIUM | P3 | No |
| observability (logging) | LOW | HIGH | P3 | No |

---

## Recommended Refactoring Phases

### Phase 1: Stabilize Core (1-2 weeks)
1. **Split agent.go** (1881 → 6 files)
   - Unblock mode routing tests
   - Prepare for MCP extensions
2. **Expand runtime tests**
   - Add schema rendering tests
   - Test concurrent slot filling

### Phase 2: Harden APIs (2 weeks)
1. **Version handler routes** (add `/api/v1/`)
2. **Add sandbox integration tests**
3. **Extract memory semantics** (RecallByFilter as single API)

### Phase 3: Observability (2-3 weeks)
1. **Structured logging** (replace log.Printf)
2. **Trace IDs** (per-request correlation)
3. **Metrics** (Prometheus counters/gauges)

### Phase 4: Optimization (3+ weeks)
1. **Neo4j query performance** (prepared statements, caching)
2. **Multi-model LLM support**
3. **Config file format** (YAML/TOML, validation)

---

## Immediate Actionable Next Steps

1. **This week**: Run `go test ./...` to establish baseline. Current: 4 test files only.
2. **This week**: Create `agent/router.go` extracting mode routing from agent.go.
3. **Next week**: Add test file for each handler route.
4. **Next week**: Add resource constraint tests to sandbox/.

---

## Dependency Order for Refactoring

**Do first** (enables others):
1. Split agent.go
2. Add observability hooks (logging, trace)
3. Version handler routes

**Then** (builds on above):
4. Expand runtime tests
5. Extract memory semantics
6. Sandbox integration tests

**Finally** (polish):
7. Config file format
8. Multi-model LLM
9. Neo4j optimization

---

## Risk Assessment

**Low Risk**:
- Splitting agent.go (refactor only, no logic change)
- Adding tests (tests don't affect behavior)
- Versioning routes (backward compat with `/api/chat` redirects)

**Medium Risk**:
- Runtime tests (may reveal concurrent bugs to fix)
- Memory semantics (consolidation changes could affect recall quality)
- Sandbox tests (may reveal Docker resource enforcement bugs)

**High Risk**:
- Config file format (migration path needed)
- Multi-model LLM (needs careful fallback logic)
- Neo4j optimization (parameterized queries = potential logic change)

---

