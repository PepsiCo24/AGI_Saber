// Package consistency defines the contracts shared by the authoritative
// PostgreSQL memory store and its rebuildable projections.
package consistency

import (
	"encoding/json"
	"time"
)

type EventType string

const (
	EventUpsertMemoryVector     EventType = "upsert_memory_vector"
	EventDeleteMemoryVector     EventType = "delete_memory_vector"
	EventUpsertMemoryGraphNode  EventType = "upsert_memory_graph_node"
	EventDeleteMemoryGraphNode  EventType = "delete_memory_graph_node"
	EventUpsertMemoryGraphEdges EventType = "upsert_memory_graph_edges"
	EventDeleteMemoryGraphEdges EventType = "delete_memory_graph_edges"
	EventInvalidateLTMCache     EventType = "invalidate_ltm_cache"
)

type Target string

const (
	TargetMilvus   Target = "milvus"
	TargetNeo4j    Target = "neo4j"
	TargetLTMCache Target = "ltm_cache"
)

// MemoryRecord is the authoritative representation of a long-term-memory row.
// Score is intentionally absent because it is computed only during recall.
type MemoryRecord struct {
	ID                int64      `json:"memory_id"`
	UserID            string     `json:"user_id"`
	Content           string     `json:"content"`
	Importance        float64    `json:"importance"`
	Embedding         []float64  `json:"embedding,omitempty"`
	EmbeddingModel    string     `json:"embedding_model,omitempty"`
	EmbeddingRevision string     `json:"embedding_revision,omitempty"`
	Category          string     `json:"category,omitempty"`
	Tags              []string   `json:"tags,omitempty"`
	SlotHint          string     `json:"slot_hint,omitempty"`
	Version           int64      `json:"version"`
	ContentHash       string     `json:"content_hash"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	LastAccessed      time.Time  `json:"last_accessed"`
	DeletedAt         *time.Time `json:"deleted_at,omitempty"`
	Quarantined       bool       `json:"quarantined,omitempty"`
	QuarantineReason  string     `json:"quarantine_reason,omitempty"`
	Superseded        bool       `json:"superseded,omitempty"`
	SupersededAt      *time.Time `json:"superseded_at,omitempty"`
	Supersedes        []int64    `json:"supersedes,omitempty"`
}

type OutboxEvent struct {
	ID               int64           `json:"id,omitempty"`
	EventID          string          `json:"event_id"`
	AggregateID      int64           `json:"aggregate_id"`
	UserID           string          `json:"user_id"`
	AggregateVersion int64           `json:"aggregate_version"`
	Type             EventType       `json:"event_type"`
	Target           Target          `json:"target"`
	Payload          json.RawMessage `json:"payload"`
	Attempts         int             `json:"attempts,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
}

type CommittedChangeSet struct {
	Upserts  []MemoryRecord `json:"upserts,omitempty"`
	Deletes  []MemoryRecord `json:"deletes,omitempty"`
	EventIDs []string       `json:"event_ids,omitempty"`
}

type MemoryUpdate struct {
	Record          MemoryRecord `json:"record"`
	ExpectedVersion int64        `json:"expected_version"`
}

type MemoryDelete struct {
	ID              int64  `json:"memory_id"`
	UserID          string `json:"user_id"`
	ExpectedVersion int64  `json:"expected_version"`
	Reason          string `json:"reason,omitempty"`
}

type ConsolidationPlan struct {
	Updates []MemoryUpdate `json:"updates,omitempty"`
	Deletes []MemoryDelete `json:"deletes,omitempty"`
}

type ProjectionState struct {
	ID          int64  `json:"memory_id"`
	Version     int64  `json:"version"`
	ContentHash string `json:"content_hash"`
	Deleted     bool   `json:"deleted"`
}
