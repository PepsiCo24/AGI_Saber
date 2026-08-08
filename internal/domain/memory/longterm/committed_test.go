package longterm

import (
	"errors"
	"testing"
	"time"

	"agi-assistant/internal/domain/memory/consistency"
)

func TestApplyCommittedRejectsOlderVersion(t *testing.T) {
	memory := New()
	memory.StoreItem(Item{
		ID:          41,
		UserID:      "u1",
		Content:     "v3",
		Version:     3,
		ContentHash: "hash-v3",
	})

	err := memory.ApplyCommitted(consistency.CommittedChangeSet{
		Upserts: []consistency.MemoryRecord{{
			ID:          41,
			UserID:      "u1",
			Content:     "v2",
			Version:     2,
			ContentHash: "hash-v2",
		}},
	})
	if !errors.Is(err, ErrStaleCommittedVersion) {
		t.Fatalf("error=%v, want ErrStaleCommittedVersion", err)
	}
	item, ok := memory.FindByID(41)
	if !ok || item.Content != "v3" || item.Version != 3 {
		t.Fatalf("stale update changed cache: %+v, ok=%v", item, ok)
	}
}

func TestApplyCommittedTombstoneRemovesItem(t *testing.T) {
	memory := New()
	memory.StoreItem(Item{
		ID:          41,
		UserID:      "u1",
		Content:     "active",
		Version:     1,
		ContentHash: "hash-v1",
	})
	deletedAt := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	err := memory.ApplyCommitted(consistency.CommittedChangeSet{
		Deletes: []consistency.MemoryRecord{{
			ID:          41,
			UserID:      "u1",
			Version:     2,
			ContentHash: "delete-v2",
			DeletedAt:   &deletedAt,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := memory.FindByID(41); ok {
		t.Fatal("tombstoned item remains in active cache")
	}
}

func TestReplaceCommittedDropsLocalOnlyAndDeletedRows(t *testing.T) {
	memory := New()
	memory.StoreItem(Item{ID: 1, UserID: "u1", Content: "local-only", Version: 1})
	deletedAt := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	memory.ReplaceCommitted([]consistency.MemoryRecord{
		{ID: 2, UserID: "u1", Content: "authoritative", Version: 4, ContentHash: "hash-v4"},
		{ID: 3, UserID: "u1", Content: "deleted", Version: 2, DeletedAt: &deletedAt},
	})

	if memory.Count() != 1 {
		t.Fatalf("count=%d, want 1", memory.Count())
	}
	if _, ok := memory.FindByID(1); ok {
		t.Fatal("local-only item survived authoritative replacement")
	}
	item, ok := memory.FindByID(2)
	if !ok || item.Version != 4 {
		t.Fatalf("authoritative item missing: %+v, ok=%v", item, ok)
	}
	if _, ok := memory.FindByID(3); ok {
		t.Fatal("deleted row entered active cache")
	}
}

func TestApplyCommittedEqualVersionDifferentHashFails(t *testing.T) {
	memory := New()
	memory.StoreItem(Item{ID: 41, UserID: "u1", Content: "cached", Version: 2, ContentHash: "hash-a"})

	err := memory.ApplyCommitted(consistency.CommittedChangeSet{
		Upserts: []consistency.MemoryRecord{{
			ID:          41,
			UserID:      "u1",
			Content:     "different",
			Version:     2,
			ContentHash: "hash-b",
		}},
	})
	if !errors.Is(err, ErrCommittedHashConflict) {
		t.Fatalf("error=%v, want ErrCommittedHashConflict", err)
	}
}
