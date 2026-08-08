// auth_middleware.go — JWT 鉴权中间件 + ctx 内 userID 取值 helper。
//
// 设计：
//   - RequireAuth 工厂返回中间件——secret/issuer 在 main 里注入，运行时全局只构造一次
//   - 通过 internal/usercontext 包共享 ctx key，让 application/domain 层也能读
//   - 401 响应带 request_id，便于排查；body 是稳定 JSON 形态供前端识别
package httpmw

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"agi-assistant/internal/domain/auth"
	"agi-assistant/internal/usercontext"
)

// UserIDFromContext / UsernameFromContext / WithUser 是 usercontext 包的转发包装。
// 让 handler 层只 import httpmw 一个包就能搞定鉴权 + ctx 读写。

func UserIDFromContext(ctx context.Context) string   { return usercontext.UserIDFromContext(ctx) }
func UsernameFromContext(ctx context.Context) string { return usercontext.UsernameFromContext(ctx) }
func WithUser(ctx context.Context, userID, username string) context.Context {
	return usercontext.With(ctx, userID, username)
}

// RequireAuth 返回一个中间件：解 Authorization: Bearer <token> → 放 ctx → 放行。
//
// 校验失败统一返 401 JSON：{"error": "...", "code": "...", "request_id": "..."}
//   - code=invalid_token：签名错 / 算法错 / 缺 header
//   - code=token_expired：过期，前端可据此跳登录
//
// issuer 不能为 nil——nil 时直接 panic（启动期 misconfig 应早死）。
func RequireAuth(issuer *auth.TokenIssuer) func(http.Handler) http.Handler {
	if issuer == nil {
		panic("RequireAuth: issuer is nil")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr := bearerToken(r)
			if tokenStr == "" {
				writeAuthError(w, r, "missing_token", "缺少 Authorization 头", http.StatusUnauthorized)
				return
			}
			claims, err := issuer.Verify(tokenStr)
			if err != nil {
				code := "invalid_token"
				if err == auth.ErrTokenExpired {
					code = "token_expired"
				}
				writeAuthError(w, r, code, err.Error(), http.StatusUnauthorized)
				return
			}
			ctx := usercontext.With(r.Context(), claims.Subject, claims.Username)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// bearerToken 从 Authorization header 抽 "Bearer xxx" 的 xxx。
// 接受大小写不敏感（"BEARER" / "bearer" 都行）；不接受 cookie——避免 CSRF 误伤。
func bearerToken(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if v == "" {
		return ""
	}
	const prefix = "bearer "
	if len(v) < len(prefix) || !strings.EqualFold(v[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(v[len(prefix):])
}

func writeAuthError(w http.ResponseWriter, r *http.Request, code, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":      msg,
		"code":       code,
		"request_id": RequestIDFromContext(r.Context()),
	})
}
