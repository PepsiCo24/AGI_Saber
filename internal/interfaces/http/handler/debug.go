// debug.go — 调试端点（当前仅 pprof）。
//
// pprof 走 chi.Route + admin token 双重防护：
//  1. 只在 config.observability.pprof.enabled=true 时挂载
//  2. 每次请求校验 X-Admin-Token 与 config.observability.pprof.admin_token 一致
//     token 未配置时视为配置错误——启动期在 New 里应已 panic，防止无鉴权暴露
//
// 覆盖端点：
//
//	/debug/pprof/            index 页
//	/debug/pprof/cmdline     启动命令
//	/debug/pprof/profile     30s CPU 采样
//	/debug/pprof/symbol      符号解析
//	/debug/pprof/trace       执行 trace
//	/debug/pprof/{profile}   goroutine / heap / allocs / block / mutex / threadcreate 等
package handler

import (
	"crypto/subtle"
	"net/http"
	"net/http/pprof"

	"github.com/go-chi/chi/v5"
)

// mountPprof 把 pprof 系列端点挂到给定 router 上，全部套一层 admin token 校验。
//
// 使用 crypto/subtle.ConstantTimeCompare 对 token 做常量时间比较，
// 避免通过响应耗时侧信道枚举 token 前缀。
func (s *Server) mountPprof(r chi.Router) {
	adminOnly := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			expected := s.cfg.PprofAdminToken
			got := req.Header.Get("X-Admin-Token")
			if expected == "" || subtle.ConstantTimeCompare([]byte(expected), []byte(got)) != 1 {
				// 不返回 401 body 中任何内容——避免透露 pprof 已挂载。
				// 与关闭 pprof 时的 404 语义几乎一致，减少侦察价值。
				http.NotFound(w, req)
				return
			}
			next(w, req)
		}
	}

	r.Route("/debug/pprof", func(r chi.Router) {
		r.Get("/", adminOnly(pprof.Index))
		r.Get("/cmdline", adminOnly(pprof.Cmdline))
		r.Get("/profile", adminOnly(pprof.Profile))
		r.Get("/symbol", adminOnly(pprof.Symbol))
		r.Post("/symbol", adminOnly(pprof.Symbol))
		r.Get("/trace", adminOnly(pprof.Trace))
		// 具名 profile：goroutine / heap / allocs / block / mutex / threadcreate
		r.Get("/{profile}", adminOnly(func(w http.ResponseWriter, req *http.Request) {
			// pprof.Handler 按 URL 最后一段选择 profile；chi 剥掉了前缀，
			// 直接把 chi.URLParam 转发给标准 pprof.Handler。
			pprof.Handler(chi.URLParam(req, "profile")).ServeHTTP(w, req)
		}))
	})
}
