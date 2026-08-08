package longterm

import (
	"reflect"
	"testing"
	"time"
)

func TestPlanConsolidationDoesNotMutateCache(t *testing.T) {
	memory := New()
	memory.SetConsolidationConfig(&ConsolidationConfig{
		SimilarityThreshold: 0.8,
		DedupThreshold:      0.95,
		TTLDays:             30,
		DecayRate:           1,
		MinImportance:       0.3,
		TriggerInterval:     1,
	})
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	memory.StoreItem(Item{
		ID: 1, UserID: "u1", Content: "用户喜欢咖啡", Importance: 0.8,
		Embedding: []float64{1, 0}, CreatedAt: now.Add(-time.Hour),
		Version: 2, ContentHash: "hash-1",
	})
	memory.StoreItem(Item{
		ID: 2, UserID: "u1", Content: "用户非常喜欢咖啡", Importance: 0.7,
		Embedding: []float64{1, 0}, CreatedAt: now.Add(-time.Hour),
		Version: 5, ContentHash: "hash-2",
	})
	before := memory.Snapshot()

	plan := memory.PlanConsolidation(now)

	after := memory.Snapshot()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("planning mutated cache:\nbefore=%+v\nafter=%+v", before, after)
	}
	if len(plan.Deletes) != 1 {
		t.Fatalf("deletes=%d, want 1: %+v", len(plan.Deletes), plan)
	}
	if plan.Deletes[0].ID != 2 || plan.Deletes[0].ExpectedVersion != 5 {
		t.Fatalf("delete lacks authoritative identity/version: %+v", plan.Deletes[0])
	}
}

func TestPlanConsolidationKeepsHigherImportanceRecordID(t *testing.T) {
	memory := New()
	memory.SetConsolidationConfig(&ConsolidationConfig{
		SimilarityThreshold: 0.8,
		DedupThreshold:      0.99,
		TTLDays:             30,
		DecayRate:           1,
		MinImportance:       0.3,
		TriggerInterval:     1,
	})
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	memory.StoreItem(Item{
		ID: 1, UserID: "u1", Content: "偏好咖啡", Importance: 0.5,
		Embedding: []float64{1, 0}, CreatedAt: now, Version: 2,
	})
	memory.StoreItem(Item{
		ID: 2, UserID: "u1", Content: "非常偏好咖啡", Importance: 0.9,
		Embedding: []float64{0.9, 0.435889894}, CreatedAt: now, Version: 7,
	})

	plan := memory.PlanConsolidation(now)

	if len(plan.Updates) != 1 || plan.Updates[0].Record.ID != 2 || plan.Updates[0].ExpectedVersion != 7 {
		t.Fatalf("higher-importance survivor was not updated: %+v", plan)
	}
	if len(plan.Deletes) != 1 || plan.Deletes[0].ID != 1 || plan.Deletes[0].ExpectedVersion != 2 {
		t.Fatalf("lower-importance row was not deleted: %+v", plan)
	}
}
