package memoryreconcile

import (
	"agi-assistant/internal/domain/memory/consistency"
	"context"
	"testing"
)

type source struct{ rows []consistency.MemoryRecord }

func (s source) LoadPage(context.Context, int64, int) ([]consistency.MemoryRecord, error) {
	return s.rows, nil
}

type target struct{ rows []consistency.ProjectionState }

func (t target) ListPage(context.Context, int64, int) ([]consistency.ProjectionState, error) {
	return t.rows, nil
}

type repairs struct{ events []consistency.OutboxEvent }

func (r *repairs) EnqueueRepair(_ context.Context, e consistency.OutboxEvent, _ string) (bool, error) {
	r.events = append(r.events, e)
	return true, nil
}
func TestReconcilerEnqueuesMissingAndStaleRepairs(t *testing.T) {
	q := &repairs{}
	r := New(source{[]consistency.MemoryRecord{{ID: 1, UserID: "u", Version: 2, ContentHash: "a"}, {ID: 2, UserID: "u", Version: 3, ContentHash: "b"}}}, target{[]consistency.ProjectionState{{ID: 2, Version: 2, ContentHash: "old"}}}, q, consistency.TargetMilvus, 100)
	report, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Missing != 1 || report.Stale != 1 || len(q.events) != 2 {
		t.Fatalf("report=%+v events=%d", report, len(q.events))
	}
}
