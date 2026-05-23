// Package handler 实现所有 HTTP API 的请求处理逻辑，并注册到 ServeMux。
package handler

import (
	"context"
	"encoding/json"
	"final/config"
	"final/internal/agent"
	"final/internal/infra"
	"final/internal/tools"
	"fmt"
	"net/http"
	"unicode/utf8"
)

// Server 聚合 Agent 和基础设施引用，挂载所有 HTTP 路由
type Server struct {
	agent  *agent.UnifiedAgent
	infra  *infra.Infrastructure
	cfg    *config.APIConfig
}

// New 创建 Server 并注册所有路由到 mux
func New(a *agent.UnifiedAgent, inf *infra.Infrastructure, cfg *config.APIConfig) *Server {
	s := &Server{agent: a, infra: inf, cfg: cfg}
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	http.HandleFunc("/api/chat",       s.chat)
	http.HandleFunc("/api/chat/stream", s.chatStream)
	http.HandleFunc("/api/chat/cancel", s.chatCancel)
	http.HandleFunc("/api/upload",     s.upload)
	http.HandleFunc("/api/docs/delete", s.docsDelete)
	http.HandleFunc("/api/memory",     s.memory)
	http.HandleFunc("/api/tools",      s.toolsList)
	http.HandleFunc("/api/tools/mcp",  s.registerMCPTool)
	http.HandleFunc("/api/snapshots",  s.snapshots)
	http.HandleFunc("/api/status",     s.status)
}

// ─────────────────────────────── 路由处理 ────────────────────────────────

// POST /api/chat — 统一对话入口（同步模式，向后兼容）
func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Message       string   `json:"message"`
		UseRAG        bool     `json:"use_rag"`
		SelectedTools []string `json:"selected_tools"`
		Explicit      bool     `json:"explicit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	opts := agent.ChatOptions{
		UseRAG:        req.UseRAG,
		SelectedTools: req.SelectedTools,
		Explicit:      req.Explicit,
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	resp := s.agent.ProcessContext(ctx, req.Message, opts)
	writeJSON(w, resp)
}

// POST /api/chat/stream — SSE 流式对话入口
func (s *Server) chatStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Message       string   `json:"message"`
		UseRAG        bool     `json:"use_rag"`
		SelectedTools []string `json:"selected_tools"`
		Explicit      bool     `json:"explicit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, canFlush := w.(http.Flusher)

	// send SSE helper
	sendSSE := func(event string, data interface{}) {
		jsonData, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, jsonData)
		if canFlush {
			flusher.Flush()
		}
	}

	// Send step progress events via a callback
	opts := agent.ChatOptions{
		UseRAG:        req.UseRAG,
		SelectedTools: req.SelectedTools,
		Explicit:      req.Explicit,
	}

	// Notify client that processing has started
	sendSSE("start", map[string]interface{}{"message": req.Message})

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Monitor client disconnect: if client closes connection, cancel context
	notify := r.Context().Done()
	go func() {
		<-notify
		cancel()
	}()

	resp := s.agent.ProcessContext(ctx, req.Message, opts)

	// Send final response
	sendSSE("done", resp)
}

// POST /api/chat/cancel — 取消当前正在执行的对话任务
func (s *Server) chatCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.agent.Cancel()
	writeJSON(w, map[string]interface{}{"ok": true, "message": "已发送取消信号"})
}

// POST /api/upload — 上传文档到 RAG 知识库
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	count, docHash := s.agent.RAG().Ingest(req.Content)
	writeJSON(w, map[string]interface{}{
		"chunk_count": count,
		"doc_hash":    docHash,
		"chunks":      s.agent.RAG().Chunks(),
	})
}

// POST /api/docs/delete — 删除指定文档的所有 chunks
func (s *Server) docsDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		DocHash string `json:"doc_hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.DocHash == "" {
		http.Error(w, "doc_hash is required", http.StatusBadRequest)
		return
	}
	if err := s.agent.RAG().Delete(req.DocHash); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "doc_hash": req.DocHash})
}

// GET /api/memory — 查看三层记忆状态
func (s *Server) memory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"short_term": s.agent.ShortTerm().Messages,
		"long_term":  s.agent.LongTerm().Items,
		"preference": s.agent.Preferences().Data,
	})
}

// GET /api/tools — 列出所有可用工具
func (s *Server) toolsList(w http.ResponseWriter, r *http.Request) {
	type toolInfo struct {
		Name  string       `json:"name"`
		Desc  string       `json:"description"`
		IsMCP bool         `json:"is_mcp,omitempty"`
		Params []tools.Param `json:"params,omitempty"`
	}
	var list []toolInfo
	for _, t := range s.agent.Tools() {
		list = append(list, toolInfo{Name: t.Name, Desc: t.Description, IsMCP: t.IsMCP, Params: t.Parameters})
	}
	writeJSON(w, list)
}

// POST /api/tools/mcp — 动态注册一个 MCP 工具
func (s *Server) registerMCPTool(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Name        string        `json:"name"`
		Description string        `json:"description"`
		Endpoint    string        `json:"endpoint"`
		Params      []tools.Param `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Endpoint == "" {
		http.Error(w, "name and endpoint are required", http.StatusBadRequest)
		return
	}
	t := tools.NewMCPTool(req.Name, req.Description, req.Endpoint, req.Params)
	s.agent.RegisterTool(t)
	writeJSON(w, map[string]interface{}{"ok": true, "name": req.Name})
}

// GET /api/snapshots — 列出任务执行快照摘要
func (s *Server) snapshots(w http.ResponseWriter, r *http.Request) {
	snaps := s.agent.Snapshots()
	infos := make([]map[string]interface{}, 0, len(snaps))
	for i, snap := range snaps {
		infos = append(infos, map[string]interface{}{
			"index":     i,
			"timestamp": snap.Timestamp,
			"steps":     len(snap.State.Steps),
		})
	}
	writeJSON(w, infos)
}

// GET /api/status — 系统状态与配置摘要
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	// RAG chunk 预览（最多 60 字符）
	var chunkPreviews []map[string]interface{}
	for _, c := range s.agent.RAG().Chunks() {
		preview := c.Content
		if utf8.RuneCountInString(preview) > 60 {
			runes := []rune(preview)
			preview = string(runes[:60]) + "..."
		}
		chunkPreviews = append(chunkPreviews, map[string]interface{}{
			"id":      c.ID,
			"content": preview,
		})
	}
	writeJSON(w, map[string]interface{}{
		"rag_loaded":       s.agent.RAG().Loaded,
		"rag_mode":         s.agent.RAG().Mode(),
		"rag_chunks":       chunkPreviews,
		"short_term_count": len(s.agent.ShortTerm().Messages),
		"long_term_count":  len(s.agent.LongTerm().Items),
		"preferences":      s.agent.Preferences().Data,
		"tools_count":      len(s.agent.Tools()),
		"llm_model":        s.cfg.LLMModel,
		"embedding_model":  s.cfg.EmbeddingModel,
		"is_mock":          !s.cfg.IsRealLLM(),
		"infrastructure":   s.infra.Ready,
	})
}

// ─────────────────────────────── 工具函数 ────────────────────────────────

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
