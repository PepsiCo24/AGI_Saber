package postgres

import (
	"strings"
	"testing"
)

func TestMemoryConsistencyDDLsCreateAuthoritativeVersionAndOutbox(t *testing.T) {
	joined := strings.Join(strings.Fields(strings.Join(MemoryConsistencyDDLs(), "\n")), " ")
	required := []string{
		"ADD COLUMN IF NOT EXISTS version BIGINT NOT NULL DEFAULT 1",
		"ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()",
		"ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ",
		"ADD COLUMN IF NOT EXISTS content_hash TEXT NOT NULL DEFAULT ''",
		"CREATE TABLE IF NOT EXISTS memory_outbox",
		"event_id UUID NOT NULL UNIQUE",
		"aggregate_version BIGINT NOT NULL",
		"CHECK (status IN ('pending', 'processing', 'processed', 'dead'))",
		"CHECK (target IN ('milvus', 'neo4j', 'ltm_cache'))",
	}
	for _, want := range required {
		if !strings.Contains(joined, want) {
			t.Errorf("schema is missing %q", want)
		}
	}
}
