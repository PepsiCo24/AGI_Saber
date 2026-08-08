package chat

import (
	"context"
	"errors"
	"testing"

	"agi-assistant/internal/domain/memory/consistency"
	"agi-assistant/internal/domain/memory/longterm"
	"agi-assistant/internal/infrastructure/persistence/memorytx"
)

type fakeMemoryStore struct {
	createChanges consistency.CommittedChangeSet
	createErr     error
	active        []consistency.MemoryRecord
	loadErr       error
}

func (f *fakeMemoryStore) Create(context.Context, memorytx.CreateCommand) (consistency.CommittedChangeSet, error) {
	return f.createChanges, f.createErr
}

func (f *fakeMemoryStore) Update(context.Context, memorytx.UpdateCommand) (consistency.CommittedChangeSet, error) {
	return consistency.CommittedChangeSet{}, errors.New("not used")
}

func (f *fakeMemoryStore) Tombstone(context.Context, memorytx.DeleteCommand) (consistency.CommittedChangeSet, error) {
	return consistency.CommittedChangeSet{}, errors.New("not used")
}

func (f *fakeMemoryStore) LoadActive(context.Context) ([]consistency.MemoryRecord, error) {
	return f.active, f.loadErr
}

func (f *fakeMemoryStore) LoadPage(context.Context, int64, int) ([]consistency.MemoryRecord, error) {
	return nil, errors.New("not used")
}
func (f *fakeMemoryStore) ApplyConsolidation(context.Context, consistency.ConsolidationPlan) (consistency.CommittedChangeSet, error) {
	return consistency.CommittedChangeSet{}, errors.New("not used")
}

func TestCommitMemoryFailureDoesNotMutateCache(t *testing.T) {
	cache := longterm.New()
	store := &fakeMemoryStore{createErr: errors.New("postgres unavailable")}
	agent := &UnifiedAgent{
		mem:   &memoryStack{ltm: cache},
		repos: &repoBundle{memoryTx: store},
	}

	_, err := agent.commitMemory(context.Background(), memorytx.CreateCommand{
		UserID:  "u1",
		Content: "用户喜欢咖啡",
	})

	if err == nil {
		t.Fatal("commitMemory succeeded after PostgreSQL failure")
	}
	if cache.Count() != 0 {
		t.Fatalf("cache changed before commit: count=%d", cache.Count())
	}
}

func TestCommitMemoryAppliesAuthoritativeIDAndVersionAfterCommit(t *testing.T) {
	cache := longterm.New()
	store := &fakeMemoryStore{
		createChanges: consistency.CommittedChangeSet{
			Upserts: []consistency.MemoryRecord{{
				ID:          91,
				UserID:      "u1",
				Content:     "用户喜欢咖啡",
				Version:     4,
				ContentHash: "hash-v4",
			}},
		},
	}
	agent := &UnifiedAgent{
		mem:   &memoryStack{ltm: cache},
		repos: &repoBundle{memoryTx: store},
	}

	record, err := agent.commitMemory(context.Background(), memorytx.CreateCommand{
		UserID:  "u1",
		Content: "用户喜欢咖啡",
	})
	if err != nil {
		t.Fatal(err)
	}

	if record.ID != 91 || record.Version != 4 {
		t.Fatalf("returned non-authoritative record: %+v", record)
	}
	item, ok := cache.FindByID(91)
	if !ok || item.Version != 4 || item.ContentHash != "hash-v4" {
		t.Fatalf("committed row not visible in cache: %+v, ok=%v", item, ok)
	}
}

func TestCommitMemoryReloadsAuthoritativeCacheAfterApplyConflict(t *testing.T) {
	cache := longterm.New()
	cache.StoreItem(longterm.Item{
		ID: 91, UserID: "u1", Content: "cache-v5", Version: 5, ContentHash: "hash-v5",
	})
	store := &fakeMemoryStore{
		createChanges: consistency.CommittedChangeSet{
			Upserts: []consistency.MemoryRecord{{
				ID: 91, UserID: "u1", Content: "committed-v4", Version: 4, ContentHash: "hash-v4",
			}},
		},
		active: []consistency.MemoryRecord{{
			ID: 92, UserID: "u1", Content: "reloaded-v6", Version: 6, ContentHash: "hash-v6",
		}},
	}
	agent := &UnifiedAgent{
		mem:   &memoryStack{ltm: cache},
		repos: &repoBundle{memoryTx: store},
	}

	if _, err := agent.commitMemory(context.Background(), memorytx.CreateCommand{
		UserID: "u1", Content: "committed-v4",
	}); err != nil {
		t.Fatal(err)
	}

	if _, ok := cache.FindByID(91); ok {
		t.Fatal("conflicting cache was not replaced from PostgreSQL")
	}
	item, ok := cache.FindByID(92)
	if !ok || item.Version != 6 {
		t.Fatalf("authoritative reload missing: %+v, ok=%v", item, ok)
	}
}
