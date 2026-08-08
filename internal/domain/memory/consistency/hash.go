package consistency

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

type contentHashInput struct {
	ID                int64     `json:"memory_id"`
	UserID            string    `json:"user_id"`
	Content           string    `json:"content"`
	Importance        float64   `json:"importance"`
	Embedding         []float64 `json:"embedding,omitempty"`
	EmbeddingModel    string    `json:"embedding_model,omitempty"`
	EmbeddingRevision string    `json:"embedding_revision,omitempty"`
	Category          string    `json:"category,omitempty"`
	Tags              []string  `json:"tags,omitempty"`
	SlotHint          string    `json:"slot_hint,omitempty"`
	Version           int64     `json:"version"`
	DeletedAt         string    `json:"deleted_at,omitempty"`
	Quarantined       bool      `json:"quarantined,omitempty"`
	Superseded        bool      `json:"superseded,omitempty"`
	Supersedes        []int64   `json:"supersedes,omitempty"`
}

// ComputeContentHash returns a deterministic digest of every field that
// changes a Milvus or Neo4j projection. Set-like slices are sorted on copies
// so semantically equivalent records hash equally without mutating callers.
func ComputeContentHash(record MemoryRecord) (string, error) {
	tags := append([]string(nil), record.Tags...)
	sort.Strings(tags)
	supersedes := append([]int64(nil), record.Supersedes...)
	sort.Slice(supersedes, func(i, j int) bool { return supersedes[i] < supersedes[j] })

	var deletedAt string
	if record.DeletedAt != nil {
		deletedAt = record.DeletedAt.UTC().Format(time.RFC3339Nano)
	}
	input := contentHashInput{
		ID:                record.ID,
		UserID:            record.UserID,
		Content:           record.Content,
		Importance:        record.Importance,
		Embedding:         append([]float64(nil), record.Embedding...),
		EmbeddingModel:    record.EmbeddingModel,
		EmbeddingRevision: record.EmbeddingRevision,
		Category:          record.Category,
		Tags:              tags,
		SlotHint:          record.SlotHint,
		Version:           record.Version,
		DeletedAt:         deletedAt,
		Quarantined:       record.Quarantined,
		Superseded:        record.Superseded,
		Supersedes:        supersedes,
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
