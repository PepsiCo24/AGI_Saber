package toolimpl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agi-assistant/internal/domain/tool"
)

// TestMCPToolStructured_Success 验证成功路径下 ToolResult 的字段完整。
func TestMCPToolStructured_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"echo":  body,
			"label": "ok",
		})
	}))
	defer ts.Close()

	tl := NewMCPTool("echo", "echo tool", ts.URL, []tool.Param{
		{Name: "q", Type: "string", Required: true},
	})
	if tl.ExecuteStructured == nil {
		t.Fatal("MCP 工具应填充 ExecuteStructured")
	}
	r := tl.ExecuteStructured(context.Background(), map[string]interface{}{"q": "hi"})
	if !r.Success {
		t.Fatalf("期望 Success=true, 得到 error=%v", r.Error)
	}
	if r.Error != nil {
		t.Fatalf("成功路径不应有 Error: %+v", r.Error)
	}
	if r.PayloadJSON == nil || r.PayloadJSON["label"] != "ok" {
		t.Fatalf("PayloadJSON 解析失败: %+v", r.PayloadJSON)
	}
	if r.Metadata["backend"] != "mcp" {
		t.Errorf("Metadata.backend 错: %v", r.Metadata)
	}
	if r.Metadata["status_code"] != "200" {
		t.Errorf("Metadata.status_code 错: %v", r.Metadata)
	}
	if r.Duration <= 0 {
		t.Errorf("Duration 应大于 0: %v", r.Duration)
	}
}

// TestMCPToolStructured_4xxNotRetryable 验证 4xx 标记为不可重试。
func TestMCPToolStructured_4xxNotRetryable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"err":"bad param"}`))
	}))
	defer ts.Close()

	tl := NewMCPTool("bad", "", ts.URL, nil)
	r := tl.ExecuteStructured(context.Background(), nil)
	if r.Success {
		t.Fatal("4xx 不应成功")
	}
	if r.Error == nil || r.Error.Code != "http_4xx" {
		t.Fatalf("期望 code=http_4xx, 得到 %+v", r.Error)
	}
	if r.Error.Retryable {
		t.Error("4xx 不应可重试")
	}
	if !strings.Contains(r.Payload, "bad param") {
		t.Error("4xx 应保留 body 便于排查")
	}
}

// TestMCPToolStructured_5xxRetryable 验证 5xx 标记为可重试。
func TestMCPToolStructured_5xxRetryable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
	}))
	defer ts.Close()

	tl := NewMCPTool("flaky", "", ts.URL, nil)
	r := tl.ExecuteStructured(context.Background(), nil)
	if r.Error == nil || r.Error.Code != "http_5xx" || !r.Error.Retryable {
		t.Fatalf("期望 5xx + retryable=true, 得到 %+v", r.Error)
	}
}

// TestMCPToolStructured_Timeout 验证 ctx 超时被识别为 timeout 错误。
func TestMCPToolStructured_Timeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(200)
	}))
	defer ts.Close()

	tl := NewMCPTool("slow", "", ts.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	r := tl.ExecuteStructured(ctx, nil)
	if r.Success {
		t.Fatal("超时不应成功")
	}
	if r.Error == nil || r.Error.Code != "timeout" {
		t.Fatalf("期望 code=timeout, 得到 %+v", r.Error)
	}
	if !r.Error.Retryable {
		t.Error("timeout 应该可重试（重试时使用新 ctx）")
	}
}
