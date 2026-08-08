# Long-Term Memory Eventual Consistency Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make PostgreSQL the only source of truth for long-term memory and reliably project committed changes to the in-process LongTerm cache, Milvus, and Neo4j through a transactional outbox, idempotent workers, and reconciliation.

**Architecture:** A new `memorytx.Repository` owns PostgreSQL transactions that change `long_term_memory` and insert `memory_outbox` rows atomically. The application computes candidate writes or consolidation plans without mutating shared memory, commits them through that repository, then applies the returned committed change set to the local cache. Independent outbox workers project versioned full-state events to Milvus and Neo4j; reconcilers compare `memory_id`, `version`, and `content_hash` and enqueue repair events through the same outbox.

**Tech Stack:** Go 1.24, `database/sql`, PostgreSQL, `lib/pq`, `go-sqlmock` for repository transaction tests, Milvus Go SDK v2, Neo4j Go Driver v5, existing Go test tooling.

## Global Constraints

- PostgreSQL is the only source of truth; LongTerm, Milvus, and Neo4j are rebuildable projections.
- Every authoritative memory mutation and its outbox rows commit in one PostgreSQL transaction.
- Projection delivery is at least once; every target operation must be idempotent.
- Every memory mutation increments a monotonic `version`; stale updates and deletes must not overwrite newer target state.
- Deletion uses a tombstone (`deleted_at`) until retention and reconciliation requirements are satisfied.
- The local LongTerm cache is updated only after PostgreSQL commit; cache-apply failure invalidates/reloads the affected scope.
- Milvus and Neo4j use independent worker pools so one target cannot block the other.
- Reconciliation never writes a target directly; it enqueues deduplicated repair outbox events.
- Existing unauthenticated/empty-user behavior remains unchanged: empty user IDs do not enter the long-term-memory write path.
- All production behavior changes follow red-green-refactor TDD.

---

## File and Responsibility Map

**New files**

- `internal/domain/memory/consistency/types.go` — committed memory record, mutation, outbox event, consolidation plan, hash input, and constants shared by application and infrastructure.
- `internal/domain/memory/consistency/hash.go` — deterministic canonical `content_hash`.
- `internal/domain/memory/consistency/hash_test.go` — hash stability and field-sensitivity tests.
- `internal/infrastructure/persistence/memorytx/repository.go` — PostgreSQL source-of-truth transactions for create, update, delete, and consolidation.
- `internal/infrastructure/persistence/memorytx/repository_test.go` — SQL transaction rollback, optimistic version, and atomic outbox tests.
- `internal/infrastructure/persistence/memoryoutbox/repository.go` — claim/ack/retry/dead/recover/repair-dedup persistence.
- `internal/infrastructure/persistence/memoryoutbox/repository_test.go` — outbox state-machine SQL tests.
- `internal/application/memoryprojection/worker.go` — target-neutral worker loop and retry policy.
- `internal/application/memoryprojection/worker_test.go` — duplicate delivery, retry, stale-lock, and cancellation tests.
- `internal/application/memoryprojection/projector.go` — projector interface and version outcome types.
- `internal/infrastructure/projection/milvusmemory/projector.go` — Milvus memory collection and idempotent vector projection.
- `internal/infrastructure/projection/milvusmemory/projector_test.go` — adapter-level version behavior with a fake store.
- `internal/infrastructure/projection/neo4jmemory/projector.go` — versioned node/edge projection.
- `internal/infrastructure/projection/neo4jmemory/projector_test.go` — Cypher intent and stale-event behavior with a fake executor.
- `internal/application/memoryreconcile/reconciler.go` — paged source/target comparison and repair event generation.
- `internal/application/memoryreconcile/reconciler_test.go` — missing, stale, orphan, and repair-dedup tests.
- `internal/application/chat/mem_commit.go` — application-level PG-first commit and local-cache application.
- `internal/application/chat/mem_commit_test.go` — commit failure does not mutate cache; commit success provides read-your-writes.

**Modified files**

- `internal/infrastructure/platform/postgres/postgres.go` — schema upgrades for versions, tombstones, hashes, and outbox.
- `internal/infrastructure/platform/postgres/postgres_test.go` — schema contract tests.
- `internal/domain/memory/longterm/longterm.go` — versioned committed-item cache APIs and pure consolidation planning.
- `internal/domain/memory/longterm/*_test.go` — committed apply, stale version, tombstone, and plan purity coverage.
- `internal/infrastructure/persistence/longterm/longterm.go` — read-only restore/audit compatibility while writes migrate to `memorytx`.
- `internal/application/chat/core_agent.go` — inject consistency dependencies and own background lifecycle.
- `internal/application/chat/infra_repos.go` — add transactional memory and outbox repositories.
- `internal/application/chat/mem_writer.go` — replace memory-first writes with commit-first writes.
- `internal/application/chat/runtime_process.go` — replace direct graph/PG memory writes and old consolidation synchronization.
- `internal/application/chat/mem_restore.go` — restore version/hash/tombstone-aware authoritative rows; stop rebuilding Neo4j directly.
- `internal/domain/memory/graph/graphmem.go` — remove direct fire-and-forget graph mutations from authoritative write paths.
- `internal/infrastructure/platform/neo4j/neo4j.go` — unique memory ID constraint and executor support.
- `config/config.go`, `config/config.yaml`, `config/config.docker.yaml` — worker batch, lease, retry, dead-letter, and reconciliation intervals.
- `cmd/server/main.go` — construct workers/projectors/reconcilers and stop them before closing clients.

---

### Task 1: Define Versioned Memory and Outbox Contracts

**Files:**
- Create: `internal/domain/memory/consistency/types.go`
- Create: `internal/domain/memory/consistency/hash.go`
- Create: `internal/domain/memory/consistency/hash_test.go`
- Modify: `internal/domain/memory/longterm/longterm.go`

**Interfaces:**
- Produces:
  - `type MemoryRecord struct { ID int64; UserID, Content string; Importance float64; Embedding []float64; Category string; Tags []string; SlotHint string; Version int64; ContentHash string; CreatedAt, UpdatedAt time.Time; DeletedAt *time.Time; Superseded bool; Supersedes []int64 }`
  - `type EventType string` with the seven event constants from the approved design.
  - `type Target string` with `milvus`, `neo4j`, and `ltm_cache`.
  - `type OutboxEvent struct { EventID string; AggregateID int64; UserID string; AggregateVersion int64; Type EventType; Target Target; Payload json.RawMessage; CreatedAt time.Time }`
  - `func ComputeContentHash(record MemoryRecord) (string, error)`

- [ ] **Step 1: Write failing deterministic-hash tests**

```go
func TestComputeContentHashIsStableAcrossTagOrder(t *testing.T) {
    a := consistency.MemoryRecord{ID: 7, UserID: "u1", Content: "用户喜欢咖啡", Version: 2, Tags: []string{"src:user", "preference"}}
    b := a
    b.Tags = []string{"preference", "src:user"}
    ah, err := consistency.ComputeContentHash(a)
    if err != nil { t.Fatal(err) }
    bh, err := consistency.ComputeContentHash(b)
    if err != nil { t.Fatal(err) }
    if ah != bh { t.Fatalf("hash must be canonical: %s != %s", ah, bh) }
}

func TestComputeContentHashChangesWithVersion(t *testing.T) {
    a := consistency.MemoryRecord{ID: 7, UserID: "u1", Content: "x", Version: 1}
    b := a
    b.Version = 2
    ah, _ := consistency.ComputeContentHash(a)
    bh, _ := consistency.ComputeContentHash(b)
    if ah == bh { t.Fatal("version must affect projection hash") }
}
```

- [ ] **Step 2: Run the tests and verify RED**

Run: `go test ./internal/domain/memory/consistency -run TestComputeContentHash -v`

Expected: FAIL because the package and contracts do not exist.

- [ ] **Step 3: Implement canonical types and SHA-256 hashing**

Canonicalize tags and supersedes by copying and sorting them; encode a private hash DTO with `json.Marshal`; return lowercase hex SHA-256. Do not mutate caller slices.

- [ ] **Step 4: Add projection metadata to `longterm.Item`**

Add `Version int64`, `ContentHash string`, `UpdatedAt time.Time`, and `DeletedAt *time.Time`. Keep zero values compatible with legacy rows, treating restored zero version as version 1.

- [ ] **Step 5: Run focused and domain tests**

Run:

```bash
go test ./internal/domain/memory/consistency -v
go test ./internal/domain/memory/longterm -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/domain/memory/consistency internal/domain/memory/longterm/longterm.go
git commit -m "feat: define versioned memory consistency contracts"
```

---

### Task 2: Add PostgreSQL Schema and Atomic Source-of-Truth Repository

**Files:**
- Modify: `internal/infrastructure/platform/postgres/postgres.go`
- Create: `internal/infrastructure/platform/postgres/postgres_test.go`
- Create: `internal/infrastructure/persistence/memorytx/repository.go`
- Create: `internal/infrastructure/persistence/memorytx/repository_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Consumes: `consistency.MemoryRecord`, `consistency.OutboxEvent`.
- Produces:

```go
type Repository interface {
    Create(ctx context.Context, cmd CreateCommand) (consistency.CommittedChangeSet, error)
    Update(ctx context.Context, cmd UpdateCommand) (consistency.CommittedChangeSet, error)
    Tombstone(ctx context.Context, cmd DeleteCommand) (consistency.CommittedChangeSet, error)
    ApplyConsolidation(ctx context.Context, plan consistency.ConsolidationPlan) (consistency.CommittedChangeSet, error)
    LoadActive(ctx context.Context) ([]consistency.MemoryRecord, error)
    LoadPage(ctx context.Context, afterID int64, limit int) ([]consistency.MemoryRecord, error)
}
```

`CommittedChangeSet` contains authoritative `Upserts []MemoryRecord`, `Deletes []MemoryRecord`, and the committed outbox event IDs. `ErrVersionConflict` is returned when an expected version no longer matches.

- [ ] **Step 1: Add `go-sqlmock` test dependency**

Run: `go get github.com/DATA-DOG/go-sqlmock@v1.5.2`

- [ ] **Step 2: Write failing schema contract test**

Test `MemoryConsistencyDDLs()` contains:

```go
required := []string{
    "ADD COLUMN IF NOT EXISTS version BIGINT NOT NULL DEFAULT 1",
    "ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ",
    "ADD COLUMN IF NOT EXISTS content_hash TEXT NOT NULL DEFAULT ''",
    "CREATE TABLE IF NOT EXISTS memory_outbox",
    "event_id UUID NOT NULL UNIQUE",
    "aggregate_version BIGINT NOT NULL",
}
```

- [ ] **Step 3: Verify schema test RED**

Run: `go test ./internal/infrastructure/platform/postgres -run TestMemoryConsistencyDDLs -v`

Expected: FAIL because `MemoryConsistencyDDLs` does not exist.

- [ ] **Step 4: Implement idempotent schema DDL**

Create `MemoryConsistencyDDLs() []string`, append it from `BootstrapSchema`, add ready/stale-lock/aggregate indexes, status and target checks, and backfill version/hash-compatible defaults.

- [ ] **Step 5: Write failing atomic-create repository tests**

Use `sqlmock` to expect:

1. `BEGIN`;
2. `INSERT INTO long_term_memory ... RETURNING id, version, ...`;
3. one vector outbox insert;
4. one graph-node outbox insert;
5. optional graph-edge outbox insert;
6. `COMMIT`.

Add a second test where the second outbox insert fails and expect `ROLLBACK` plus a returned error.

- [ ] **Step 6: Verify repository tests RED**

Run: `go test ./internal/infrastructure/persistence/memorytx -run 'TestRepositoryCreate' -v`

Expected: FAIL because `memorytx.Repository` is not implemented.

- [ ] **Step 7: Implement atomic create/update/tombstone**

Use `BeginTx(ctx, nil)`, named internal helpers receiving `*sql.Tx`, and `defer` rollback. Generate UUID event IDs without a new UUID dependency by using `crypto/rand` and RFC 4122 formatting. Compute the full-state payload and hash before writing outbox rows.

For update and tombstone SQL, include `WHERE id = $n AND version = $n`; translate zero affected rows into `ErrVersionConflict`.

- [ ] **Step 8: Run transaction and schema tests**

Run:

```bash
go test ./internal/infrastructure/platform/postgres -v
go test ./internal/infrastructure/persistence/memorytx -v
```

Expected: PASS, including rollback tests.

- [ ] **Step 9: Commit**

```bash
git add go.mod go.sum internal/infrastructure/platform/postgres internal/infrastructure/persistence/memorytx
git commit -m "feat: add transactional memory source of truth"
```

---

### Task 3: Make LongTerm a Committed-State Cache and Pure Planner

**Files:**
- Modify: `internal/domain/memory/longterm/longterm.go`
- Create: `internal/domain/memory/longterm/committed_test.go`
- Create: `internal/domain/memory/longterm/consolidation_plan_test.go`

**Interfaces:**
- Consumes: `consistency.CommittedChangeSet`.
- Produces:
  - `func (m *LongTerm) ApplyCommitted(changes consistency.CommittedChangeSet) error`
  - `func (m *LongTerm) ReplaceCommitted(items []consistency.MemoryRecord)`
  - `func (m *LongTerm) PlanConsolidation(now time.Time) consistency.ConsolidationPlan`
  - `var ErrStaleCommittedVersion error`

- [ ] **Step 1: Write failing committed-cache tests**

Cover:

```go
func TestApplyCommittedRejectsOlderVersion(t *testing.T) { /* cache has v3; apply v2; expect ErrStaleCommittedVersion and unchanged item */ }
func TestApplyCommittedTombstoneRemovesRecallableItem(t *testing.T) { /* apply v2 tombstone; RecallByFilter must not return it */ }
func TestReplaceCommittedDropsLegacyLocalOnlyItems(t *testing.T) { /* authoritative replacement contains only PG rows */ }
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/domain/memory/longterm -run 'TestApplyCommitted|TestReplaceCommitted' -v`

Expected: FAIL because committed-state APIs do not exist.

- [ ] **Step 3: Implement committed cache application**

Apply changes under one write lock. Upsert by authoritative ID, reject a lower version, make equal-version/equal-hash a no-op, and reject equal-version/different-hash. Rebuild vocabulary once after the batch.

- [ ] **Step 4: Write failing pure-plan test**

Take a snapshot, call `PlanConsolidation`, then assert the LongTerm snapshot is byte-for-byte unchanged while the plan contains expected updates/deletes and their `ExpectedVersion`.

- [ ] **Step 5: Verify plan test RED**

Run: `go test ./internal/domain/memory/longterm -run TestPlanConsolidationDoesNotMutateCache -v`

Expected: FAIL because planning is not separated from mutation.

- [ ] **Step 6: Extract pure consolidation planning**

Reuse existing similarity, merge, decay, TTL, and protect rules but operate on a deep copy. Keep the old `Consolidate` temporarily as a deprecated adapter for unaffected tests; no application write path may call it after Task 7.

- [ ] **Step 7: Run all memory-domain tests**

Run: `go test ./internal/domain/memory/... -v`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/domain/memory/longterm
git commit -m "refactor: make long-term memory a committed-state cache"
```

---

### Task 4: Switch New Memory Writes to PostgreSQL First

**Files:**
- Create: `internal/application/chat/mem_commit.go`
- Create: `internal/application/chat/mem_commit_test.go`
- Modify: `internal/application/chat/core_agent.go`
- Modify: `internal/application/chat/infra_repos.go`
- Modify: `internal/application/chat/mem_writer.go`
- Modify: `internal/application/chat/runtime_process.go`
- Modify: `internal/application/chat/mem_restore.go`
- Modify: `internal/infrastructure/persistence/longterm/longterm.go`
- Modify: `cmd/server/main.go`

**Interfaces:**
- Consumes: `memorytx.Repository`, `LongTerm.ApplyCommitted`.
- Produces:

```go
func (a *UnifiedAgent) commitMemory(ctx context.Context, cmd memorytx.CreateCommand) (consistency.MemoryRecord, error)
```

`Deps` gains `MemoryTxRepo memorytx.Repository`. The old `LTMRepo` remains read-only during migration and is removed only after all write call sites move.

- [ ] **Step 1: Write failing application tests**

Use a fake `memorytx.Repository`:

- repository returns an error: assert cache count and graph projector calls remain zero;
- repository returns committed v1 row: assert cache immediately contains the authoritative PG ID and version;
- cache apply returns stale/equal-hash result: assert PG success is returned and the affected cache is reloaded rather than reversed.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/application/chat -run TestCommitMemory -v`

Expected: FAIL because `commitMemory` and the dependency do not exist.

- [ ] **Step 3: Implement commit-first application service**

`commitMemory` calls PostgreSQL first. Only a successful `CommittedChangeSet` is applied locally. On cache apply failure, call `LoadActive`, then `ReplaceCommitted`. Return errors to the extraction task for accurate warning logs.

- [ ] **Step 4: Replace both direct memory write paths**

Change:

- `runMemExtractAndStore` in `mem_writer.go`;
- preference extraction in `runtime_process.go`.

Neither path may call `graphMem.Store*`, `ltm.StoreClassified`, `ltm.SyncLastItemPGID`, or `ltmrepo.Save*`.

- [ ] **Step 5: Make restore authoritative and version-aware**

Load active plus tombstone/audit metadata through `memorytx.LoadActive`, then call `ReplaceCommitted`. Remove startup graph-node recreation; reconciliation/outbox owns graph projection.

- [ ] **Step 6: Verify no forbidden write calls remain**

Run:

```bash
rg -n 'graphMem\\.Store|ltm\\.StoreClassified|SyncLastItemPGID|repos\\.ltm\\.Save' internal/application/chat
```

Expected: no authoritative runtime write matches. Test fixtures may still call domain methods directly.

- [ ] **Step 7: Run chat and memory tests**

Run:

```bash
go test ./internal/application/chat -v
go test ./internal/domain/memory/... -v
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add cmd/server/main.go internal/application/chat internal/infrastructure/persistence/longterm
git commit -m "refactor: persist long-term memory before cache projection"
```

---

### Task 5: Implement the Durable Outbox State Machine and Worker

**Files:**
- Create: `internal/infrastructure/persistence/memoryoutbox/repository.go`
- Create: `internal/infrastructure/persistence/memoryoutbox/repository_test.go`
- Create: `internal/application/memoryprojection/projector.go`
- Create: `internal/application/memoryprojection/worker.go`
- Create: `internal/application/memoryprojection/worker_test.go`

**Interfaces:**
- Produces:

```go
type OutboxRepository interface {
    Claim(ctx context.Context, target consistency.Target, workerID string, limit int, lease time.Duration) ([]consistency.OutboxEvent, error)
    MarkProcessed(ctx context.Context, eventID string) error
    MarkRetry(ctx context.Context, eventID, message string, availableAt time.Time) error
    MarkDead(ctx context.Context, eventID, message string) error
    RecoverExpired(ctx context.Context, target consistency.Target, before time.Time) (int64, error)
    EnqueueRepair(ctx context.Context, event consistency.OutboxEvent, dedupeKey string) (bool, error)
}

type Projector interface {
    Target() consistency.Target
    Apply(ctx context.Context, event consistency.OutboxEvent) error
}
```

- [ ] **Step 1: Write failing repository state-machine tests**

With `sqlmock`, verify claim uses `FOR UPDATE SKIP LOCKED`, changes rows to processing in the same short transaction, and `MarkRetry` clears the lease while incrementing attempts.

- [ ] **Step 2: Verify repository RED**

Run: `go test ./internal/infrastructure/persistence/memoryoutbox -v`

Expected: FAIL because repository is absent.

- [ ] **Step 3: Implement outbox repository**

Use bounded claims, lease recovery, and `INSERT ... ON CONFLICT (repair_dedupe_key) DO NOTHING` for repair events. Add the nullable unique repair key column to Task 2 schema if not already present.

- [ ] **Step 4: Write failing worker tests**

Test:

- success calls `Apply` then `MarkProcessed`;
- target failure schedules exponential backoff with deterministic injected clock/random source;
- max attempts marks dead;
- canceled context exits promptly;
- panic in a projector becomes retry/dead instead of terminating the process.

- [ ] **Step 5: Verify worker RED**

Run: `go test ./internal/application/memoryprojection -v`

Expected: FAIL because worker is absent.

- [ ] **Step 6: Implement worker**

Inject `Clock`, `Jitter`, repository, projector, batch size, poll interval, lease, and max attempts. Keep one worker instance target-specific. Never hold SQL locks during `Projector.Apply`.

- [ ] **Step 7: Run focused tests with race detector**

Run:

```bash
go test -race ./internal/infrastructure/persistence/memoryoutbox ./internal/application/memoryprojection
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/infrastructure/persistence/memoryoutbox internal/application/memoryprojection internal/infrastructure/platform/postgres/postgres.go
git commit -m "feat: add durable memory outbox workers"
```

---

### Task 6: Add Idempotent Milvus and Neo4j Memory Projectors

**Files:**
- Create: `internal/infrastructure/projection/milvusmemory/projector.go`
- Create: `internal/infrastructure/projection/milvusmemory/projector_test.go`
- Create: `internal/infrastructure/projection/neo4jmemory/projector.go`
- Create: `internal/infrastructure/projection/neo4jmemory/projector_test.go`
- Modify: `internal/infrastructure/platform/neo4j/neo4j.go`
- Modify: `internal/domain/memory/graph/graphmem.go`

**Interfaces:**
- Consumes: `memoryprojection.Projector`, versioned full-state outbox payloads.
- Produces:
  - `milvusmemory.New(client, dimension) memoryprojection.Projector`
  - `neo4jmemory.New(executor) memoryprojection.Projector`
  - target readers used later by reconciliation:

```go
type ProjectionReader interface {
    ListPage(ctx context.Context, afterID int64, limit int) ([]consistency.ProjectionState, error)
}
```

- [ ] **Step 1: Write failing projector behavior tests**

Use small fake target adapters and cover:

- v2 upsert followed by v1 upsert leaves v2;
- v2 upsert followed by v1 delete leaves v2;
- same version/same hash is successful no-op;
- same version/different hash returns `ErrProjectionConflict`;
- deleting a missing target object succeeds.

- [ ] **Step 2: Verify projector tests RED**

Run:

```bash
go test ./internal/infrastructure/projection/milvusmemory ./internal/infrastructure/projection/neo4jmemory -v
```

Expected: FAIL because projector packages do not exist.

- [ ] **Step 3: Implement Milvus memory collection**

Create a dedicated collection such as `long_term_memory_vectors` with stable `memory_id` primary key, `user_id`, `version`, `content_hash`, vector, and filtering metadata. Do not reuse the RAG chunk collection. Implement upsert/delete and page-reading through a narrow adapter so behavior tests do not require a live Milvus.

- [ ] **Step 4: Implement Neo4j versioned node and edge projection**

Replace the non-unique index with a unique constraint on `Memory.memory_id`. Use conditional Cypher so stored newer versions win. Node deletion must be version-guarded and idempotent; edge events include endpoint IDs, edge type, version, and hash.

- [ ] **Step 5: Remove direct graph side effects**

`GraphMemory` remains a read facade during migration but must not start goroutines that mutate Neo4j from `StoreClassified`, consolidation, restore, or ID synchronization. All such writes flow through outbox projectors.

- [ ] **Step 6: Run projector and graph tests**

Run:

```bash
go test ./internal/infrastructure/projection/... -v
go test ./internal/domain/memory/graph ./internal/domain/memory/longterm -v
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/infrastructure/projection internal/infrastructure/platform/neo4j internal/domain/memory/graph
git commit -m "feat: add versioned memory projections"
```

---

### Task 7: Make Consolidation PostgreSQL-Transactional

**Files:**
- Modify: `internal/infrastructure/persistence/memorytx/repository.go`
- Modify: `internal/infrastructure/persistence/memorytx/repository_test.go`
- Modify: `internal/application/chat/runtime_process.go`
- Modify: `internal/application/chat/mem_writer.go`
- Create: `internal/application/chat/mem_consolidation_test.go`

**Interfaces:**
- Consumes: `LongTerm.PlanConsolidation`, `memorytx.Repository.ApplyConsolidation`.
- Produces:

```go
func (a *UnifiedAgent) consolidateCommitted(ctx context.Context) error
```

- [ ] **Step 1: Write failing SQL transaction tests**

Verify one consolidation transaction:

- locks every affected row with `FOR UPDATE`;
- validates expected versions;
- updates survivor and increments version;
- tombstones/supersedes removed rows and increments their versions;
- maintains the `supersedes` audit chain;
- inserts vector/node/edge upsert and delete events;
- rolls everything back when any version mismatches or any outbox insert fails.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/infrastructure/persistence/memorytx -run TestApplyConsolidation -v`

Expected: FAIL because `ApplyConsolidation` is incomplete.

- [ ] **Step 3: Implement atomic consolidation apply**

Sort affected IDs before locking to avoid deadlocks. Bound a transaction to a configured maximum batch. Return `ErrVersionConflict` for stale plans and the exact committed change set on success.

- [ ] **Step 4: Write failing application orchestration test**

Assert:

- plan calculation alone does not change cache;
- repository error leaves cache unchanged;
- successful commit applies the returned authoritative rows;
- version conflict retries by reloading once and replanning, then stops with a logged error if conflict repeats.

- [ ] **Step 5: Verify application RED**

Run: `go test ./internal/application/chat -run TestConsolidateCommitted -v`

Expected: FAIL because orchestration is absent.

- [ ] **Step 6: Replace old consolidation path**

In `finalize`, call `consolidateCommitted` from the existing safe background wrapper. Remove `syncConsolidationToDB` and all calls to mutating `LongTerm.Consolidate`/`GraphAwareConsolidate` from application code.

- [ ] **Step 7: Verify forbidden old flow is gone**

Run:

```bash
rg -n 'syncConsolidationToDB|GraphAwareConsolidate|\\.Consolidate\\(' internal/application/chat
```

Expected: no matches.

- [ ] **Step 8: Run transaction, application, and race tests**

Run:

```bash
go test ./internal/infrastructure/persistence/memorytx ./internal/application/chat -v
go test -race ./internal/domain/memory/... ./internal/application/chat
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/infrastructure/persistence/memorytx internal/application/chat
git commit -m "feat: commit memory consolidation atomically"
```

---

### Task 8: Add Reconciliation Through Repair Outbox Events

**Files:**
- Create: `internal/application/memoryreconcile/reconciler.go`
- Create: `internal/application/memoryreconcile/reconciler_test.go`
- Modify: `internal/infrastructure/persistence/memorytx/repository.go`
- Modify: `internal/infrastructure/persistence/memoryoutbox/repository.go`

**Interfaces:**
- Consumes: source `LoadPage`, target `ProjectionReader`, `OutboxRepository.EnqueueRepair`.
- Produces:

```go
type Reconciler struct { /* source, target reader, outbox, target, page size, clock */ }
func (r *Reconciler) RunOnce(ctx context.Context) (Report, error)
type Report struct { Checked, Missing, Stale, Orphan, RepairEnqueued int64 }
```

- [ ] **Step 1: Write failing reconciliation tests**

Table-drive these cases:

| PG state | Target state | Expected repair |
|---|---|---|
| active v2/hash A | missing | upsert v2 |
| active v3/hash B | v2/hash A | upsert v3 |
| active v3/hash B | v3/hash C | upsert v3 plus conflict metric |
| tombstone v4 | active v3 | delete v4 |
| absent after complete scan | active v1 | delete repair |
| identical | identical | none |

Run the same mismatch twice and assert the second `EnqueueRepair` reports not inserted due to dedupe key.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/application/memoryreconcile -v`

Expected: FAIL because reconciler is absent.

- [ ] **Step 3: Implement merge-style paged comparison**

Compare sorted `memory_id` pages without loading all data in memory. Build repair events from current PG state. Never invoke target write APIs directly.

- [ ] **Step 4: Add Neo4j edge reconciliation**

Represent graph edge projection state with `(from_id, to_id, edge_type, version, hash)`. Detect missing, stale, and dangling edges and enqueue the corresponding graph-edge repair events.

- [ ] **Step 5: Run reconciliation tests**

Run: `go test -race ./internal/application/memoryreconcile -v`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/application/memoryreconcile internal/infrastructure/persistence/memorytx internal/infrastructure/persistence/memoryoutbox
git commit -m "feat: reconcile memory projections through outbox"
```

---

### Task 9: Wire Lifecycle, Configuration, and Graceful Shutdown

**Files:**
- Modify: `config/config.go`
- Modify: `config/config.yaml`
- Modify: `config/config.docker.yaml`
- Modify: `cmd/server/main.go`
- Modify: `internal/application/chat/core_agent.go`
- Modify: `internal/application/chat/infra_cancel.go`
- Create: `internal/application/chat/memory_background_test.go`

**Interfaces:**
- Produces exact configuration fields:
  - `MemoryOutboxBatchSize` default `100`
  - `MemoryOutboxPollInterval` default `500ms`
  - `MemoryOutboxLease` default `30s`
  - `MemoryOutboxMaxAttempts` default `10`
  - `MemoryReconcileInterval` default `6h`
  - `MemoryReconcilePageSize` default `500`
  - `MemoryConsolidationMaxBatch` default `100`
  - `MemoryTombstoneRetentionDays` default `30`

- [ ] **Step 1: Write failing lifecycle test**

Construct background components with fake workers that record start/stop. Assert `StartMemoryConsistency` starts separate Milvus, Neo4j, and reconciliation loops; assert `Close` cancels and joins all loops before clients are closed.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/application/chat -run TestMemoryBackgroundLifecycle -v`

Expected: FAIL because lifecycle methods do not exist.

- [ ] **Step 3: Add parsed configuration with defaults**

Use `time.Duration` values in `APIConfig` and YAML-friendly string fields in the intermediate YAML struct. Validate positive batch/page sizes and retention longer than the maximum retry/reconciliation window.

- [ ] **Step 4: Wire repositories, projectors, workers, and reconcilers**

Create the consistency stack in `cmd/server/main.go` after PG/Milvus clients are connected. Start it only after `chat.New` finishes authoritative restore. During shutdown: stop HTTP, cancel/join memory background tasks, then close Milvus, Neo4j, PostgreSQL, and Kafka.

- [ ] **Step 5: Expose health metrics in existing status snapshot**

Add per-target pending/dead/oldest-pending and last-reconciliation fields without changing existing keys. Logging must include `event_id`, `memory_id`, `user_id`, `version`, `target`, and `attempts`.

- [ ] **Step 6: Run config, chat, and server tests**

Run:

```bash
go test ./config ./internal/application/chat ./cmd/server -v
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add config cmd/server internal/application/chat
git commit -m "feat: wire memory consistency background services"
```

---

### Task 10: Fault Injection, Full Verification, and Migration Cleanup

**Files:**
- Create: `internal/application/memoryprojection/fault_test.go`
- Create: `internal/infrastructure/persistence/memorytx/fault_test.go`
- Modify: `docs/solve/长期记忆更改涉及多库如何保证一致性.md` only if implementation names differ from the approved names
- Modify: obsolete direct-write files identified by the forbidden-pattern checks

**Interfaces:**
- Consumes all prior components.
- Produces no new public API; this task proves recovery and removes compatibility code.

- [ ] **Step 1: Add fault-injection tests**

Cover failure at:

- before PG commit;
- after PG commit but before cache apply;
- after target apply but before outbox acknowledgement;
- during worker processing with lease expiry;
- during reconciliation repair enqueue.

Assertions must prove PostgreSQL remains authoritative and a subsequent retry/reload converges.

- [ ] **Step 2: Add duplicate and out-of-order stream tests**

Feed `upsert v1`, `upsert v2`, duplicate `upsert v2`, delayed `delete v1`, and `delete v3`; assert the final target is deleted at v3 and no v1 operation regresses v2.

- [ ] **Step 3: Run focused fault tests**

Run:

```bash
go test -race ./internal/application/memoryprojection ./internal/application/memoryreconcile ./internal/infrastructure/persistence/memorytx -v
```

Expected: PASS.

- [ ] **Step 4: Remove old write compatibility APIs**

Remove production uses of:

- `LongTerm.SyncLastItemPGID`;
- direct `GraphMemory` store/update/delete goroutines;
- write methods on the legacy `longterm.Repo`;
- `syncConsolidationToDB`.

Keep restore/audit reads in a clearly named read repository or move them to `memorytx`.

- [ ] **Step 5: Run forbidden-pattern checks**

Run:

```bash
rg -n 'SyncLastItemPGID|graphMem\\.Store|syncConsolidationToDB|repos\\.ltm\\.(Save|Update|Delete)' internal --glob '*.go'
```

Expected: no production matches.

- [ ] **Step 6: Run formatting and full verification**

Run:

```bash
gofmt -w internal config cmd
go vet ./...
go test ./...
go test -race ./internal/domain/memory/... ./internal/application/chat ./internal/application/memoryprojection ./internal/application/memoryreconcile
git diff --check
```

Expected: every command exits 0 with no race report.

- [ ] **Step 7: Review design-to-implementation consistency**

Verify the implementation covers:

- PG-only source of truth;
- transactional outbox;
- all vector/node/edge upsert/delete events;
- idempotency and stale-version rejection;
- transactional consolidation;
- independent workers and dead letters;
- missing/stale/orphan reconciliation;
- cache read-your-writes and reload;
- tombstone retention;
- graceful shutdown and observability.

- [ ] **Step 8: Commit**

```bash
git add .
git commit -m "test: verify eventual consistency recovery"
```

---

## Implementation Checkpoints

1. **After Task 2:** PostgreSQL can atomically commit memory plus outbox, but runtime traffic still uses the old path.
2. **After Task 4:** All new memory writes are PG-first and local cache is read-your-writes; target projection is not yet active.
3. **After Task 6:** Milvus and Neo4j converge through durable workers; direct graph writes are gone.
4. **After Task 7:** Consolidation is fully transactional and no longer mutates memory before persistence.
5. **After Task 9:** Reconciliation and worker lifecycle are production-wired.
6. **After Task 10:** Fault injection and full regression/race verification establish completion.
