// Package logger 是全项目统一的结构化日志入口。
//
// 设计：
//   - 基于标准库 log/slog，零第三方依赖
//   - ContextHandler 在每条日志自动附加 ctx 中的 request_id / user_id
//     业务代码不需要在每处调用手动带 ID —— 传 ctx 即可
//   - 全局默认 logger 由 Init 在 main 启动时配置一次；单测中可用 SetDefault 注入
//
// 用法：
//
//	logger.Init(logger.Config{Format: "json", Level: slog.LevelInfo})
//	logger.L().Info("bootstrap start")
//	logger.C(ctx).Warn("rag chunk write failed", "doc_hash", h, "err", err)
//
// 迁移旧代码：
//
//	log.Printf("⚠️  xxx: %v", err)      →  logger.L().Warn("xxx", "err", err)
//	log.Printf("⚠️  req=%s xxx", reqID) →  logger.C(ctx).Warn("xxx")   // reqID 自动带
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"

	"agi-assistant/internal/usercontext"
)

// Config 是 logger 初始化配置
type Config struct {
	// Format = "text" | "json"，其他值退化到 text。
	// 生产建议 json（便于 ELK / Loki 摄入），本地开发 text（人眼友好）。
	Format string
	// Level = "debug" | "info" | "warn" | "error"，其他值退化到 info。
	Level string
	// Output 是日志输出目标。nil → os.Stderr。
	// 单测里可注入 bytes.Buffer 抓输出。
	Output io.Writer
	// AddSource 是否在每条日志附加代码位置（file:line）。
	// 开发期打开便于定位；生产期一般关闭以省 CPU。
	AddSource bool
}

// defaultLogger 存储全局 logger 指针；atomic 保证 SetDefault 与并发读安全。
var defaultLogger atomic.Pointer[slog.Logger]

func init() {
	// 未 Init 前用一个安全的兜底：text 格式、Info 级、写 stderr。
	// 保证 import 后就能 logger.L().Info(...) 而不 panic（例如单测直接用）。
	SetDefault(newLogger(Config{Format: "text", Level: "info", Output: os.Stderr}))
}

// Init 是 main 启动期唯一入口，配置全局 logger。
// 多次调用只有最后一次生效——生产里只在 bootstrap 调用一次。
func Init(cfg Config) {
	SetDefault(newLogger(cfg))
}

// SetDefault 直接注入自定义 logger（单测 / 特殊嵌入场景用）。
func SetDefault(l *slog.Logger) {
	defaultLogger.Store(l)
	slog.SetDefault(l) // 让第三方库若走 slog.Default() 也用同一实例
}

// L 返回全局默认 logger。**优先用 C(ctx) 而不是 L()**：
// C 会自动带上 request_id / user_id，方便日志串联。
func L() *slog.Logger {
	return defaultLogger.Load()
}

// C 返回一个带上 ctx 中 request_id / user_id 的 logger。
// ctx 为 nil 或不含身份字段时等价于 L()。
//
// 实现上通过 slog.Logger.With 直接把字段附到日志记录上——
// 这样调用方无需用 InfoContext(ctx, ...) 也能把 request_id 打出来，
// 保持 C(ctx).Info("msg", "k", v) 的写法。
func C(ctx context.Context) *slog.Logger {
	base := L()
	if ctx == nil {
		return base
	}
	var attrs []any
	if reqID := usercontext.RequestIDFromContext(ctx); reqID != "" {
		attrs = append(attrs, slog.String("request_id", reqID))
	}
	if userID := usercontext.UserIDFromContext(ctx); userID != "" {
		attrs = append(attrs, slog.String("user_id", userID))
	}
	if len(attrs) == 0 {
		return base
	}
	return base.With(attrs...)
}

// newLogger 按 cfg 构造 *slog.Logger（带 ContextHandler 包装）。
func newLogger(cfg Config) *slog.Logger {
	out := cfg.Output
	if out == nil {
		out = os.Stderr
	}
	opts := &slog.HandlerOptions{
		Level:     parseLevel(cfg.Level),
		AddSource: cfg.AddSource,
	}
	var inner slog.Handler
	if strings.EqualFold(cfg.Format, "json") {
		inner = slog.NewJSONHandler(out, opts)
	} else {
		inner = slog.NewTextHandler(out, opts)
	}
	return slog.New(&contextHandler{inner: inner})
}

// parseLevel 把配置里的字符串等级映射到 slog 常量。未知值退化 Info。
func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error", "err":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// ─────────────────────── Context Handler ────────────────────────────────

// contextHandler 是 slog.Handler 的装饰器：Handle 时从 ctx 自动读取
// request_id / user_id 并附加到 record 上。
//
// 这样业务代码只要传 ctx（本来就要传），不必手动 With("request_id", ...)。
type contextHandler struct {
	inner slog.Handler
}

func (h *contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if ctx != nil {
		if reqID := usercontext.RequestIDFromContext(ctx); reqID != "" {
			r.AddAttrs(slog.String("request_id", reqID))
		}
		if userID := usercontext.UserIDFromContext(ctx); userID != "" {
			r.AddAttrs(slog.String("user_id", userID))
		}
	}
	return h.inner.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{inner: h.inner.WithGroup(name)}
}
