// Package userrepo 是 users 表的仓储。
//
// 接口而非具体实现——便于 application 层用 mock 单测 Service 编排。
// PG 不可用时 NewPGRepo 返回的实例 db==nil，所有方法变成"返回 ErrUnavailable"。
package userrepo

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"agi-assistant/internal/domain/auth"
)

// ErrUnavailable 表示底层 PG 不可用。区别于"用户不存在"，便于 service 决策降级。
var ErrUnavailable = errors.New("user repo unavailable (db not connected)")

// Repo 是 users 表的仓储接口
type Repo interface {
	// Create 插入新用户。username 重复时返回 auth.ErrUserExists。
	Create(username, passwordHash string) (auth.User, error)
	// FindByUsername 按用户名查找。未命中返回 auth.ErrUserNotFound。
	FindByUsername(username string) (auth.User, error)
	// FindByID 按 ID 查找。
	FindByID(id int64) (auth.User, error)
	// TouchLastLogin 更新 last_login_at = NOW()。失败仅 log，不阻塞登录。
	TouchLastLogin(id int64)
}

// PGRepo 是 PostgreSQL 实现
type PGRepo struct {
	db *sql.DB
}

// NewPGRepo 创建 PG 仓储。db == nil 时返回的实例所有方法返回 ErrUnavailable。
func NewPGRepo(db *sql.DB) *PGRepo { return &PGRepo{db: db} }

// Create 插入新用户。利用 username UNIQUE 约束捕获重复注册。
// 注意：bcrypt hash 已经包含盐和成本因子，存储时不需要额外加密。
func (r *PGRepo) Create(username, passwordHash string) (auth.User, error) {
	if r.db == nil {
		return auth.User{}, ErrUnavailable
	}
	var user auth.User
	err := r.db.QueryRow(
		`INSERT INTO users (username, password_hash) VALUES ($1, $2)
		 RETURNING id, username, password_hash, created_at`,
		username, passwordHash,
	).Scan(&user.ID, &user.Username, &user.PasswordHash, &user.CreatedAt)
	if err != nil {
		// PG 唯一约束错误信息含 "duplicate key value violates unique constraint"
		// 用字符串匹配避免引入 lib/pq 的具体 error 类型耦合（虽然 go.mod 里已有，但用 driver-agnostic 写法）
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint") {
			return auth.User{}, auth.ErrUserExists
		}
		return auth.User{}, err
	}
	return user, nil
}

// FindByUsername 按用户名查找。
func (r *PGRepo) FindByUsername(username string) (auth.User, error) {
	if r.db == nil {
		return auth.User{}, ErrUnavailable
	}
	return r.queryOne(
		`SELECT id, username, password_hash, created_at, COALESCE(last_login_at, created_at)
		 FROM users WHERE username = $1`,
		username,
	)
}

// FindByID 按 ID 查找——主要给 JWT middleware 在解 token 后回查用户名/状态用。
func (r *PGRepo) FindByID(id int64) (auth.User, error) {
	if r.db == nil {
		return auth.User{}, ErrUnavailable
	}
	return r.queryOne(
		`SELECT id, username, password_hash, created_at, COALESCE(last_login_at, created_at)
		 FROM users WHERE id = $1`,
		id,
	)
}

func (r *PGRepo) queryOne(query string, args ...interface{}) (auth.User, error) {
	var user auth.User
	var lastLogin time.Time
	err := r.db.QueryRow(query, args...).Scan(
		&user.ID, &user.Username, &user.PasswordHash, &user.CreatedAt, &lastLogin,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.User{}, auth.ErrUserNotFound
	}
	if err != nil {
		return auth.User{}, err
	}
	user.LastLoginAt = lastLogin
	return user, nil
}

// TouchLastLogin best-effort 更新登录时间；DB 错误只 log，不阻塞登录主流程。
func (r *PGRepo) TouchLastLogin(id int64) {
	if r.db == nil {
		return
	}
	_, _ = r.db.Exec(`UPDATE users SET last_login_at = NOW() WHERE id = $1`, id)
}
