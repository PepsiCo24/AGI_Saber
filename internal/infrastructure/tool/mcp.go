package toolimpl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"agi-assistant/internal/domain/tool"
)

// mcpDefaultTimeout 是 MCP 工具未显式指定 ctx 截止时间时的兜底超时。
// MCP 通过 HTTP 调外部服务，外部 endpoint 卡死会拖住整个 goroutine，必须强制设上限。
const mcpDefaultTimeout = 30 * time.Second

// mcpHTTPClient 复用同一个带连接池的客户端；每个请求自带 ctx 超时，
// 这里的 Timeout 是兜底（防止 ExecuteCtx 的调用方传 ctx.Background() 而忘了截止时间）。
var mcpHTTPClient = &http.Client{Timeout: mcpDefaultTimeout}

// NewMCPTool 创建一个调用外部 HTTP 端点的 MCP 兼容工具。
// 请求体为 JSON 对象（params），响应体作为工具结果返回。
//
// 超时策略（双保险）：
//  1. 调用方通过 ExecuteCtx / ExecuteStructured 传入的 ctx（GraphRuntime 会包 StepTimeout）
//  2. mcpHTTPClient.Timeout = 30s 兜底，防止 ctx 是 Background()
//
// 结果策略：
//   - ExecuteStructured 返回 ToolResult（携带 status_code / duration / retryable 错误分类）
//   - Execute / ExecuteCtx 仍返回 string，向后兼容旧调用路径
func NewMCPTool(name, description, endpoint string, params []tool.Param) tool.Tool {
	structured := func(ctx context.Context, p map[string]interface{}) tool.ToolResult {
		start := time.Now()
		meta := map[string]string{"backend": "mcp", "endpoint": endpoint}

		body, err := json.Marshal(p)
		if err != nil {
			return tool.ToolResult{
				Success:  false,
				Duration: time.Since(start),
				Metadata: meta,
				Error: &tool.ToolError{
					Code: "param", Message: "序列化参数失败: " + err.Error(),
					Retryable: false, Cause: err,
				},
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return tool.ToolResult{
				Success:  false,
				Duration: time.Since(start),
				Metadata: meta,
				Error: &tool.ToolError{
					Code: "internal", Message: "构造 MCP 请求失败: " + err.Error(),
					Retryable: false, Cause: err,
				},
			}
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := mcpHTTPClient.Do(req)
		if err != nil {
			// ctx 取消或超时 → 不可重试（重试会再次超时；让上层调度决定）
			if ctx.Err() != nil {
				code := "cancelled"
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					code = "timeout"
				}
				return tool.ToolResult{
					Success:  false,
					Duration: time.Since(start),
					Metadata: meta,
					Error: &tool.ToolError{
						Code: code, Message: fmt.Sprintf("MCP 请求%s [%s]", code, endpoint),
						Retryable: code == "timeout", Cause: ctx.Err(),
					},
				}
			}
			// 网络层错误（连接拒绝、DNS、TLS）→ 可重试
			return tool.ToolResult{
				Success:  false,
				Duration: time.Since(start),
				Metadata: meta,
				Error: &tool.ToolError{
					Code: "network", Message: "MCP 请求失败: " + err.Error(),
					Retryable: true, Cause: err,
				},
			}
		}
		defer resp.Body.Close()
		meta["status_code"] = strconv.Itoa(resp.StatusCode)

		data, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return tool.ToolResult{
				Success:  false,
				Duration: time.Since(start),
				Metadata: meta,
				Error: &tool.ToolError{
					Code: "network", Message: "读取 MCP 响应失败: " + readErr.Error(),
					Retryable: true, Cause: readErr,
				},
			}
		}

		// HTTP 错误分类：4xx 不重试（参数/鉴权问题），5xx 可重试（服务端瞬时故障）
		if resp.StatusCode >= 400 {
			code := "http_4xx"
			retryable := false
			if resp.StatusCode >= 500 {
				code = "http_5xx"
				retryable = true
			}
			return tool.ToolResult{
				Success:  false,
				Payload:  string(data), // 保留 body 便于排查
				Duration: time.Since(start),
				Metadata: meta,
				Error: &tool.ToolError{
					Code: code, Message: fmt.Sprintf("MCP 返回 %d", resp.StatusCode),
					Retryable: retryable,
				},
			}
		}

		// 成功路径：尝试解析为 JSON 对象，失败时仅作为字符串载荷返回
		result := tool.ToolResult{
			Success:  true,
			Payload:  string(data),
			Duration: time.Since(start),
			Metadata: meta,
		}
		var asObj map[string]interface{}
		if json.Unmarshal(data, &asObj) == nil {
			result.PayloadJSON = asObj
		}
		return result
	}

	// 字符串版（向后兼容）：把 ToolResult 拍扁
	exec := func(ctx context.Context, p map[string]interface{}) (string, error) {
		r := structured(ctx, p)
		if !r.Success {
			return r.Payload, r.Error
		}
		return r.Payload, nil
	}

	return tool.Tool{
		Name:        name,
		Description: description,
		Parameters:  params,
		IsMCP:       true,
		Execute: func(p map[string]interface{}) (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), mcpDefaultTimeout)
			defer cancel()
			return exec(ctx, p)
		},
		ExecuteCtx:        exec,
		ExecuteStructured: structured,
	}
}
