package memoryoutbox

import (
	"agi-assistant/internal/domain/memory/consistency"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/lib/pq"
	"time"
)

type Repository struct{ db *sql.DB }

func New(db *sql.DB) *Repository { return &Repository{db: db} }

func (r *Repository) Claim(ctx context.Context, target consistency.Target, worker string, limit int, lease time.Duration) (events []consistency.OutboxEvent, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	rows, err := tx.QueryContext(ctx, `SELECT id,event_id,aggregate_id,user_id,aggregate_version,event_type,target,payload,attempts,created_at FROM memory_outbox WHERE status='pending' AND target=$1 AND available_at<=NOW() ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED`, string(target), limit)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var e consistency.OutboxEvent
		var typ, targetText string
		var payload []byte
		if err = rows.Scan(&e.ID, &e.EventID, &e.AggregateID, &e.UserID, &e.AggregateVersion, &typ, &targetText, &payload, &e.Attempts, &e.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		e.Type = consistency.EventType(typ)
		e.Target = consistency.Target(targetText)
		e.Payload = json.RawMessage(payload)
		events = append(events, e)
		ids = append(ids, e.ID)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		_, err = tx.ExecContext(ctx, `UPDATE memory_outbox SET status = 'processing', locked_by=$1, locked_at=$2 WHERE id = ANY($3)`, worker, time.Now(), pq.Array(ids))
		if err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return events, nil
}
func (r *Repository) MarkProcessed(ctx context.Context, id string) error {
	return r.exec(ctx, `UPDATE memory_outbox SET status='processed',processed_at=NOW(),locked_at=NULL,locked_by=NULL WHERE event_id=$1`, id)
}
func (r *Repository) MarkRetry(ctx context.Context, id, msg string, at time.Time) error {
	return r.exec(ctx, `UPDATE memory_outbox SET status='pending',attempts=attempts+1,available_at=$2,last_error=$3,locked_at=NULL,locked_by=NULL WHERE event_id=$1`, id, at, msg)
}
func (r *Repository) MarkDead(ctx context.Context, id, msg string) error {
	return r.exec(ctx, `UPDATE memory_outbox SET status='dead',attempts=attempts+1,last_error=$2,locked_at=NULL,locked_by=NULL WHERE event_id=$1`, id, msg)
}
func (r *Repository) exec(ctx context.Context, q string, args ...any) error {
	if r.db == nil {
		return fmt.Errorf("outbox unavailable")
	}
	_, err := r.db.ExecContext(ctx, q, args...)
	return err
}

func (r *Repository) EnqueueRepair(ctx context.Context, e consistency.OutboxEvent, key string) (bool, error) {
	if e.EventID == "" {
		e.EventID = fmt.Sprintf("repair-%s-%d-%d-%s", e.Target, e.AggregateID, e.AggregateVersion, e.Type)
	}
	res, err := r.db.ExecContext(ctx, `INSERT INTO memory_outbox
		(event_id,aggregate_id,user_id,aggregate_version,event_type,target,payload,repair_dedupe_key)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (repair_dedupe_key) DO NOTHING`,
		e.EventID, e.AggregateID, e.UserID, e.AggregateVersion, string(e.Type), string(e.Target), e.Payload, key)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
