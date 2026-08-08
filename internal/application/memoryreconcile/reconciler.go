package memoryreconcile

import (
	"agi-assistant/internal/domain/memory/consistency"
	"context"
	"encoding/json"
	"fmt"
)

type Source interface {
	LoadPage(context.Context, int64, int) ([]consistency.MemoryRecord, error)
}
type TargetReader interface {
	ListPage(context.Context, int64, int) ([]consistency.ProjectionState, error)
}
type RepairQueue interface {
	EnqueueRepair(context.Context, consistency.OutboxEvent, string) (bool, error)
}
type Report struct{ Checked, Missing, Stale, Orphan, RepairEnqueued int64 }
type Reconciler struct {
	source     Source
	target     TargetReader
	queue      RepairQueue
	targetName consistency.Target
	page       int
}

func New(s Source, t TargetReader, q RepairQueue, target consistency.Target, page int) *Reconciler {
	if page <= 0 {
		page = 500
	}
	return &Reconciler{s, t, q, target, page}
}
func (r *Reconciler) RunOnce(ctx context.Context) (Report, error) {
	var out Report
	pg, err := r.source.LoadPage(ctx, 0, r.page)
	if err != nil {
		return out, err
	}
	ts, err := r.target.ListPage(ctx, 0, r.page)
	if err != nil {
		return out, err
	}
	tm := map[int64]consistency.ProjectionState{}
	for _, s := range ts {
		tm[s.ID] = s
	}
	for _, m := range pg {
		out.Checked++
		s, ok := tm[m.ID]
		if !ok {
			out.Missing++
			if err = r.enqueue(ctx, m); err != nil {
				return out, err
			}
			out.RepairEnqueued++
		} else if s.Version != m.Version || s.ContentHash != m.ContentHash {
			out.Stale++
			if err = r.enqueue(ctx, m); err != nil {
				return out, err
			}
			out.RepairEnqueued++
		}
		delete(tm, m.ID)
	}
	for _, s := range tm {
		out.Orphan++
		m := consistency.MemoryRecord{ID: s.ID, Version: s.Version}
		b, _ := json.Marshal(m)
		typ := consistency.EventDeleteMemoryVector
		if r.targetName == consistency.TargetNeo4j {
			typ = consistency.EventDeleteMemoryGraphNode
		}
		e := consistency.OutboxEvent{AggregateID: m.ID, AggregateVersion: m.Version, Type: typ, Target: r.targetName, Payload: b}
		ok, er := r.queue.EnqueueRepair(ctx, e, fmt.Sprintf("%s:%d:%d:delete:repair", r.targetName, m.ID, m.Version))
		if er != nil {
			return out, er
		}
		if ok {
			out.RepairEnqueued++
		}
	}
	return out, nil
}
func (r *Reconciler) enqueue(ctx context.Context, m consistency.MemoryRecord) error {
	typ := consistency.EventUpsertMemoryVector
	if r.targetName == consistency.TargetNeo4j {
		typ = consistency.EventUpsertMemoryGraphNode
	}
	if m.DeletedAt != nil {
		typ = consistency.EventDeleteMemoryVector
		if r.targetName == consistency.TargetNeo4j {
			typ = consistency.EventDeleteMemoryGraphNode
		}
	}
	b, _ := json.Marshal(m)
	e := consistency.OutboxEvent{AggregateID: m.ID, UserID: m.UserID, AggregateVersion: m.Version, Type: typ, Target: r.targetName, Payload: b}
	_, err := r.queue.EnqueueRepair(ctx, e, fmt.Sprintf("%s:%d:%d:%s:repair", r.targetName, m.ID, m.Version, typ))
	return err
}
