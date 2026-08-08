package httpmw

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agi-assistant/internal/domain/auth"
)

func newTestIssuer(t *testing.T) *auth.TokenIssuer {
	t.Helper()
	is, err := auth.NewTokenIssuer("this-is-a-32-byte-secret-key-for-jwt!", time.Hour, "test")
	if err != nil {
		t.Fatalf("issuer: %v", err)
	}
	return is
}

// TestRequireAuth_MissingHeader 缺 Authorization → 401 + missing_token
func TestRequireAuth_MissingHeader(t *testing.T) {
	mw := RequireAuth(newTestIssuer(t))
	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", rec.Code)
	}
	if called {
		t.Error("缺 token 时 handler 不应被调用")
	}
	body, _ := io.ReadAll(rec.Body)
	var bodyJSON map[string]string
	if err := json.Unmarshal(body, &bodyJSON); err != nil {
		t.Fatalf("响应应是 JSON: %v body=%s", err, string(body))
	}
	if bodyJSON["code"] != "missing_token" {
		t.Errorf("code=%q want missing_token", bodyJSON["code"])
	}
}

// TestRequireAuth_InvalidToken 错误签名 → 401 invalid_token
func TestRequireAuth_InvalidToken(t *testing.T) {
	mw := RequireAuth(newTestIssuer(t))
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-jwt")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", rec.Code)
	}
}

// TestRequireAuth_BearerCaseInsensitive Bearer 大小写不敏感
func TestRequireAuth_BearerCaseInsensitive(t *testing.T) {
	is := newTestIssuer(t)
	tok, _, _ := is.Sign(auth.User{ID: 7, Username: "alice"})
	mw := RequireAuth(is)
	gotUserID := ""
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserID = UserIDFromContext(r.Context())
	}))

	for _, prefix := range []string{"Bearer ", "BEARER ", "bearer ", "bEaReR "} {
		t.Run(strings.TrimSpace(prefix), func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
			req.Header.Set("Authorization", prefix+tok)
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("应通过, 得到 %d", rec.Code)
			}
			if gotUserID != "7" {
				t.Errorf("ctx 中应有 userID=7, 得到 %q", gotUserID)
			}
		})
	}
}

// TestRequireAuth_ValidTokenPasses 通过的请求把 user info 写入 ctx
func TestRequireAuth_ValidTokenPasses(t *testing.T) {
	is := newTestIssuer(t)
	tok, _, _ := is.Sign(auth.User{ID: 42, Username: "bob"})
	mw := RequireAuth(is)
	var seenID, seenName string
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenID = UserIDFromContext(r.Context())
		seenName = UsernameFromContext(r.Context())
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(rec, req)

	if seenID != "42" || seenName != "bob" {
		t.Errorf("ctx user info 错: id=%q name=%q", seenID, seenName)
	}
}

// TestRequireAuth_NoCookieFallback Cookie 不应被识别为 token
// （明确拒绝 cookie 是为防 CSRF；前端必须显式 Authorization header）
func TestRequireAuth_NoCookieFallback(t *testing.T) {
	is := newTestIssuer(t)
	tok, _, _ := is.Sign(auth.User{ID: 1, Username: "u"})
	mw := RequireAuth(is)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.AddCookie(&http.Cookie{Name: "token", Value: tok})
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("Cookie 不应被当 token 通过, 得到 %d", rec.Code)
	}
}

// TestRequireAuth_ExpiredToken 过期 token → 401 token_expired
func TestRequireAuth_ExpiredToken(t *testing.T) {
	is, _ := auth.NewTokenIssuer("this-is-a-32-byte-secret-key-for-jwt!", time.Nanosecond, "test")
	tok, _, _ := is.Sign(auth.User{ID: 1, Username: "u"})
	time.Sleep(20 * time.Millisecond)
	mw := RequireAuth(is)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d", rec.Code)
	}
	if !strings.Contains(body, "token_expired") {
		t.Errorf("响应应含 token_expired, 得到 %s", body)
	}
}

// TestWithUser_ContextHelpers 验证 helper 函数读写一致
func TestWithUser_ContextHelpers(t *testing.T) {
	ctx := WithUser(context.Background(), "u123", "alice")
	if got := UserIDFromContext(ctx); got != "u123" {
		t.Errorf("UserID got=%q", got)
	}
	if got := UsernameFromContext(ctx); got != "alice" {
		t.Errorf("Username got=%q", got)
	}
}
