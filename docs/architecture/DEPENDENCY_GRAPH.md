# Dependency Graph Visualization

## High-Level Layers

```
┌─────────────────────────────────────────────────────────────┐
│                     HTTP Handler Layer                      │
│                    handler/ (280 lines)                     │
│         Routes: /api/chat, /api/memory, /api/docs, etc      │
└────────────────────────────┬────────────────────────────────┘
                             │ imports
                             ▼
┌─────────────────────────────────────────────────────────────┐
│                  Core Orchestration Hub                     │
│                   agent/ (1881 lines)                       │
│    Routes requests via 4 modes: ReAct, Tool, RAG, Chat     │
└────────────────────┬────────────────────────────────────────┘
                     │ imports (all functional layers below)
        ┌────────────┼─────────────────────┐
        │            │                     │
        ▼            ▼                     ▼
    ┌────────┐  ┌────────┐          ┌─────────────┐
    │ llm/   │  │ memory/│          │ runtime/    │
    │ 414L   │  │ 1053L  │          │ 1123L       │
    └────────┘  └────────┘          │ (w/ tests)  │
        │            │               └─────────────┘
        │            │                     │
        └────────────┼─────────────────────┘
                     │ all import
                     ▼
        ┌────────────┬────────────┐
        │            │            │
        ▼            ▼            ▼
    ┌────────┐  ┌────────┐  ┌──────────┐
    │ graph/ │  │ rag/   │  │ sandbox/ │
    │ 705L   │  │ 581L   │  │ 632L     │
    └────────┘  └────────┘  └──────────┘
        │            │            │
        └────────────┼────────────┘
                     │ all import
                     ▼
┌──────────────────────────────────────────────────────────────┐
│                Infrastructure Layer                         │
│               infra/ (847 lines)                             │
│    Manages: Milvus, PostgreSQL, Elasticsearch, Kafka       │
│    (Graceful degradation on connection failure)             │
└──────────────────────────────────────────────────────────────┘
        │
        └──────────────── also used by tools/
                              ▼
                        ┌──────────┐
                        │ tools/   │
                        │ 302L     │
                        └──────────┘
                             │
                             └─ imports: stdlib only
                                        config
```

---

## Detailed Dependency Matrix

```
Package         Imports From           Used By            Notes
─────────────────────────────────────────────────────────────────
config          • stdlib yaml          • all 11 packages   Central configuration
                • stdlib               
                                       
infra           • config               • agent             Infrastructure abstraction
                • stdlib               • rag               Gracefully degrades
                • DB drivers           • memory            
                                       
llm             • config               • agent             LLM + Embedding API
                • stdlib               • graph             Falls back to mock
                                       • rag
                                       • memory
                                       
graph           • config               • agent             Neo4j-backed KG
                • stdlib               • rag               Entity extraction
                • neo4j driver         • memory            Extractor uses LLM
                                       
rag             • config               • agent             Hybrid search
                • graph                                    RRF fusion (semantic+BM25+KG)
                • infra                                    
                • stdlib               
                                       
memory          • graph (only in       • agent             3-layer: Short/Long/Pref
                  graph_memory.go)     • runtime           Consolidation logic
                • stdlib               
                                       
runtime         • memory               • agent             Schema-driven prompt assembly
                • stdlib               • no tests to add   ContextSource interface
                                       
sandbox         • stdlib               • agent             Validate → Exec → Audit
                • docker SDK           • tools             Docker/Local/Mock backends
                • config types         
                                       
tools           • stdlib               • agent             Tool registry + built-ins
                • config types         • handler           MCP support
                                       
handler         • config               • main.go           HTTP route handlers
                • agent                                    SSE streaming support
                • infra                
                • tools                
                • stdlib               
```

---

## Import Flow Example: User Message → Response

```
main.go
  └─ config.DefaultConfig()
  └─ infra.New(cfg)
  └─ agent.New(cfg, inf)
  └─ handler.New(agent, inf, cfg)
  └─ http.ListenAndServe()

User sends: POST /api/chat
  │
  └─ handler.chat()
       └─ agent.ProcessContext(ctx, message, opts)
            │
            ├─ memory.stm.Add(message)
            ├─ memory.ltm.RecallTop(message)  ─┐
            │                                    │
            ├─ runtime.assembler.Assemble()     │ System Prompt
            │    ├─ runtime.SourceProfile.Fetch()    │ Construction
            │    ├─ runtime.SourceRecall.Fetch()     │
            │    └─ [other slots]                ─┘
            │
            ├─ agent.decide(mode)  ← routing based on complexity
            │
            ├─ MODE: Chat
            │    └─ llm.ChatStreamContext(systemPrompt, messages)
            │
            ├─ MODE: Tool
            │    ├─ agent.runTool(toolName, params)
            │    └─ sandbox.Exec(req)
            │         ├─ sandbox.validator.Validate()
            │         ├─ sandbox.executor.Exec()  (Docker|Local|Mock)
            │         └─ [audit callback]
            │
            ├─ MODE: RAG
            │    ├─ rag.Search(query, topK)
            │    │    ├─ HybridStore.Search()
            │    │    │   ├─ infra.SearchMilvus() (semantic)
            │    │    │   ├─ infra.SearchES() (BM25)
            │    │    │   ├─ graph.Search() (KG subgraph)
            │    │    │   └─ RRF fusion
            │    │    └─ rag.generateFn()  (LLM)
            │    └─ llm.ChatStreamContext(systemPrompt + RAG results, messages)
            │
            ├─ MODE: ReAct + Harness
            │    └─ [complex multi-step reasoning loop]
            │
            └─ handler.chatStream()  ← SSE events to client
                 └─ json.Marshal(response)
```

---

## Package Dependency Depth

```
Depth 0 (Foundation):
  config/

Depth 1 (Infrastructure):
  infra/
  llm/

Depth 2 (Functional):
  graph/
  sandbox/
  tools/
  rag/

Depth 3 (Semantic):
  memory/
  runtime/

Depth 4 (Orchestration):
  agent/

Depth 5 (HTTP):
  handler/

Depth 6 (Main):
  main.go
```

**Maximum depth**: 6 layers (config → main)
**No cycles detected**: Fully acyclic

---

## Critical Paths

### Path 1: Document Ingestion (Ingest → RAG → KG)
```
infra.SaveRAGChunk() → rag.Index() → {
  infra.SaveMilvus(),
  infra.SaveES(),
  graph.IndexDocument()  ← async, LLM-based extraction
}
```

### Path 2: Query Processing (Assemble → Route → Execute)
```
agent.ProcessContext() → {
  runtime.Assemble() → llm.ChatContext() (for context),
  agent.decide(mode) → {
    MODE_CHAT → llm.ChatStreamContext(),
    MODE_TOOL → sandbox.Exec() + tools.Execute(),
    MODE_RAG → rag.Search() → llm.ChatStreamContext(),
    MODE_REACT → [complex loop using llm + tools + memory]
  }
}
```

### Path 3: Memory Consolidation (Store → Merge → Decay)
```
memory.ltm.Store() → memory.ltm.Consolidate() {
  Dedup: RecallByFilter(highSimilarity),
  Merge: combine similar items,
  Decay: importance *= decayRate,
  Expire: delete old + low-importance,
  OptionalGraphUpdate: graph_memory.Store()
}
```

### Path 4: Sandbox Execution (Validate → Execute → Audit)
```
agent.runTool(exec_command) → sandbox.Exec() {
  validator.Validate() → {RiskSafe|RiskWarn|RiskBlock},
  if Safe/Warn: executor.Exec() (Docker|Local|Mock),
  auditFn() → infra.PublishAudit() → Kafka
}
```

---

## Testing Coverage by Layer

```
Layer               Files  LOC   Tests   Coverage Status
─────────────────────────────────────────────────────
config              1      372   0       ⚠️  Needs config validation tests
infra               1      847   0       ⚠️  Needs mock backends + integration
llm                 1      414   0       ⚠️  Needs fallback + streaming tests
graph               4      705   0       ⚠️  Needs query injection + perf bench
rag                 2      581   0       ⚠️  Needs RRF fusion + hybrid mode tests
memory              2      1053  0       ⚠️  Needs consolidation + graph tests
runtime             10     1123  4✓      ✓ Partial (assembler, sources)
sandbox             5      632   0       ⚠️  Needs Docker + Local backend tests
tools               2      302   0       ⚠️  Needs tool execution tests
handler             1      280   0       ⚠️  Needs route + streaming tests
agent               1      1881  0       ⚠️  Needs mode routing tests
─────────────────────────────────────────────────────
TOTAL               33     8099  4✓      ~0.1% estimated
```

**Test Status**: 4 test files out of 33 files (12% coverage by count)
**Recommendation**: Add tests for critical paths (agent modes, sandbox, rag search)

---

## Potential Refactoring Impact Map

```
If you split agent.go (1881L):
  ├─ Affects: agent/*, handler, runtime
  ├─ Risk: Low (refactor only, no logic change)
  └─ Enables: Easier testing of individual modes

If you expand runtime tests:
  ├─ Affects: runtime/*_test.go
  ├─ Risk: Medium (may reveal concurrent bugs)
  └─ Enables: Safer schema modifications

If you version handler routes (/api/v1/):
  ├─ Affects: handler, client contracts
  ├─ Risk: Low (add routes, keep old ones)
  └─ Enables: Backward compatibility

If you add memory consolidation tests:
  ├─ Affects: memory/memory.go
  ├─ Risk: Medium (may show dedup/merge issues)
  └─ Enables: Safer memory tuning

If you add Neo4j query parameterization:
  ├─ Affects: graph/neo4j.go, kgstore.go
  ├─ Risk: Medium (logic change in queries)
  └─ Enables: Query injection prevention + perf gain
```

