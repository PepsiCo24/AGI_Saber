// skills_handler.go 实现「Skill 广场」的 REST 端点。
//
// 全部挂在受 RequireAuth 保护的 /api/skills 分组下，userID 从 ctx 取。
package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	skillapp "agi-assistant/internal/application/skill"
	"agi-assistant/internal/infrastructure/persistence/skillrepo"
	httpmw "agi-assistant/internal/interfaces/http/middleware"
)

// GET /api/skills/marketplace — 广场：内置精选 + GitHub 热门。
func (s *Server) skillsMarketplace(w http.ResponseWriter, r *http.Request) {
	if s.skills == nil {
		http.Error(w, "skill marketplace not available", http.StatusServiceUnavailable)
		return
	}
	featured, github, hubStatus := s.skills.Marketplace(r.Context())
	writeJSON(w, map[string]interface{}{
		"featured":   featured,
		"github":     github,
		"hub_status": hubStatus,
	})
}

// GET /api/skills/installed — 当前用户已安装的 skill（含开关）。
func (s *Server) skillsInstalled(w http.ResponseWriter, r *http.Request) {
	if s.skills == nil {
		http.Error(w, "skill marketplace not available", http.StatusServiceUnavailable)
		return
	}
	userID := httpmw.UserIDFromContext(r.Context())
	list, err := s.skills.ListInstalled(userID)
	if err != nil {
		s.writeSkillErr(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{"skills": list})
}

// POST /api/skills/install — 安装 skill（body: {skill_id}）。
func (s *Server) skillsInstall(w http.ResponseWriter, r *http.Request) {
	if s.skills == nil {
		http.Error(w, "skill marketplace not available", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		SkillID string `json:"skill_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SkillID == "" {
		http.Error(w, "skill_id is required", http.StatusBadRequest)
		return
	}
	userID := httpmw.UserIDFromContext(r.Context())
	if err := s.skills.Install(r.Context(), userID, req.SkillID); err != nil {
		s.writeSkillErr(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "skill_id": req.SkillID})
}

// POST /api/skills/uninstall — 卸载 skill（body: {skill_id}）。
func (s *Server) skillsUninstall(w http.ResponseWriter, r *http.Request) {
	if s.skills == nil {
		http.Error(w, "skill marketplace not available", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		SkillID string `json:"skill_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SkillID == "" {
		http.Error(w, "skill_id is required", http.StatusBadRequest)
		return
	}
	userID := httpmw.UserIDFromContext(r.Context())
	ok, err := s.skills.Uninstall(userID, req.SkillID)
	if err != nil {
		s.writeSkillErr(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": ok, "skill_id": req.SkillID})
}

// POST /api/skills/toggle — 开关 skill（body: {skill_id, enabled}）。
func (s *Server) skillsToggle(w http.ResponseWriter, r *http.Request) {
	if s.skills == nil {
		http.Error(w, "skill marketplace not available", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		SkillID string `json:"skill_id"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SkillID == "" {
		http.Error(w, "skill_id is required", http.StatusBadRequest)
		return
	}
	userID := httpmw.UserIDFromContext(r.Context())
	ok, err := s.skills.Toggle(userID, req.SkillID, req.Enabled)
	if err != nil {
		s.writeSkillErr(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": ok, "skill_id": req.SkillID, "enabled": req.Enabled})
}

// writeSkillErr 把 skill 领域/仓储错误映射到 HTTP 状态码。
func (s *Server) writeSkillErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, skillrepo.ErrUnavailable):
		http.Error(w, "持久化不可用（数据库未连接）", http.StatusServiceUnavailable)
	case errors.Is(err, skillapp.ErrManifestNotFound):
		http.Error(w, "广场中找不到该 skill", http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
