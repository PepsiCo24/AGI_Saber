// Package longterm 是长期记忆条目的仓储。
package longterm

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"agi-assistant/internal/pkg/logger"

	"github.com/lib/pq"
)

// Row 是长期记忆条目的领域模型
type Row struct {
	ID               int
	UserID           string
	Content          string
	Importance       float64
	Embedding        []float64
	CreatedAt        time.Time
	LastAccessed     time.Time
	Category         string
	Tags             []string
	SlotHint         string
	Quarantined      bool
	QuarantineReason string
	Superseded       bool
	SupersededAt     time.Time
	Supersedes       []int
}

// Repo 长期记忆仓储接口
type Repo interface {
	Save(userID, content string, importance float64, embeddingJSON []byte) int
	SaveClassified(userID, content string, importance float64, embeddingJSON []byte,
		category string, tags []string, slotHint string) int
	// Load 全量加载（含所有用户的 legacy 老数据）；启动期 boot 用。
	// 多用户场景下生产建议改用 LoadByUser 按需加载。
	Load() []Row
	// LoadByUser 仅加载指定用户的条目；为未来"按需 lazy load"留位。V1 暂未启用。
	LoadByUser(userID string) []Row
	Update(id int, content string, importance float64, embeddingJSON []byte)
	UpdateImportanceBatch(items []ImportanceUpdate)
	Delete(ids []int)
	// SetQuarantine 设置/清除某条记忆的隔离标记。reason 仅在 quarantined=true 时有意义。
	SetQuarantine(id int, quarantined bool, reason string)
	// MarkSuperseded 把 oldIDs 标记为 superseded（带时间戳），并把 oldIDs
	// 写入 newID 的 supersedes 数组。事务内完成，避免审计链路断裂。
	MarkSuperseded(oldIDs []int, newID int)
}

// ImportanceUpdate 批量衰减时的最小变更单元
type ImportanceUpdate struct {
	ID         int
	Importance float64
}

// PGRepo 是 Postgres 实现
type PGRepo struct {
	db *sql.DB
}

func NewPGRepo(db *sql.DB) *PGRepo { return &PGRepo{db: db} }

// Save 默认分类 "general" 写入
func (r *PGRepo) Save(userID, content string, importance float64, embeddingJSON []byte) int {
	return r.SaveClassified(userID, content, importance, embeddingJSON, "general", nil, "")
}

// SaveClassified 带分类信息写入。userID 是多租户隔离主键；空字符串退化为 'legacy'。
func (r *PGRepo) SaveClassified(userID, content string, importance float64, embeddingJSON []byte,
	category string, tags []string, slotHint string) int {
	if r.db == nil {
		return -1
	}
	if userID == "" {
		userID = "legacy"
	}
	if category == "" {
		category = "general"
	}
	if tags == nil {
		tags = []string{}
	}
	var id int
	err := r.db.QueryRow(
		`INSERT INTO long_term_memory (user_id, content, importance, embedding, category, tags, slot_hint)
		 VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, '')) RETURNING id`,
		userID, content, importance, embeddingJSON, category, pq.Array(tags), slotHint,
	).Scan(&id)
	if err != nil {
		logger.L().Warn("long-term memory save failed", "user_id", userID, "err", err)
		return -1
	}
	return id
}

// Load 加载全部长期记忆条目（含 'legacy' 老数据）。启动期 boot 用。
func (r *PGRepo) Load() []Row {
	if r.db == nil {
		return nil
	}
	rows, err := r.db.Query(`SELECT id, COALESCE(user_id, 'legacy'),
		content, importance, embedding,
		COALESCE(created_at, NOW()), COALESCE(last_accessed, NOW()),
		COALESCE(category, 'general'), COALESCE(tags, '{}'::TEXT[]), COALESCE(slot_hint, ''),
		COALESCE(quarantined, FALSE), COALESCE(quarantine_reason, ''),
		COALESCE(superseded, FALSE), superseded_at, COALESCE(supersedes, '{}'::INT[])
		FROM long_term_memory ORDER BY id`)
	if err != nil {
		logger.L().Warn("long-term memory load failed", "err", err)
		return nil
	}
	return r.scanRows(rows)
}

// LoadByUser 仅加载指定用户的条目。给未来"按需 lazy load"留位。
func (r *PGRepo) LoadByUser(userID string) []Row {
	if r.db == nil || userID == "" {
		return nil
	}
	rows, err := r.db.Query(`SELECT id, user_id,
		content, importance, embedding,
		COALESCE(created_at, NOW()), COALESCE(last_accessed, NOW()),
		COALESCE(category, 'general'), COALESCE(tags, '{}'::TEXT[]), COALESCE(slot_hint, ''),
		COALESCE(quarantined, FALSE), COALESCE(quarantine_reason, ''),
		COALESCE(superseded, FALSE), superseded_at, COALESCE(supersedes, '{}'::INT[])
		FROM long_term_memory WHERE user_id = $1 ORDER BY id`, userID)
	if err != nil {
		logger.L().Warn("long-term memory load-by-user failed", "user_id", userID, "err", err)
		return nil
	}
	return r.scanRows(rows)
}

// scanRows 是 Load / LoadByUser 共用的反序列化逻辑。
func (r *PGRepo) scanRows(rows *sql.Rows) []Row {
	defer rows.Close()
	var items []Row
	for rows.Next() {
		var row Row
		var embJSON []byte
		var tags pq.StringArray
		var supersedes pq.Int64Array
		var supersededAt sql.NullTime
		if err := rows.Scan(&row.ID, &row.UserID, &row.Content, &row.Importance, &embJSON,
			&row.CreatedAt, &row.LastAccessed, &row.Category, &tags, &row.SlotHint,
			&row.Quarantined, &row.QuarantineReason,
			&row.Superseded, &supersededAt, &supersedes); err != nil {
			continue
		}
		if len(embJSON) > 0 {
			json.Unmarshal(embJSON, &row.Embedding)
		}
		row.Tags = []string(tags)
		if supersededAt.Valid {
			row.SupersededAt = supersededAt.Time
		}
		row.Supersedes = make([]int, len(supersedes))
		for i, v := range supersedes {
			row.Supersedes[i] = int(v)
		}
		items = append(items, row)
	}
	return items
}

// Update 修改一条长期记忆
func (r *PGRepo) Update(id int, content string, importance float64, embeddingJSON []byte) {
	if r.db == nil {
		return
	}
	_, err := r.db.Exec(
		`UPDATE long_term_memory SET content = $1, importance = $2, embedding = $3, last_accessed = NOW() WHERE id = $4`,
		content, importance, embeddingJSON, id,
	)
	if err != nil {
		logger.L().Warn("long-term memory update failed", "id", id, "err", err)
	}
}

// Delete 批量删除
func (r *PGRepo) Delete(ids []int) {
	if r.db == nil || len(ids) == 0 {
		return
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	query := fmt.Sprintf("DELETE FROM long_term_memory WHERE id IN (%s)", strings.Join(placeholders, ","))
	if _, err := r.db.Exec(query, args...); err != nil {
		logger.L().Warn("long-term memory batch delete failed", "err", err)
	}
}

// UpdateImportanceBatch 用单条 SQL（VALUES + UPDATE FROM）批量刷新 importance。
// 衰减阶段每次 Consolidate 都会触达全表，单条 UPDATE 会产生 N 次往返；
// 这里用 unnest 把 (id, importance) 数组下推一次完成。
func (r *PGRepo) UpdateImportanceBatch(items []ImportanceUpdate) {
	if r.db == nil || len(items) == 0 {
		return
	}
	ids := make([]int64, len(items))
	imps := make([]float64, len(items))
	for i, it := range items {
		ids[i] = int64(it.ID)
		imps[i] = it.Importance
	}
	_, err := r.db.Exec(
		`UPDATE long_term_memory AS m
		 SET importance = v.importance
		 FROM (SELECT unnest($1::BIGINT[]) AS id, unnest($2::DOUBLE PRECISION[]) AS importance) AS v
		 WHERE m.id = v.id`,
		pq.Array(ids), pq.Array(imps),
	)
	if err != nil {
		logger.L().Warn("long-term memory batch decay update failed",
			"count", len(items), "err", err)
	}
}

// SetQuarantine 持久化隔离状态。quarantined=false 时清空 reason。
// 调用方在内存层 Quarantine/Unquarantine 成功后调用，保证内存与 PG 一致。
func (r *PGRepo) SetQuarantine(id int, quarantined bool, reason string) {
	if r.db == nil {
		return
	}
	if !quarantined {
		reason = ""
	}
	_, err := r.db.Exec(
		`UPDATE long_term_memory SET quarantined = $1, quarantine_reason = NULLIF($2, '')
		 WHERE id = $3`,
		quarantined, reason, id,
	)
	if err != nil {
		logger.L().Warn("long-term memory quarantine flag update failed", "id", id, "err", err)
	}
}

// MarkSuperseded 把 oldIDs 标记为 superseded（带时间戳），并把它们追加到
// newID.supersedes 数组（去重）。事务内完成——避免"旧条目已下架但新条目
// 没记录替代关系"的窗口期，让审计链路始终连贯。
//
// newID == 0 表示新条目尚未持久化，此时仅标记旧条目（调用方需在新条目
// 落库后再调一次以补上 supersedes 链接）。
func (r *PGRepo) MarkSuperseded(oldIDs []int, newID int) {
	if r.db == nil || len(oldIDs) == 0 {
		return
	}
	tx, err := r.db.Begin()
	if err != nil {
		logger.L().Warn("MarkSuperseded start tx failed", "err", err)
		return
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	ids := make([]int64, len(oldIDs))
	for i, id := range oldIDs {
		ids[i] = int64(id)
	}
	if _, err = tx.Exec(
		`UPDATE long_term_memory
		 SET superseded = TRUE, superseded_at = NOW()
		 WHERE id = ANY($1::INT[]) AND NOT superseded`,
		pq.Array(ids),
	); err != nil {
		logger.L().Warn("MarkSuperseded flag old rows failed", "err", err)
		return
	}

	if newID > 0 {
		// 用 array_cat + 反向去重写法（确保 supersedes 内无重复）
		if _, err = tx.Exec(
			`UPDATE long_term_memory
			 SET supersedes = (
			   SELECT ARRAY(SELECT DISTINCT unnest(COALESCE(supersedes, '{}'::INT[]) || $1::INT[]))
			   FROM long_term_memory WHERE id = $2
			 )
			 WHERE id = $2`,
			pq.Array(ids), newID,
		); err != nil {
			logger.L().Warn("MarkSuperseded link new row failed", "new_id", newID, "err", err)
			return
		}
	}

	if err = tx.Commit(); err != nil {
		logger.L().Warn("MarkSuperseded commit failed", "err", err)
	}
}
