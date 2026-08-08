package neo4jmemory

import (
	"agi-assistant/internal/domain/memory/consistency"
	"context"
	"encoding/json"
	"testing"
)

type fakeStore struct{ state consistency.ProjectionState }

func (f *fakeStore) Get(context.Context, int64) (consistency.ProjectionState, bool, error) {
	return f.state, f.state.ID != 0, nil
}
func (f *fakeStore) Upsert(_ context.Context, r consistency.MemoryRecord) error {
	f.state = consistency.ProjectionState{ID: r.ID, Version: r.Version, ContentHash: r.ContentHash}
	return nil
}
func (f *fakeStore) Delete(_ context.Context, id, version int64) error {
	f.state = consistency.ProjectionState{}
	return nil
}
func TestStaleUpsertDoesNotOverwriteNewerNode(t *testing.T) {
	s := &fakeStore{state: consistency.ProjectionState{ID: 1, Version: 3, ContentHash: "v3"}}
	p := New(s)
	b, _ := json.Marshal(consistency.MemoryRecord{ID: 1, Version: 2, ContentHash: "v2"})
	if err := p.Apply(context.Background(), consistency.OutboxEvent{Type: consistency.EventUpsertMemoryGraphNode, Payload: b}); err != nil {
		t.Fatal(err)
	}
	if s.state.Version != 3 {
		t.Fatal("stale upsert overwrote v3")
	}
}
