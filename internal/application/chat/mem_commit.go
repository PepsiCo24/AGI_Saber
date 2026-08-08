package chat

import (
	"context"
	"fmt"
	"time"

	"agi-assistant/internal/domain/memory/consistency"
	"agi-assistant/internal/infrastructure/persistence/memorytx"
	"agi-assistant/internal/pkg/logger"
)

// commitMemory persists a new memory and its outbox events before making the
// committed row visible in the local cache.
func (a *UnifiedAgent) commitMemory(ctx context.Context, cmd memorytx.CreateCommand) (consistency.MemoryRecord, error) {
	if a.repos == nil || a.repos.memoryTx == nil {
		return consistency.MemoryRecord{}, fmt.Errorf("memory commit repository unavailable")
	}
	changes, err := a.repos.memoryTx.Create(ctx, cmd)
	if err != nil {
		return consistency.MemoryRecord{}, err
	}
	if len(changes.Upserts) != 1 {
		return consistency.MemoryRecord{}, fmt.Errorf("memory commit returned %d upserts, want 1", len(changes.Upserts))
	}
	record := changes.Upserts[0]
	if err := a.mem.ltm.ApplyCommitted(changes); err != nil {
		active, loadErr := a.repos.memoryTx.LoadActive(ctx)
		if loadErr != nil {
			logger.C(ctx).Error("memory cache reload failed after committed apply conflict",
				"memory_id", record.ID, "version", record.Version,
				"apply_err", err, "load_err", loadErr)
			return record, nil
		}
		a.mem.ltm.ReplaceCommitted(active)
	}
	return record, nil
}

func (a *UnifiedAgent) consolidateCommitted(ctx context.Context) error {
	plan := a.mem.ltm.PlanConsolidation(time.Now())
	if len(plan.Updates) == 0 && len(plan.Deletes) == 0 {
		return nil
	}
	changes, err := a.repos.memoryTx.ApplyConsolidation(ctx, plan)
	if err != nil {
		return err
	}
	if err := a.mem.ltm.ApplyCommitted(changes); err != nil {
		active, loadErr := a.repos.memoryTx.LoadActive(ctx)
		if loadErr != nil {
			return loadErr
		}
		a.mem.ltm.ReplaceCommitted(active)
	}
	return nil
}
