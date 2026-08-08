// Package preference 是用户偏好仓储。
package preference

import (
	"database/sql"

	"agi-assistant/internal/pkg/logger"
)

// Repo 用户偏好仓储接口
type Repo interface {
	Save(userID, key, value string)
	Load(userID string) map[string]string
}

// PGRepo 是 Postgres 实现
type PGRepo struct {
	db *sql.DB
}

func NewPGRepo(db *sql.DB) *PGRepo { return &PGRepo{db: db} }

// Save 写入或更新一条偏好
func (r *PGRepo) Save(userID, key, value string) {
	if r.db == nil {
		return
	}
	_, err := r.db.Exec(
		`INSERT INTO user_preferences (user_id, key, value) VALUES ($1, $2, $3)
		 ON CONFLICT (user_id, key) DO UPDATE SET value = $3, updated_at = NOW()`,
		userID, key, value,
	)
	if err != nil {
		logger.L().Warn("preference save to PG failed", "user_id", userID, "key", key, "err", err)
	}
}

// Load 返回该用户的所有偏好键值
func (r *PGRepo) Load(userID string) map[string]string {
	result := make(map[string]string)
	if r.db == nil {
		return result
	}
	rows, err := r.db.Query(`SELECT key, value FROM user_preferences WHERE user_id = $1`, userID)
	if err != nil {
		logger.L().Warn("preference load failed", "user_id", userID, "err", err)
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err == nil {
			result[k] = v
		}
	}
	return result
}
