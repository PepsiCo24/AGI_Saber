package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"agi-assistant/internal/usercontext"
)

// helper：初始化 logger 到 buf、返回 buf；每个测试独立，避免全局状态污染。
func setupBuf(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	Init(Config{Format: "json", Level: "debug", Output: &buf})
	return &buf
}

// TestContextHandler_AttachesRequestID 验证 ctx 里的 request_id 会被 handler
// 自动写入日志——这是 P1 的核心目标（跨层日志串联）。
func TestContextHandler_AttachesRequestID(t *testing.T) {
	buf := setupBuf(t)
	ctx := usercontext.WithRequestID(context.Background(), "req-abc-123")

	C(ctx).Info("rag chunk saved", "doc_hash", "h1")

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("log line not valid JSON: %v (%s)", err, buf.String())
	}
	if got["request_id"] != "req-abc-123" {
		t.Errorf("expected request_id=req-abc-123, got %v", got["request_id"])
	}
	if got["msg"] != "rag chunk saved" {
		t.Errorf("expected msg preserved, got %v", got["msg"])
	}
	if got["doc_hash"] != "h1" {
		t.Errorf("expected explicit attr preserved, got %v", got["doc_hash"])
	}
}

// TestContextHandler_AttachesUserID 验证 user_id 也被自动带上——多租户日志排查依赖此字段。
func TestContextHandler_AttachesUserID(t *testing.T) {
	buf := setupBuf(t)
	ctx := usercontext.With(context.Background(), "u-42", "alice")
	ctx = usercontext.WithRequestID(ctx, "req-xyz")

	C(ctx).Warn("ltm recall slow")

	var got map[string]any
	_ = json.Unmarshal(buf.Bytes(), &got)
	if got["user_id"] != "u-42" {
		t.Errorf("expected user_id=u-42, got %v", got["user_id"])
	}
	if got["request_id"] != "req-xyz" {
		t.Errorf("expected request_id=req-xyz, got %v", got["request_id"])
	}
}

// TestContextHandler_MissingCtxKeys 验证 ctx 里没有身份/请求 ID 时不会崩，也不会写空字段。
// 后台 goroutine（consolidate、outbox）常常没有 request_id，行为要稳。
func TestContextHandler_MissingCtxKeys(t *testing.T) {
	buf := setupBuf(t)

	C(context.Background()).Info("consolidation done")

	line := buf.String()
	if strings.Contains(line, `"request_id":""`) || strings.Contains(line, `"user_id":""`) {
		t.Errorf("empty ctx should not emit empty id fields, got: %s", line)
	}
	// 消息本身仍要被记录
	if !strings.Contains(line, "consolidation done") {
		t.Errorf("expected msg written, got: %s", line)
	}
}

// TestC_NilCtx 验证传 nil ctx 不 panic——防守型分支，避免调用方失误拖崩。
func TestC_NilCtx(t *testing.T) {
	setupBuf(t)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("C(nil) should not panic: %v", r)
		}
	}()
	//nolint:staticcheck // 故意传 nil 测防守
	C(nil).Info("safe")
}

// TestParseLevel_UnknownFallsBackToInfo 保护配置健壮性——用户在 yaml 里写错级别时不该拉挂启动。
func TestParseLevel_UnknownFallsBackToInfo(t *testing.T) {
	buf := setupBuf(t)
	// 重新初始化到 "verbose"（拼错），预期退化成 info：debug 级不出现，info 级出现。
	Init(Config{Format: "json", Level: "verbose", Output: buf})
	L().Debug("should be dropped")
	L().Info("should appear")
	if strings.Contains(buf.String(), "should be dropped") {
		t.Errorf("debug leaked when level fallback should be info")
	}
	if !strings.Contains(buf.String(), "should appear") {
		t.Errorf("info missing after fallback")
	}
}
