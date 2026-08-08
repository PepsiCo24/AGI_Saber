package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agi-assistant/config"

	"github.com/go-chi/chi/v5"
)

// TestMountPprof_RejectsWithoutToken 验证：即使 pprof 已挂载，
// 请求不带匹配 token 也拿不到内容——避免因端口误开放导致内部信息泄漏。
func TestMountPprof_RejectsWithoutToken(t *testing.T) {
	cfg := &config.APIConfig{}
	cfg.PprofEnabled = true
	cfg.PprofAdminToken = "secret-abc-123"
	s := &Server{cfg: cfg}

	r := chi.NewRouter()
	s.mountPprof(r)

	// 无 token → 404，不透露 pprof 挂了
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("no-token access: expected 404, got %d body=%q", rr.Code, rr.Body.String())
	}

	// 错误 token → 也是 404（常量时间比较，防侧信道枚举）
	req = httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.Header.Set("X-Admin-Token", "wrong")
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("wrong-token access: expected 404, got %d", rr.Code)
	}
}

// TestMountPprof_AllowsWithMatchingToken 验证正确 token 能访问 pprof index。
// 只跑 index 页避免长耗时的 profile 采样进 CI。
func TestMountPprof_AllowsWithMatchingToken(t *testing.T) {
	cfg := &config.APIConfig{}
	cfg.PprofEnabled = true
	cfg.PprofAdminToken = "secret-abc-123"
	s := &Server{cfg: cfg}

	r := chi.NewRouter()
	s.mountPprof(r)

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.Header.Set("X-Admin-Token", "secret-abc-123")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("with-token access: expected 200, got %d body=%q", rr.Code, rr.Body.String())
	}
	// pprof.Index 输出会包含 "Types of profiles available"
	if !strings.Contains(rr.Body.String(), "profiles available") {
		t.Errorf("expected pprof index page, got body=%q", rr.Body.String())
	}
}

// TestMountPprof_NamedProfile 验证 goroutine 这类具名 profile 能通过 admin token 访问。
func TestMountPprof_NamedProfile(t *testing.T) {
	cfg := &config.APIConfig{}
	cfg.PprofEnabled = true
	cfg.PprofAdminToken = "tok"
	s := &Server{cfg: cfg}

	r := chi.NewRouter()
	s.mountPprof(r)

	// debug=1 让 pprof 输出人类可读文本；避免二进制协议解析
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/goroutine?debug=1", nil)
	req.Header.Set("X-Admin-Token", "tok")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("named profile with token: expected 200, got %d body=%q", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "goroutine profile") {
		t.Errorf("expected goroutine profile output, got body=%q", rr.Body.String())
	}
}
