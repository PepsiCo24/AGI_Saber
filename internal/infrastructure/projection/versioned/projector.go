package versioned

import (
	"agi-assistant/internal/domain/memory/consistency"
	"context"
	"encoding/json"
	"errors"
)

var ErrProjectionConflict = errors.New("projection: equal version hash conflict")

type Store interface {
	Get(context.Context, int64) (consistency.ProjectionState, bool, error)
	Upsert(context.Context, consistency.MemoryRecord) error
	Delete(context.Context, int64, int64) error
}
type Projector struct {
	Store       Store
	TargetValue consistency.Target
	Upserts     map[consistency.EventType]bool
	Deletes     map[consistency.EventType]bool
}

func (p *Projector) Target() consistency.Target { return p.TargetValue }
func (p *Projector) Apply(ctx context.Context, e consistency.OutboxEvent) error {
	var r consistency.MemoryRecord
	if err := json.Unmarshal(e.Payload, &r); err != nil {
		return err
	}
	current, ok, err := p.Store.Get(ctx, r.ID)
	if err != nil {
		return err
	}
	if ok && r.Version < current.Version {
		return nil
	}
	if p.Upserts[e.Type] {
		if ok && r.Version == current.Version {
			if r.ContentHash != current.ContentHash {
				return ErrProjectionConflict
			}
			return nil
		}
		return p.Store.Upsert(ctx, r)
	}
	if p.Deletes[e.Type] {
		if !ok {
			return nil
		}
		if r.Version < current.Version {
			return nil
		}
		return p.Store.Delete(ctx, r.ID, r.Version)
	}
	return nil
}
