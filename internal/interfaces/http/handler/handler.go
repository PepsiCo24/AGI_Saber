// Package handler 实现所有 HTTP API 的请求处理逻辑。
//
// 路由用 chi 注册：
//   - 显式 Method（GET/POST），不再在每个 handler 里手写 if r.Method != ... 检查
//   - 嵌套分组（/api/memory/*）减少前缀重复
//   - 中间件由调用方（main 或 New 内部）拼装，本包只关心业务逻辑
//
// 横切关注点（requestID / accessLog / panicRecover / cors）见
// internal/interfaces/http/middleware 包。
package handler

import (
	"agi-assistant/config"
	authapp "agi-assistant/internal/application/auth"
	"agi-assistant/internal/application/chat"
	skillapp "agi-assistant/internal/application/skill"
	authdomain "agi-assistant/internal/domain/auth"
	"agi-assistant/internal/domain/document"
	"agi-assistant/internal/domain/tool"
	toolimpl "agi-assistant/internal/infrastructure/tool"
	httpmw "agi-assistant/internal/interfaces/http/middleware"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Server 聚合 Agent / Auth Service / 配置，挂载 chi.Router。
type Server struct {
	agent  *chat.UnifiedAgent
	auth   *authapp.Service
	issuer *authdomain.TokenIssuer
	cfg    *config.APIConfig
	skills *skillapp.Service
	router *chi.Mux
}

// New 创建 Server 并构建 chi.Router。
//
// 路由分两组：
//   - /api/auth/* 不需鉴权（注册/登录入口本身要能在无 token 时调用）
//   - /api/* 其他端点全部走 RequireAuth 中间件 → ctx 中带 userID
//   - /healthz /readyz 公开
//   - /* 静态资源公开
//
// 中间件顺序（外→内）：RequestID → PanicRecover → AccessLog → CORS → [RequireAuth]
func New(a *chat.UnifiedAgent, authSvc *authapp.Service, issuer *authdomain.TokenIssuer, cfg *config.APIConfig, skills *skillapp.Service) *Server {
	s := &Server{agent: a, auth: authSvc, issuer: issuer, cfg: cfg, skills: skills}
	r := chi.NewRouter()

	r.Use(httpmw.RequestID)
	r.Use(httpmw.PanicRecover)
	r.Use(httpmw.AccessLog)
	r.Use(httpmw.CORS(httpmw.DefaultCORSConfig()))

	s.router = r
	s.registerRoutes()
	return s
}

// Handler 返回 http.Handler 给 main 用于 http.Server。
func (s *Server) Handler() http.Handler { return s.router }
func (s *Server) Router() chi.Router    { return s.router }

func (s *Server) registerRoutes() {
	r := s.router

	// 公开端点：注册 / 登录
	r.Route("/api/auth", func(r chi.Router) {
		r.Post("/register", s.authRegister)
		r.Post("/login", s.authLogin)
		// /me 需要鉴权——注册到 protected 组
	})

	// 鉴权保护的业务 API
	r.Route("/api", func(r chi.Router) {
		r.Use(httpmw.RequireAuth(s.issuer))

		r.Get("/auth/me", s.authMe)

		r.Post("/chat", s.chat)
		r.Post("/chat/stream", s.chatStream)
		r.Post("/chat/cancel", s.chatCancel)

		r.Post("/upload", s.upload)
		r.Post("/docs/delete", s.docsDelete)

		r.Route("/documents", func(r chi.Router) {
			r.Get("/", s.documentsList)
			r.Post("/", s.documentsCreate)
			r.Get("/{documentID}", s.documentGet)
			r.Post("/{documentID}/ingest", s.documentIngest)
		})

		r.Route("/memory", func(r chi.Router) {
			r.Get("/", s.memory)
			r.Get("/quarantined", s.memoryQuarantined)
			r.Post("/quarantine", s.memoryQuarantine)
			r.Post("/unquarantine", s.memoryUnquarantine)
			r.Get("/superseded", s.memorySuperseded)
		})

		r.Get("/tools", s.toolsList)
		r.Post("/tools/mcp", s.registerMCPTool)

		r.Route("/skills", func(r chi.Router) {
			r.Get("/marketplace", s.skillsMarketplace)
			r.Get("/installed", s.skillsInstalled)
			r.Post("/install", s.skillsInstall)
			r.Post("/uninstall", s.skillsUninstall)
			r.Post("/toggle", s.skillsToggle)
		})

		r.Get("/snapshots", s.snapshots)
		r.Get("/status", s.status)
	})

	// 健康检查端点
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Get("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// pprof 端点：仅在 config.observability.pprof.enabled=true 且请求带匹配的
	// X-Admin-Token 时开放。生产暴露 pprof 会泄漏 goroutine 栈 / 堆信息，
	// 因此双重防线（配置开关 + token 校验）——避免因端口误开放而全网可访问。
	if s.cfg.PprofEnabled {
		s.mountPprof(r)
	}

	// 前端已迁移为独立工程（web/，Vite + Vue3），由独立静态服务器/nginx 托管，
	// 后端不再托管静态资源。根路径返回一句提示，避免裸 404。
	r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("AGI-saber API. 前端请运行 web/（npm run dev 或部署 dist）。"))
	})
}

// ─────────────────────────────── Auth Handlers ────────────────────────────

// POST /api/auth/register — 注册新账号，注册成功直接返回 access token
func (s *Server) authRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAuthHTTPError(w, r, "invalid_body", "请求体格式错误", http.StatusBadRequest)
		return
	}
	res, err := s.auth.Register(req.Username, req.Password)
	if err != nil {
		s.translateAuthErr(w, r, err)
		return
	}
	writeJSON(w, res)
}

// POST /api/auth/login — 用户名+密码登录
func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAuthHTTPError(w, r, "invalid_body", "请求体格式错误", http.StatusBadRequest)
		return
	}
	res, err := s.auth.Login(req.Username, req.Password)
	if err != nil {
		s.translateAuthErr(w, r, err)
		return
	}
	writeJSON(w, res)
}

// GET /api/auth/me — 验证 token 并回显当前用户身份
func (s *Server) authMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{
		"user_id":  httpmw.UserIDFromContext(r.Context()),
		"username": httpmw.UsernameFromContext(r.Context()),
	})
}

// translateAuthErr 把 domain/auth 的 sentinel error 映射到 HTTP 状态码 + code 字符串。
// 集中处理避免每个 handler 各写一份 switch。
func (s *Server) translateAuthErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, authdomain.ErrUserExists):
		writeAuthHTTPError(w, r, "user_exists", err.Error(), http.StatusConflict)
	case errors.Is(err, authdomain.ErrPasswordMismatch),
		errors.Is(err, authdomain.ErrUserNotFound):
		// 故意把 NotFound 也归到 401，避免账号枚举（service 已统一返 PasswordMismatch；这里保险）
		writeAuthHTTPError(w, r, "invalid_credentials", "用户名或密码错误", http.StatusUnauthorized)
	case errors.Is(err, authdomain.ErrUsernameTooShort),
		errors.Is(err, authdomain.ErrUsernameTooLong),
		errors.Is(err, authdomain.ErrPasswordTooShort),
		errors.Is(err, authdomain.ErrPasswordTooLong):
		writeAuthHTTPError(w, r, "invalid_input", err.Error(), http.StatusBadRequest)
	default:
		writeAuthHTTPError(w, r, "internal", "服务器内部错误", http.StatusInternalServerError)
	}
}

func writeAuthHTTPError(w http.ResponseWriter, r *http.Request, code, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":      msg,
		"code":       code,
		"request_id": httpmw.RequestIDFromContext(r.Context()),
	})
}

// ─────────────────────────────── 路由处理 ────────────────────────────────

// POST /api/chat — 统一对话入口（同步模式，向后兼容）
func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Message string `json:"message"`
		UseRAG  bool   `json:"use_rag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	opts := chat.ChatOptions{
		UseRAG: req.UseRAG,
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	resp := s.agent.ProcessContext(ctx, req.Message, opts)
	writeJSON(w, resp)
}

// POST /api/chat/stream — SSE 流式对话入口
func (s *Server) chatStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Message string `json:"message"`
		UseRAG  bool   `json:"use_rag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)

	sendSSE := func(event string, data interface{}) {
		jsonData, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, jsonData)
		if canFlush {
			flusher.Flush()
		}
	}

	opts := chat.ChatOptions{
		UseRAG: req.UseRAG,
	}

	sendSSE("start", map[string]interface{}{"message": req.Message})

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	notify := r.Context().Done()
	go func() {
		<-notify
		cancel()
	}()

	s.agent.ProcessStream(ctx, req.Message, opts, func(evt chat.StreamEvent) {
		sendSSE(evt.Type, evt.Data)
	})
}

// POST /api/chat/cancel — 取消当前正在执行的对话任务
func (s *Server) chatCancel(w http.ResponseWriter, r *http.Request) {
	s.agent.Cancel()
	writeJSON(w, map[string]interface{}{"ok": true, "message": "已发送取消信号"})
}

// POST /api/upload — 上传文档到 RAG 知识库
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)

	parsed, err := parseUploadDocument(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if parsed.NeedsOCR {
		writeJSON(w, map[string]interface{}{
			"filename":      parsed.Filename,
			"content_type":  parsed.ContentType,
			"parser":        parsed.Parser,
			"pages":         parsed.Pages,
			"text_chars":    parsed.TextChars,
			"needs_ocr":     true,
			"chunk_count":   0,
			"parent_count":  0,
			"indexed_count": 0,
			"doc_hash":      "",
			"chunks":        nil,
			"message":       "PDF 文本抽取结果过少，可能是扫描件，需要 OCR 后再入库",
		})
		return
	}

	ingested := s.agent.RAG().Ingest(parsed.Content)
	writeJSON(w, map[string]interface{}{
		"filename":      parsed.Filename,
		"content_type":  parsed.ContentType,
		"parser":        parsed.Parser,
		"pages":         parsed.Pages,
		"text_chars":    parsed.TextChars,
		"needs_ocr":     parsed.NeedsOCR,
		"chunk_count":   ingested.ChunkCount,
		"parent_count":  ingested.ParentCount,
		"indexed_count": ingested.IndexedCount,
		"chunk_preview": ingested.ChunkPreview,
		"doc_hash":      ingested.DocHash,
		"chunks":        s.agent.RAG().Chunks(),
	})
}

func parseUploadDocument(r *http.Request) (document.ParseResult, error) {
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(strings.ToLower(contentType), "multipart/form-data") {
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			return document.ParseResult{}, fmt.Errorf("invalid multipart upload: %w", err)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			return document.ParseResult{}, fmt.Errorf("file is required")
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			return document.ParseResult{}, fmt.Errorf("read uploaded file failed: %w", err)
		}
		return document.ParseBytes(header.Filename, header.Header.Get("Content-Type"), data)
	}

	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return document.ParseResult{}, fmt.Errorf("invalid request body")
	}
	return document.ParseBytes("upload.txt", "text/plain", []byte(req.Content))
}

// POST /api/docs/delete — 删除指定文档的所有 chunks
func (s *Server) docsDelete(w http.ResponseWriter, r *http.Request) {
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

// GET /api/documents — 列出本地文档库
func (s *Server) documentsList(w http.ResponseWriter, _ *http.Request) {
	docs, err := s.agent.ListDocuments()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"documents": docs})
}

// POST /api/documents — 写入本地文档库
func (s *Server) documentsCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DocumentID  string                 `json:"document_id"`
		Title       string                 `json:"title"`
		DocType     string                 `json:"doc_type"`
		Source      string                 `json:"source"`
		CreatedBy   string                 `json:"created_by"`
		ContentMD   string                 `json:"content_md"`
		Summary     string                 `json:"summary"`
		Metadata    map[string]interface{} `json:"metadata"`
		IngestToRAG bool                   `json:"ingest_to_rag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	res, err := s.agent.WriteDocument(document.WriteRequest{
		DocumentID: req.DocumentID,
		Title:      req.Title,
		DocType:    req.DocType,
		Source:     req.Source,
		CreatedBy:  req.CreatedBy,
		ContentMD:  req.ContentMD,
		Summary:    req.Summary,
		Metadata:   req.Metadata,
	}, req.IngestToRAG)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, res)
}

// GET /api/documents/{documentID}
func (s *Server) documentGet(w http.ResponseWriter, r *http.Request) {
	documentID := chi.URLParam(r, "documentID")
	if documentID == "" {
		http.Error(w, "document_id is required", http.StatusBadRequest)
		return
	}
	doc, ver, err := s.agent.GetDocument(documentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]interface{}{"document": doc, "version": ver})
}

// POST /api/documents/{documentID}/ingest
func (s *Server) documentIngest(w http.ResponseWriter, r *http.Request) {
	documentID := chi.URLParam(r, "documentID")
	if documentID == "" {
		http.Error(w, "document_id is required", http.StatusBadRequest)
		return
	}
	var req struct {
		VersionID string `json:"version_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	res, err := s.agent.IngestDocument(documentID, req.VersionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, res)
}

// GET /api/memory — 查看当前用户的三层记忆状态。
// LTM 全量快照含所有用户的条目（前端按 user_id 自行过滤）；
// short_term / preference 只暴露本用户的桶，避免跨用户泄漏。
func (s *Server) memory(w http.ResponseWriter, r *http.Request) {
	userID := httpmw.UserIDFromContext(r.Context())
	body := map[string]interface{}{
		"long_term":  s.agent.LongTerm().Snapshot(),
		"short_term": []interface{}{},
		"preference": map[string]string{},
	}
	if stm := s.agent.ShortTerm(userID); stm != nil {
		body["short_term"] = stm.Snapshot()
	}
	if pref := s.agent.Preferences(userID); pref != nil {
		body["preference"] = pref.Snapshot()
	}
	writeJSON(w, body)
}

// GET /api/memory/quarantined
func (s *Server) memoryQuarantined(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]interface{}{
		"items": s.agent.QuarantinedMemories(),
	})
}

// POST /api/memory/quarantine
func (s *Server) memoryQuarantine(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     int    `json:"id"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID <= 0 {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	reason := req.Reason
	if reason == "" {
		reason = "manual"
	}
	ok := s.agent.QuarantineMemory(req.ID, reason)
	writeJSON(w, map[string]interface{}{"ok": ok, "id": req.ID})
}

// POST /api/memory/unquarantine
func (s *Server) memoryUnquarantine(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID int `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID <= 0 {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	ok := s.agent.UnquarantineMemory(req.ID)
	writeJSON(w, map[string]interface{}{"ok": ok, "id": req.ID})
}

// GET /api/memory/superseded
func (s *Server) memorySuperseded(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]interface{}{
		"items": s.agent.SupersededMemories(),
	})
}

// GET /api/tools
func (s *Server) toolsList(w http.ResponseWriter, _ *http.Request) {
	type toolInfo struct {
		Name   string       `json:"name"`
		Desc   string       `json:"description"`
		IsMCP  bool         `json:"is_mcp,omitempty"`
		Params []tool.Param `json:"params,omitempty"`
	}
	var list []toolInfo
	for _, t := range s.agent.Tools() {
		list = append(list, toolInfo{Name: t.Name, Desc: t.Description, IsMCP: t.IsMCP, Params: t.Parameters})
	}
	writeJSON(w, list)
}

// POST /api/tools/mcp
func (s *Server) registerMCPTool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string       `json:"name"`
		Description string       `json:"description"`
		Endpoint    string       `json:"endpoint"`
		Params      []tool.Param `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Endpoint == "" {
		http.Error(w, "name and endpoint are required", http.StatusBadRequest)
		return
	}
	t := toolimpl.NewMCPTool(req.Name, req.Description, req.Endpoint, req.Params)
	s.agent.RegisterTool(t)
	writeJSON(w, map[string]interface{}{"ok": true, "name": req.Name})
}

// GET /api/snapshots
func (s *Server) snapshots(w http.ResponseWriter, _ *http.Request) {
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

// GET /api/status
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.agent.Status(r.Context()))
}

// ─────────────────────────────── 工具函数 ────────────────────────────────

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
