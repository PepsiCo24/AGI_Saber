// Package tool 定义 Agent 可调用的工具抽象与基于规则的工具选择逻辑。
//
// 这个包是纯 domain：
//   - Tool / Param / CallResult / ToolResult / ToolError 是领域类型
//   - Decide 是基于关键词的工具选择规则
//   - 不持有任何具体工具实现（time/weather/search/exec_command 在 infrastructure/tool）
package tool

import (
	"context"
	"strings"
	"time"
)

// Param 描述工具的单个参数（用于前端展示和 LLM function-calling schema）
type Param struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// Tool 是可被 Agent 调用的原子能力单元
type Tool struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Parameters  []Param `json:"parameters"`
	IsMCP       bool    `json:"is_mcp,omitempty"` // 是否为外部 MCP 工具
	// Execute 执行工具逻辑，params 对应 Parameters 中声明的参数。
	// 老接口：返回字符串载荷 + error，绝大多数内置工具走这条。
	Execute func(params map[string]interface{}) (string, error) `json:"-"`
	// ExecuteCtx 是带 ctx 的可选执行入口。当工具实现侧能感知 ctx
	// （比如 MCP 走 HTTP）时填充；GraphRuntime 优先调用 ExecuteCtx，
	// 不存在时回落到 Execute。
	ExecuteCtx func(ctx context.Context, params map[string]interface{}) (string, error) `json:"-"`
	// ExecuteStructured 是带 ctx 的结构化执行入口（试点）。
	// 工具实现侧填充时返回 ToolResult，可携带 Success/Error/Duration/Metadata。
	// 当前仅 MCP 工具试点；GraphRuntime 检测到此字段时优先调用。
	ExecuteStructured func(ctx context.Context, params map[string]interface{}) ToolResult `json:"-"`
}

// ToolError 是结构化工具错误。Retryable 字段告诉 GraphRuntime
// 当前错误是否值得重试（例如 5xx / 网络抖动 retryable=true，4xx / 参数错 retryable=false）。
type ToolError struct {
	Code      string `json:"code"`      // 简短错误码：timeout / network / http_4xx / http_5xx / parse / param / internal
	Message   string `json:"message"`   // 人类可读消息
	Retryable bool   `json:"retryable"` // 是否可重试
	Cause     error  `json:"-"`         // 原始 error，仅日志用
}

func (e *ToolError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// ToolResult 是工具调用的结构化结果（试点）。
//
// 设计意图：
//   - Payload 保留原始字符串载荷，向后兼容旧 string 接口
//   - PayloadJSON 在工具知道自己返回的是 JSON 时填充，便于上游精排/拼装
//   - Error 让重试策略有判定依据（替代过去只看 error.Error() 字面量）
//   - Duration / Metadata 给观测和调试用
type ToolResult struct {
	Success     bool                   `json:"success"`
	Payload     string                 `json:"payload,omitempty"`      // 字符串载荷（向后兼容）
	PayloadJSON map[string]interface{} `json:"payload_json,omitempty"` // 结构化载荷（可选）
	Error       *ToolError             `json:"error,omitempty"`
	Duration    time.Duration          `json:"duration"`
	Metadata    map[string]string      `json:"metadata,omitempty"` // backend/endpoint/status_code 等
}

// CallResult 记录一次工具调用的完整上下文（供响应和日志使用）
type CallResult struct {
	ToolName   string                 `json:"tool_name"`
	Params     map[string]interface{} `json:"params"`
	ToolResult string                 `json:"tool_result"`
}

// ─────────────────────────────── 工具选择 ────────────────────────────────

// Decide 基于规则推断应调用的工具及参数。
// 裁剪后内置工具只剩 search_web；命中检索类关键词走 search_web，
// 否则回落到工具集中的第一个工具（含 skill_* / MCP）。
// 只会返回 ts 中实际存在的工具；ts 为空时返回 nil。
func Decide(query string, ts map[string]Tool) *CallResult {
	q := strings.ToLower(query)

	if strings.Contains(q, "查") || strings.Contains(q, "搜索") || strings.Contains(q, "是什么") {
		if _, ok := ts["search_web"]; ok {
			return &CallResult{ToolName: "search_web", Params: map[string]interface{}{"query": query}}
		}
	}

	// 无规则命中或命中工具不在集合中时，取集合中第一个工具兜底
	// 使用工具的首个必填参数名而非硬编码 "query"
	for name, t := range ts {
		paramName := "query"
		for _, p := range t.Parameters {
			if p.Required {
				paramName = p.Name
				break
			}
		}
		return &CallResult{ToolName: name, Params: map[string]interface{}{paramName: query}}
	}
	return nil
}
