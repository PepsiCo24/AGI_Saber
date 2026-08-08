package memoryprojection

import (
	"agi-assistant/internal/domain/memory/consistency"
	"context"
	"time"
)

type Outbox interface {
	Claim(context.Context, consistency.Target, string, int, time.Duration) ([]consistency.OutboxEvent, error)
	MarkProcessed(context.Context, string) error
	MarkRetry(context.Context, string, string, time.Time) error
	MarkDead(context.Context, string, string) error
}

type Projector interface {
	Target() consistency.Target
	Apply(context.Context, consistency.OutboxEvent) error
}
