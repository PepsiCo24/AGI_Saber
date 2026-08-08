package memoryprojection

import (
	"context"
	"errors"
	"testing"
	"time"

	"agi-assistant/internal/domain/memory/consistency"
)

type fakeOutbox struct {
	events    []consistency.OutboxEvent
	processed []string
	retried   []string
	dead      []string
}

func (f *fakeOutbox) Claim(context.Context, consistency.Target, string, int, time.Duration) ([]consistency.OutboxEvent, error) {
	return f.events, nil
}
func (f *fakeOutbox) MarkProcessed(_ context.Context, id string) error {
	f.processed = append(f.processed, id)
	return nil
}
func (f *fakeOutbox) MarkRetry(_ context.Context, id, _ string, _ time.Time) error {
	f.retried = append(f.retried, id)
	return nil
}
func (f *fakeOutbox) MarkDead(_ context.Context, id, _ string) error {
	f.dead = append(f.dead, id)
	return nil
}

type fakeProjector struct{ err error }

func (f fakeProjector) Target() consistency.Target                           { return consistency.TargetMilvus }
func (f fakeProjector) Apply(context.Context, consistency.OutboxEvent) error { return f.err }

func TestWorkerMarksSuccessfulEventProcessed(t *testing.T) {
	repo := &fakeOutbox{events: []consistency.OutboxEvent{{EventID: "e1"}}}
	w := NewWorker(repo, fakeProjector{}, Config{WorkerID: "w1", BatchSize: 10, MaxAttempts: 3})
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.processed) != 1 || repo.processed[0] != "e1" {
		t.Fatalf("processed=%v", repo.processed)
	}
}

func TestWorkerRetriesFailureThenMarksDeadAtLimit(t *testing.T) {
	repo := &fakeOutbox{events: []consistency.OutboxEvent{{EventID: "e1", Attempts: 1}, {EventID: "e2", Attempts: 2}}}
	w := NewWorker(repo, fakeProjector{err: errors.New("down")}, Config{WorkerID: "w1", BatchSize: 10, MaxAttempts: 3})
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.retried) != 1 || repo.retried[0] != "e1" {
		t.Fatalf("retried=%v", repo.retried)
	}
	if len(repo.dead) != 1 || repo.dead[0] != "e2" {
		t.Fatalf("dead=%v", repo.dead)
	}
}
