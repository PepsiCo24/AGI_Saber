// Package skillrepo 是 installed_skills 表的仓储。
//
// 沿用 userrepo 的降级约定：PG 不可用时 NewPGRepo 返回的实例 db==nil，
// 所有方法返回 ErrUnavailable，让上层 Service 决策降级（广场仍可展示，
// 但安装 / 开关操作明确失败而非静默）。
package skillrepo

import (
	"database/sql"
	"encoding/json"
	"errors"

	"agi-assistant/internal/domain/skill"
	"agi-assistant/internal/domain/tool"
)

// ErrUnavailable 表示底层 PG 不可用。
var ErrUnavailable = errors.New("skill repo unavailable (db not connected)")

// Repo 是 installed_skills 表的仓储接口
type Repo interface {
	// ListByUser 返回用户已安装的全部 skill（含开关状态）。
	ListByUser(userID string) ([]skill.Skill, error)
	// ListEnabled 只返回 enabled=TRUE 的 skill，供主循环拉取。
	ListEnabled(userID string) ([]skill.Skill, error)
	// Install upsert 一条 skill，默认 enabled 保持（新装为 false）。
	Install(userID string, m skill.Manifest) error
	// SetEnabled 切换开关；skill 不存在返回 (false, nil)。
	SetEnabled(userID, skillID string, enabled bool) (bool, error)
	// Uninstall 删除；不存在返回 (false, nil)。
	Uninstall(userID, skillID string) (bool, error)
}

// PGRepo 是 PostgreSQL 实现
type PGRepo struct {
	db *sql.DB
}

// NewPGRepo 创建 PG 仓储。db==nil 时所有方法返回 ErrUnavailable。
func NewPGRepo(db *sql.DB) *PGRepo { return &PGRepo{db: db} }

// Install 幂等写入：主键 (user_id, skill_id) 冲突时更新元数据，但保留已有 enabled 开关。
func (r *PGRepo) Install(userID string, m skill.Manifest) error {
	if r.db == nil {
		return ErrUnavailable
	}
	params, err := json.Marshal(m.Parameters)
	if err != nil {
		params = []byte("[]")
	}
	_, err = r.db.Exec(
		`INSERT INTO installed_skills
			(user_id, skill_id, name, description, category, source, source_url,
			 invocation, endpoint, prompt_template, params, enabled)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,FALSE)
		 ON CONFLICT (user_id, skill_id) DO UPDATE SET
			name=$3, description=$4, category=$5, source=$6, source_url=$7,
			invocation=$8, endpoint=$9, prompt_template=$10, params=$11`,
		userID, m.ID, m.Name, m.Description, m.Category, string(m.Source), m.SourceURL,
		string(m.Invocation), m.Endpoint, m.PromptTemplate, params,
	)
	return err
}

// SetEnabled 切换开关。
func (r *PGRepo) SetEnabled(userID, skillID string, enabled bool) (bool, error) {
	if r.db == nil {
		return false, ErrUnavailable
	}
	res, err := r.db.Exec(
		`UPDATE installed_skills SET enabled=$3 WHERE user_id=$1 AND skill_id=$2`,
		userID, skillID, enabled,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Uninstall 删除一条已安装 skill。
func (r *PGRepo) Uninstall(userID, skillID string) (bool, error) {
	if r.db == nil {
		return false, ErrUnavailable
	}
	res, err := r.db.Exec(
		`DELETE FROM installed_skills WHERE user_id=$1 AND skill_id=$2`,
		userID, skillID,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListByUser 返回用户全部已安装 skill。
func (r *PGRepo) ListByUser(userID string) ([]skill.Skill, error) {
	return r.query(
		`SELECT skill_id, name, description, category, source, source_url,
		        invocation, endpoint, prompt_template, params, enabled
		 FROM installed_skills WHERE user_id=$1 ORDER BY installed_at DESC`,
		userID,
	)
}

// ListEnabled 只返回开启的 skill。
func (r *PGRepo) ListEnabled(userID string) ([]skill.Skill, error) {
	return r.query(
		`SELECT skill_id, name, description, category, source, source_url,
		        invocation, endpoint, prompt_template, params, enabled
		 FROM installed_skills WHERE user_id=$1 AND enabled=TRUE`,
		userID,
	)
}

func (r *PGRepo) query(sqlText string, args ...interface{}) ([]skill.Skill, error) {
	if r.db == nil {
		return nil, ErrUnavailable
	}
	rows, err := r.db.Query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []skill.Skill
	userID, _ := args[0].(string)
	for rows.Next() {
		var (
			s          skill.Skill
			source     string
			invocation string
			sourceURL  sql.NullString
			endpoint   sql.NullString
			promptTmpl sql.NullString
			paramsRaw  []byte
		)
		if err := rows.Scan(
			&s.ID, &s.Name, &s.Description, &s.Category, &source, &sourceURL,
			&invocation, &endpoint, &promptTmpl, &paramsRaw, &s.Enabled,
		); err != nil {
			return nil, err
		}
		s.UserID = userID
		s.Source = skill.Source(source)
		s.Invocation = skill.Invocation(invocation)
		s.SourceURL = sourceURL.String
		s.Endpoint = endpoint.String
		s.PromptTemplate = promptTmpl.String
		if len(paramsRaw) > 0 {
			var params []tool.Param
			if json.Unmarshal(paramsRaw, &params) == nil {
				s.Parameters = params
			}
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
