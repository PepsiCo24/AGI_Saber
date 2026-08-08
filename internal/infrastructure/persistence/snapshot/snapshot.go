// Package snapshot 是任务快照仓储。
package snapshot

import (
	"database/sql"

	"agi-assistant/internal/pkg/logger"
)

// Repo 任务快照仓储接口
type Repo interface {
	Save(taskID string, stateJSON []byte)
}

// PGRepo 是 Postgres 实现
type PGRepo struct {
	db *sql.DB
}

func NewPGRepo(db *sql.DB) *PGRepo { return &PGRepo{db: db} }

// Save upsert 任务快照（同一 task_id 多次保存覆盖最新状态）
func (r *PGRepo) Save(taskID string, stateJSON []byte) {
	if r.db == nil {
		return
	}
	_, err := r.db.Exec(
		`INSERT INTO task_snapshots (task_id, state) VALUES ($1, $2)
		 ON CONFLICT (task_id) DO UPDATE SET state = $2, created_at = NOW()`,
		taskID, stateJSON,
	)
	if err != nil {
		logger.L().Warn("task snapshot save to PG failed", "task_id", taskID, "err", err)
	}
}
