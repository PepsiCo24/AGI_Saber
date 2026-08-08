package memoryprojection

import (
	"agi-assistant/internal/domain/memory/consistency"
	"context"
	"fmt"
	"time"
)

type Config struct {
	WorkerID    string
	BatchSize   int
	Lease       time.Duration
	MaxAttempts int
}

type Worker struct {
	repo      Outbox
	projector Projector
	cfg       Config
}

func NewWorker(repo Outbox, projector Projector, cfg Config) *Worker {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 10
	}
	if cfg.Lease <= 0 {
		cfg.Lease = 30 * time.Second
	}
	return &Worker{repo: repo, projector: projector, cfg: cfg}
}

func (w *Worker) RunOnce(ctx context.Context) error {
	events, err := w.repo.Claim(ctx, w.projector.Target(), w.cfg.WorkerID, w.cfg.BatchSize, w.cfg.Lease)
	if err != nil {
		return err
	}
	for _, event := range events {
		applyErr := safeApply(ctx, w.projector, event)
		if applyErr == nil {
			if err := w.repo.MarkProcessed(ctx, event.EventID); err != nil {
				return err
			}
			continue
		}
		if event.Attempts+1 >= w.cfg.MaxAttempts {
			if err := w.repo.MarkDead(ctx, event.EventID, applyErr.Error()); err != nil {
				return err
			}
		} else {
			backoff := time.Duration(1<<min(event.Attempts, 10)) * time.Second
			if err := w.repo.MarkRetry(ctx, event.EventID, applyErr.Error(), time.Now().Add(backoff)); err != nil {
				return err
			}
		}
	}
	return nil
}

func safeApply(ctx context.Context, p Projector, e consistency.OutboxEvent) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("projector panic: %v", r)
		}
	}()
	return p.Apply(ctx, e)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
