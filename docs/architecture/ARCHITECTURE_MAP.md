# AGI-Assistant Go Project Architecture Map

**Date:** 2026-06-10  
**Go Version:** 1.23.0  
**External Dependencies:** PostgreSQL, Milvus, Elasticsearch, Kafka, Neo4j (all gracefully degrade on connection failure)

---

## Main Entrypoint

### `main.go` (71 lines)

**Initialization Order:**
1. Load config via `config.DefaultConfig()` (reads `config/config.yaml`)
2. Initialize infrastructure via `infra.New(cfg)` — connects to Milvus, PostgreSQL, ES, Kafka
3. Create `UnifiedAgent` via `agent.New(cfg, inf)`
4. Register HTTP routes via `handler.New(a, inf, cfg)`
5. Serve frontend static files from `frontend/`
6. Listen on `cfg.ServerPort`

**Key Wiring:**
- All services constructed in sequence (infra → agent → handler)
- Infrastructure failures are logged but don't block startup (graceful degradation)
- HTTP routes are registered to default `http.ServeMux`

---

## Configuration Package

### `config/config.go` (373 lines)

**Exposed Struct:**
```go
type APIConfig struct {
  // LLM Configuration
  LLMAPIUrl, LLMAPIKey, LLMModel, Temperature
  
  // Embedding Configuration
  EmbeddingAPIUrl, EmbeddingAPIKey, EmbeddingModel
  
  // Infrastructure
  MilvusHost, MilvusPort
  PGHost, PGPort, PGUser, PGPassword, PGDatabase
  ESAddresses, ESUsername, ESPassword
  KafkaBrokers, KafkaTopic
  
  // RAG
  ChunkSize, ChunkOverlap, TopK, RRFConstantK, SemanticWeight
  EnableHybridSearch, RAGMilvusDim
  
  // Memory
  ShortTermMaxTurns, LongTermTopK
  MemoryConsolidation* (Similarity, Dedup, TTLDays, DecayRate, MinImport, Trigger)
  
  // Harness (Task Execution)
  MaxRetries, RetryDelayMs, StepTimeoutMs, MaxIterations
  
  // Search API
  SearchAPIKey, SearchAPIURL
  
  // Neo4j (Knowledge Graph)
  Neo4jURI, Neo4jUser, Neo4jPassword, KGMaxHops, KGWeight, KGEnabled
  
  // Sandbox (Command Execution)
  SandboxEnabled, SandboxBackend, SandboxImage, SandboxTimeoutMs
  SandboxMaxOutput, SandboxMemoryMB, SandboxCPUPercent, SandboxMaxPIDs
  SandboxNetDisabled, SandboxReadOnly
  
  // Security (Command Validation)
  SecMaxCmdLength, SecAllowlistMode, SecAllowlist[]
  
  // Server
  ServerPort
}
```

**Public Functions:**
- `DefaultConfig() *APIConfig` — loads from `config/config.yaml` with strict YAML parsing
- `(c *APIConfig) IsRealLLM() bool` — checks if LLM API key is configured
- `(c *APIConfig) IsRealEmbedding() bool` — checks if Embedding API key is configured
- `(c *APIConfig) PGDSN() string` — returns PostgreSQL connection string
- `(c *APIConfig) MilvusAddr() string` — returns Milvus address

---

## Internal Packages

### 1. `internal/infra` — Infrastructure Layer

**Files:**
- `infra.go` (847 lines)

**Exports:**

```go
type Infrastructure struct {
  Ready Status // {Milvus, PG, ES, Kafka connection status}
}

type Status struct {
  Milvus, PG, ES, Kafka string
}

type LongTermRow struct {
  ID int
  Content string
  Importance float64
  Embedding []byte
  CreatedAt time.Time
  LastAccessed time.Time
  Category, Tags, SlotHint string
}

type ChunkRow struct {
  ID int64
  DocHash string
  ChunkIdx int
  Content string
  Embedding []byte
  CreatedAt time.Time
}

type ESHit struct {
  ID, DocHash string
  ChunkIdx int
  PG_ID int64
  Score float64
}

type MilvusHit struct {
  ID int64
  Distance float32
}
```

**Key Public Methods:**
- `New(cfg *config.APIConfig) *Infrastructure`
- `SavePreference(userID, key, value string)`
- `LoadPreferences(userID) map[string]string`
- `SaveSnapshot(taskID string, stateJSON []byte)`
- `SaveLongTermItem(content, importance, embedding)`
- `SaveLongTermItemClassified(content, importance, embedding, category, tags, slotHint)`
- `LoadLongTermItems() []LongTermRow`
- `UpdateLongTermItem(id, content, importance, embedding)`
- `DeleteLongTermItems(ids []int)`
- `SaveRAGChunk(docHash, chunkIdx, content, embedding) (int64, error)`
- `LoadRAGChunksByIDs(ids) ([]ChunkRow, error)`
- `LoadAllRAGChunks() ([]ChunkRow, error)`
- `DeleteRAGChunksByDocHash(docHash) ([]int64, error)`
- `SearchES(index, queryJSON) (string, error)`
- `EnsureRAGIndex() error`
- `IndexRAGChunk(pgID, content, docHash, chunkIdx) error`
- `SearchRAGChunks(query, topK) ([]ESHit, error)`
- `MilvusSearch(collection, vector, topK) ([]int64, error)`
- `MilvusSearchWithScores(collection, vector, topK) ([]MilvusHit, error)`
- `EnsureRAGCollection(dim) error`
- `InsertRAGChunks(pgIDs, contents, embeddings) error`
- `InitRAGInfra(dim)`
- `PublishEvent(eventType, payload)`
- `SaveChatHistory(role, content)`
- `Close()`

**Imports from internal/:**
- None (base layer)

**Purpose:**
Unified connection manager for Milvus (vector DB), PostgreSQL (relational DB + long-term memory + preferences), Elasticsearch (full-text search), and Kafka (event publishing). All connections gracefully degrade on failure. Provides schema initialization, CRUD operations for memories and RAG chunks, and hybrid search backends.

---

### 2. `internal/llm` — LLM Client & Chat Interface

**Files:**
- `llm.go` (414 lines)

**Exports:**

```go
type Message struct {
  Role, Content string
}

type Client struct {
  // private: cfg, httpClient
}
```

**Key Public Methods:**
- `New(cfg *config.APIConfig) *Client`
- `(c *Client) Chat(systemPrompt string, messages []Message) string`
- `(c *Client) ChatContext(ctx context.Context, systemPrompt string, messages []Message) string`
- `(c *Client) ChatStreamContext(ctx context.Context, systemPrompt string, messages []Message, onToken func(string)) string`
- `(c *Client) Embedding(text string) ([]float64, error)` (inferred from codebase usage)

**Imports from internal/:**
- `config` (via parameter)

**External Dependencies:**
- `net/http` (for API calls)
- OpenAI-compatible API when `cfg.IsRealLLM()` == true
- Falls back to mock responses when API unavailable

**Purpose:**
Stateless LLM client supporting both sync and streaming chat interactions. Implements graceful fallback to mock responses. Supports context cancellation and timeout handling. Embedding function for RAG pipeline.

---

### 3. `internal/memory` — Three-Layer Memory System

**Files:**
- `memory.go` (400+ lines)
- `graph_memory.go` (Neo4j integration)

**Exports:**

```go
type ConversationMessage struct {
  Role, Content, Timestamp string
}

type ShortTerm struct {
  Messages []ConversationMessage
  MaxTurns int
}

type Item struct {
  ID int
  Content string
  Importance float64
  Embedding []float64
  Score float64
  CreatedAt, LastAccessed time.Time
  Category, Tags, SlotHint string
}

type LongTerm struct {
  // private: storage backend
}

type RecallFilter struct {
  Categories, RequireTags []string
  MinScore float64
  TopK, MaxAgeHours int
}

type GraphMemory struct {
  // Neo4j integration layer
}
```

**Key Public Methods (ShortTerm):**
- `NewShortTerm(maxTurns int) *ShortTerm`
- `(m *ShortTerm) Add(role, content string)`
- `(m *ShortTerm) Snapshot() []ConversationMessage` (with read lock)
- `(m *ShortTerm) Count() int`

**Key Public Methods (LongTerm):**
- `NewLongTerm(inf *infra.Infrastructure, llmClient *llm.Client) *LongTerm`
- `(lt *LongTerm) Store(content string, importance float64) error` (with auto-consolidation)
- `(lt *LongTerm) RecallByFilter(filter RecallFilter) ([]Item, error)` (semantic search)
- `(lt *LongTerm) Consolidate()` (dedup/merge/decay/expire)

**Imports from internal/:**
- `graph` (GraphMemory layer uses Neo4j)
- `infra` (storage backend: PostgreSQL, Milvus)
- `llm` (for embedding text)

**Purpose:**
Multi-layer memory architecture: short-term (sliding conversation window), long-term (persistent semantic store with auto-consolidation), and graph-based associations (Neo4j). Supports semantic recall filtering by category/tags and importance decay. Auto-merges similar memories and expires stale items.

---

### 4. `internal/rag` — Retrieval-Augmented Generation

**Files:**
- `rag.go`
- `hybrid.go` (RRF fusion logic)

**Exports:**

```go
type Chunk struct {
  ID int
  Content string
}

type TextSplitter struct {
  // private: chunkSize, overlap
}

type SearchResult struct {
  Chunk Chunk
  Similarity float64
}

type Engine struct {
  // RAG execution engine
}
```

**Key Public Methods (TextSplitter):**
- `NewTextSplitter(chunkSize, overlap int) *TextSplitter`
- `(s *TextSplitter) Split(text string) []Chunk` (Unicode-safe)

**Key Public Methods (Engine):**
- `NewEngine(inf *infra.Infrastructure, llmClient *llm.Client, cfg *config.APIConfig) *Engine`
- `(e *Engine) IndexDocument(docHash, content string) error` (chunks + embeds + indexes)
- `(e *Engine) Search(query string) ([]SearchResult, error)` (hybrid: Milvus semantic + ES BM25 + Neo4j KG + RRF fusion)
- `(e *Engine) GenerateAnswer(query string, results []SearchResult) string` (LLM synthesis)

**Imports from internal/:**
- `config`
- `infra` (Milvus, ES, PostgreSQL)
- `graph` (Neo4j KG indexing + retrieval)
- `llm` (embeddings + answer generation)

**External Dependencies:**
- Milvus (vector search)
- Elasticsearch (BM25 search)
- Neo4j (knowledge graph retrieval)

**Purpose:**
Retrieval-Augmented Generation pipeline. Chunks documents, generates embeddings, indexes in Milvus + Elasticsearch + Neo4j, and performs hybrid retrieval using RRF (Reciprocal Rank Fusion). Synthesizes LLM answers from retrieved context. All backends are optional (graceful degradation).

---

### 5. `internal/graph` — Knowledge Graph & Entity Extraction

**Files:**
- `types.go` (55 lines)
- `kgstore.go` (Neo4j store)
- `neo4j.go` (83 lines)
- `extractor.go` (LLM-based entity/relation extraction)

**Exports:**

```go
type EntityType string
const (
  EntityPerson, EntityOrg, EntityLocation, EntityConcept,
  EntityEvent, EntityProduct, EntityUnknown EntityType
)

type Entity struct {
  Name string
  Type EntityType
  DocHash string
  ChunkID int
  PGID int64
}

type Relation struct {
  FromName, ToName, RelType string
  Weight float64
  DocHash string
  ChunkID int
  PGID int64
}

type GraphSearchResult struct {
  ChunkID int
  PGID int64
  Score float64
  Entities []string
  HopPath []string
}

type ExtractResult struct {
  Entities []Entity
  Relations []Relation
}

type KGStore struct {
  // Neo4j integration
}

type Extractor struct {
  // LLM-based entity/relation extraction
}
```

**Key Public Methods (KGStore):**
- `NewKGStore(cfg *config.APIConfig) *KGStore`
- `(kg *KGStore) IndexDocument(docHash, content string, chunkID int, pgID int64) error` (extract + index)
- `(kg *KGStore) Search(query string, topK int) ([]GraphSearchResult, error)` (3-hop expansion)
- `(kg *KGStore) Close()`

**Key Public Methods (Extractor):**
- `NewExtractor(llmClient *llm.Client) *Extractor`
- `(ex *Extractor) Extract(text string) (ExtractResult, error)` (LLM extraction)

**Imports from internal/:**
- `llm` (for entity/relation extraction)

**External Dependencies:**
- Neo4j (graph database)

**Purpose:**
Knowledge graph construction and querying using Neo4j. Extracts entities and relations from documents via LLM, indexes them as graph nodes/edges, and performs semantic graph traversal (up to max hops) for enhanced retrieval. All operations gracefully degrade if Neo4j is unavailable.

---

### 6. `internal/runtime` — Schema-Driven Runtime Context Assembly

**Files:**
- `slot.go` (~70 lines)
- `schema.go` (~160 lines)
- `assembler.go` (~300 lines + tests)
- `context.go` (~150 lines)
- `source.go` (~50 lines)
- `source_planner.go`, `source_profile.go`, `source_recall.go`, `source_taskmem.go`, `source_tools.go` (source implementations)
- `source_constraints.go` (security constraints source)
- Tests: `assembler_test.go`, `source_profile_test.go`, `source_constraints_test.go`, `source_recall_test.go`

**Total:** ~1500 lines

**Exports:**

```go
type SlotKind string
const (
  SlotProfile, SlotPlanner, SlotTaskMem, SlotToolState,
  SlotConstraints, SlotRecall SlotKind
)

type SlotFilter struct {
  Categories, RequireTags []string
  MinScore float64
  TopK, MaxAgeHours, TokenBudget int
}

type Slot struct {
  Kind SlotKind
  Required bool
  Filter SlotFilter
  Template string
}

type ContextItem struct {
  Text, Source string
  Score float64
  Meta map[string]string
}

type FilledSlot struct {
  Kind SlotKind
  Items []ContextItem
  Skipped bool
  Reason string
}

type Query struct {
  Text string
  Embedding []float64
  TaskID string
  Mode string  // chat/tool/react/rag
}

type ContextSource interface {
  ID() string
  Supports(SlotKind) bool
  Fetch(ctx context.Context, slot Slot, q Query) ([]ContextItem, error)
}

type SourceRegistry struct {
  // manages per-SlotKind sources
}

type ContextAssembler struct {
  // fills slots from registered sources
}

// Predefined schemas
var ChatSchema, ToolSchema, ReactSchema, RagSchema RuntimeContextSchema
```

**Key Public Methods:**
- `NewSourceRegistry() *SourceRegistry`
- `(r *SourceRegistry) Register(source ContextSource)`
- `(r *SourceRegistry) Fetch(ctx context.Context, slot Slot, q Query) ([]ContextItem, error)`

- `NewContextAssembler(registry *SourceRegistry) *ContextAssembler`
- `(ca *ContextAssembler) Assemble(ctx context.Context, schema RuntimeContextSchema, q Query) ([]FilledSlot, error)`
- `(ca *ContextAssembler) Render(slots []FilledSlot) string` (format for LLM)

**Imports from internal/:**
- `memory` (profile + task memory sources)
- `infra` (for persistent retrieval)
- `rag` (recall source)
- `sandbox` (constraints source)
- `tools` (tool state source)

**Purpose:**
Mode-aware runtime context assembly system. Defines recognition slots (profile, planner, task memory, tool state, constraints, recall) and their fill strategies via pluggable `ContextSource` implementations. Each mode (chat/tool/react/rag) has a predefined schema. Sources fill slots respecting token budgets and category filters. Renders filled slots into LLM system prompt with source attribution for interpretability.

---

### 7. `internal/sandbox` — Safe Command Execution

**Files:**
- `types.go` (62 lines)
- `validator.go` (167 lines)
- `executor.go` (120+ lines)
- `docker.go` (115+ lines)
- `local.go` (108 lines)

**Exports:**

```go
type RiskLevel string
const (
  RiskSafe, RiskWarn, RiskBlock RiskLevel
)

type ValidationResult struct {
  Level RiskLevel
  Violations []string
  Reason string
}

type ExecRequest struct {
  Command string
  Timeout time.Duration
  Confirm bool
}

type ExecResult struct {
  Command string
  Validation ValidationResult
  Stdout, Stderr string
  ExitCode int
  Duration time.Duration
  Killed, Truncated bool
  Backend string  // docker | local | mock
}

type SandboxConfig struct {
  Image string
  Timeout time.Duration
  MaxOutputBytes, MemoryLimitMB, CPUPercent, MaxPIDs int
  NetworkDisabled, ReadOnlyRootfs bool
}

type SecurityConfig struct {
  MaxCommandLength int
  AllowlistMode bool
  Allowlist []string
}

type Validator struct {}
type Executor struct {}
```

**Key Public Methods (Validator):**
- `NewValidator(cfg SecurityConfig) *Validator`
- `(v *Validator) Validate(cmd string) ValidationResult` (static validation: length, pattern, allowlist)

**Key Public Methods (Executor):**
- `NewExecutor(cfg SandboxConfig) *Executor`
- `(ex *Executor) Exec(req ExecRequest) ExecResult` (execute in Docker/Local/Mock)

**Imports from internal/:**
- `config` (for sandbox settings)

**External Dependencies:**
- Docker (if backend == "docker")
- System shell (if backend == "local")

**Purpose:**
Secure sandbox command execution with three backends: Docker (container isolation), Local (system shell with resource limits), and Mock (stubbed responses). Validates commands statically (length, allowlist mode) with three risk levels (Safe/Warn/Block). Enforces resource limits (memory, CPU, PIDs, timeout, output). All operations are audit-logged.

---

### 8. `internal/tools` — Tool Definition & Invocation

**Files:**
- `tools.go` (200 lines)
- `exec_command.go` (102 lines)

**Exports:**

```go
type Param struct {
  Name, Type, Description string
  Required bool
}

type Tool struct {
  Name, Description string
  Parameters []Param
  IsMCP bool
  Execute func(params map[string]interface{}) (string, error)
}

type CallResult struct {
  ToolName string
  Params map[string]interface{}
  ToolResult string
}
```

**Key Public Methods:**
- `GetTime() Tool` (built-in: current time with timezone)
- `GetWeather() Tool` (built-in: mock weather by city)
- `SearchWeb() Tool` (built-in: mock keyword search)
- `ExecuteCommand(req sandbox.ExecRequest) Tool` (built-in: safe shell execution)
- `ToolRegistry struct` (agent-side tool registration)
- `(reg *ToolRegistry) Register(tool Tool)`
- `(reg *ToolRegistry) Call(toolName string, params map[string]interface{}) (CallResult, error)`

**Imports from internal/:**
- `sandbox` (for ExecuteCommand tool)

**Purpose:**
Tool registry and invocation framework. Defines built-in tools (time, weather, search, command execution) with JSON Schema parameters for LLM function-calling. Supports external MCP (Model Context Protocol) tools. Handles tool parameter validation and execution error handling.

---

### 9. `internal/agent` — Unified Agent Orchestrator

**Files:**
- `agent.go` (1600+ lines)

**Exports:**

```go
type StepType string
const (
  StepThought, StepAction, StepObservation, StepFinalAnswer StepType
)

type ReActStep struct {
  Type StepType
  Content string
  Tool, string
  Params map[string]string
}

type TaskStepStatus string
const (
  StepPending, StepRunning, StepDone, StepFailed, StepInterrupted TaskStepStatus
)

type TaskStep struct {
  ID int
  Name, ToolName string
  Params map[string]string
  Status TaskStepStatus
  Result, Error string
  RetryCount int
}

type TaskState struct {
  TaskID, Query, Status, Phase string
  Steps []TaskStep
  CurrentStep, InterruptedAt int
  Result string
}

type Snapshot struct {
  State TaskState
  Timestamp string
}

type Response struct {
  Message string
  Reasoning []ReActStep
  TaskID string
  InterruptReason string
  // additional fields...
}

type StreamEvent struct {
  Type string  // "thinking", "tool_call", "tool_result", "final_answer"
  Data interface{}
}

type ChatOptions struct {
  UseRAG bool
  SelectedTools []string
  Explicit bool
}

type UnifiedAgent struct {
  // private: cfg, inf, llmClient, memory, rag, tools, runtime
}
```

**Key Public Methods:**
- `New(cfg *config.APIConfig, inf *infra.Infrastructure) *UnifiedAgent`
- `(u *UnifiedAgent) Process(query string, opts ChatOptions) Response`
- `(u *UnifiedAgent) ProcessContext(ctx context.Context, query string, opts ChatOptions) Response` (context-aware)
- `(u *UnifiedAgent) ProcessStream(ctx context.Context, query string, opts ChatOptions, onEvent func(StreamEvent)) Response` (streaming)
- `(u *UnifiedAgent) InterruptTask(taskID string) error`
- `(u *UnifiedAgent) GetToolRegistry() *tools.ToolRegistry`
- `(u *UnifiedAgent) GetMemory() *memory.ShortTerm` (for chat history access)

**Imports from internal/:**
- `config`
- `infra`
- `llm`
- `memory` (ShortTerm + LongTerm)
- `graph` (KGStore)
- `rag` (Engine)
- `runtime` (ContextAssembler)
- `sandbox` (Executor)
- `tools` (ToolRegistry)

**Architecture (Routing by Priority):**
1. **ReAct + Harness** — Multi-step reasoning (2+ sub-tasks, complex queries)
2. **Tool Agent** — Single tool invocation (time/weather/search/command)
3. **RAG** — Knowledge base retrieval (if KB loaded + no tools triggered)
4. **Chat** — Direct LLM conversation

**Memory Injection:**
- User preferences + long-term memories → System Prompt (via runtime context assembly)
- Short-term context (recent messages) → Conversation history

**Execution Model:**
- ReAct loop: Thought → Action → Observation → Repeat (or Final Answer)
- Harness: Atomic task steps with retry logic and timeout handling
- Snapshot recovery: Persists task state to PostgreSQL for resumption

**Purpose:**
Central orchestrator integrating all 6 pipeline stages (LLM, Memory, RAG, Graph, Tools, Sandbox, Runtime). Implements multi-mode routing, ReAct reasoning with Harness-based task execution, graceful interruption handling, and full context assembly for each inference step. Supports sync, streaming, and context-cancellable interactions.

---

### 10. `internal/handler` — HTTP API Layer

**Files:**
- `handler.go` (280 lines)

**Exports:**

```go
type Server struct {
  // private: agent, infra, cfg
}
```

**Key Public Methods:**
- `New(a *agent.UnifiedAgent, inf *infra.Infrastructure, cfg *config.APIConfig) *Server`

**Registered Routes:**
- `POST /api/chat` — Sync chat request
- `POST /api/chat/stream` — SSE streaming chat
- `POST /api/chat/cancel` — Interrupt ongoing task
- `POST /api/upload` — Upload document for RAG indexing
- `POST /api/docs/delete` — Delete document from RAG index
- `GET /api/memory` — Retrieve memory state
- `GET /api/tools` — List available tools
- `POST /api/tools/mcp` — Register external MCP tool
- `GET /api/snapshots` — Retrieve task snapshots
- `GET /api/status` — System status (infra readiness)

**Imports from internal/:**
- `agent`
- `infra`
- `tools`
- `config`

**Purpose:**
HTTP request routing and response serialization. Delegates to `UnifiedAgent` for all logic. Handles streaming (SSE), context cancellation, error handling, and response formatting. Stateless: each request independently invokes Agent processing.

---

## Dependency Graph Summary

```
┌──────────────────────────────────────────────────────────────┐
│                       main.go                                │
└──────────────────────────────────────────────────────────────┘
         │                    │                    │
         ▼                    ▼                    ▼
    ┌────────────┐      ┌──────────┐      ┌────────────┐
    │   config   │      │  infra   │      │   agent    │
    └────────────┘      └──────────┘      └────────────┘
         ▲                    ▲                    │
         │                    │                    │
         └────────────────────┴────────────────────┘
                              │
         ┌────────────────────┼────────────────────┐
         │                    │                    │
         ▼                    ▼                    ▼
    ┌─────────┐          ┌────────┐          ┌────────┐
    │   llm   │          │ memory │          │  rag   │
    └─────────┘          └────────┘          └────────┘
         ▲                    │                    │
         │                    │                    │
         │              ┌─────┴─────┐              │
         │              ▼           ▼              │
         │          ┌───────┐  ┌────────┐         │
         │          │ graph │  │ infra  │◄────────┘
         │          └───────┘  └────────┘
         │              ▲           ▲
         │              │           │
         │     ┌────────┘           │
         │     │                    │
         └─────┼────────┬───────────┘
               │        │
               ▼        ▼
          ┌────────┐ ┌──────────┐
          │runtime │ │ sandbox  │
          └────────┘ └──────────┘
               │           │
               │           ▼
               │      ┌────────┐
               └─────►│ tools  │
                      └────────┘
                           │
                           ▼
                     ┌──────────┐
                     │ handler  │
                     └──────────┘
```

### Edge Directions (Imports):

| From     | To               | Purpose                                           |
|----------|------------------|---------------------------------------------------|
| agent    | config, infra, llm, memory, graph, rag, runtime, sandbox, tools | Central orchestration |
| handler  | config, infra, agent, tools | HTTP routing & delegation |
| rag      | config, infra, graph, llm | Document indexing & retrieval |
| runtime  | memory, infra, rag, sandbox, tools | Context assembly sources |
| memory   | graph, infra, llm | Persistence & graph associations |
| graph    | llm | Entity/relation extraction |
| sandbox  | config | Security validation & execution |
| tools    | sandbox | Command execution tool |
| llm      | config | API client configuration |
| infra    | config | Connection initialization |

---

## Test Coverage

**Test Files Found:**
- `internal/runtime/assembler_test.go`
- `internal/runtime/source_profile_test.go`
- `internal/runtime/source_constraints_test.go`
- `internal/runtime/source_recall_test.go`

All tests are in `runtime/` package, testing context assembly and slot filling logic.

---

## Graceful Degradation Strategy

All infrastructure connections fail gracefully:
- **Milvus down** → Fallback to TF-IDF (ES only) or memory vectors
- **PostgreSQL down** → In-memory storage (data lost on restart)
- **Elasticsearch down** → Fallback to Milvus semantic search only
- **Kafka down** → Events logged to stdout
- **Neo4j down** → RAG returns results without graph enrichment
- **LLM API down** → Mock responses (configurable)

---

## Configuration Cascade

1. `config/config.yaml` → parsed to `APIConfig`
2. Missing values → hardcoded defaults in `config.go`
3. Feature flags → `KGEnabled`, `SandboxEnabled`, `EnableHybridSearch`
4. Per-request overrides → `ChatOptions` (UseRAG, SelectedTools, Explicit)

---

## Key Design Patterns

1. **Dependency Injection:** All packages receive dependencies via constructor (config, infra, etc.)
2. **Plugin Architecture:** `ContextSource` interface allows runtime sources to be registered dynamically
3. **Graceful Degradation:** Infrastructure failures logged but don't block startup
4. **Schema-Driven:** Runtime context assembly driven by declarative slot schemas
5. **Streaming First:** SSE support for long-running operations (chat, tool execution)
6. **Audit Trail:** All tool calls, commands, and task steps logged/snapshotted
