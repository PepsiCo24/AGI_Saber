// Package tools 定义 Agent 可调用的工具，以及基于规则的工具选择逻辑。
// 每个工具包含名称、描述、参数 Schema 和可执行函数。
package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	// Execute 执行工具逻辑，params 对应 Parameters 中声明的参数
	Execute func(params map[string]interface{}) (string, error) `json:"-"`
}

// CallResult 记录一次工具调用的完整上下文（供响应和日志使用）
type CallResult struct {
	ToolName   string                 `json:"tool_name"`
	Params     map[string]interface{} `json:"params"`
	ToolResult string                 `json:"tool_result"`
}

// ─────────────────────────────── 内置工具 ────────────────────────────────

// GetTime 返回当前时间，支持可选时区参数
func GetTime() Tool {
	return Tool{
		Name:        "get_time",
		Description: "获取当前时间",
		Parameters:  []Param{{Name: "timezone", Type: "string", Description: "时区（如 Asia/Tokyo）", Required: false}},
		Execute: func(p map[string]interface{}) (string, error) {
			loc := time.Local
			if v, ok := p["timezone"].(string); ok && v != "" {
				if l, err := time.LoadLocation(v); err == nil {
					loc = l
				}
			}
			return time.Now().In(loc).Format("2006-01-02 15:04:05"), nil
		},
	}
}

// GetWeather 返回指定城市的模拟天气信息
func GetWeather() Tool {
	db := map[string]string{
		"北京": "晴天 22°C",
		"东京": "多云 18°C 湿度65%",
		"上海": "小雨 20°C",
		"纽约": "晴天 15°C",
		"伦敦": "阴天 12°C",
		"广州": "晴天 28°C",
		"深圳": "晴天 26°C",
	}
	return Tool{
		Name:        "get_weather",
		Description: "获取城市天气信息",
		Parameters:  []Param{{Name: "city", Type: "string", Description: "城市名称", Required: true}},
		Execute: func(p map[string]interface{}) (string, error) {
			city, _ := p["city"].(string)
			if w, ok := db[city]; ok {
				return fmt.Sprintf("%s：%s", city, w), nil
			}
			return fmt.Sprintf("%s：晴天 20°C（模拟）", city), nil
		},
	}
}

// SearchWeb 模拟互联网关键词搜索
func SearchWeb() Tool {
	db := map[string]string{
		"AI应用工程师": "AI 应用工程师是将 AI 技术落地到业务的工程师，需具备 ML 基础、API 开发、Prompt 工程等能力。",
		"Go语言":    "Go 是 Google 开发的开源编程语言，适用于高并发服务端应用。Docker 即用 Go 开发。",
	}
	return Tool{
		Name:        "search_web",
		Description: "搜索互联网获取最新信息",
		Parameters:  []Param{{Name: "query", Type: "string", Description: "搜索关键词", Required: true}},
		Execute: func(p map[string]interface{}) (string, error) {
			q, _ := p["query"].(string)
			for k, v := range db {
				if strings.Contains(q, k) {
					return v, nil
				}
			}
			return fmt.Sprintf("关于「%s」的搜索结果（模拟）", q), nil
		},
	}
}

// DefaultTools 返回所有内置工具的映射表（不含 rag_search，由 agent 动态注入）
func DefaultTools() map[string]Tool {
	list := []Tool{GetTime(), GetWeather(), SearchWeb()}
	m := make(map[string]Tool, len(list))
	for _, t := range list {
		m[t.Name] = t
	}
	return m
}

// ─────────────────────────────── 工具选择 ────────────────────────────────

// Decide 基于规则推断应调用的工具及参数。
// 只会返回 ts 中实际存在的工具；若规则匹配到的工具不在 ts 中则返回 nil。
func Decide(query string, ts map[string]Tool) *CallResult {
	q := strings.ToLower(query)

	if strings.Contains(q, "几点") || strings.Contains(q, "时间") {
		if _, ok := ts["get_time"]; ok {
			params := map[string]interface{}{}
			if strings.Contains(q, "东京") {
				params["timezone"] = "Asia/Tokyo"
			}
			return &CallResult{ToolName: "get_time", Params: params}
		}
	}

	if strings.Contains(q, "天气") {
		if _, ok := ts["get_weather"]; ok {
			city := "北京"
			for _, c := range []string{"东京", "北京", "上海", "纽约", "伦敦", "广州", "深圳"} {
				if strings.Contains(q, c) {
					city = c
					break
				}
			}
			return &CallResult{ToolName: "get_weather", Params: map[string]interface{}{"city": city}}
		}
	}

	if strings.Contains(q, "查") || strings.Contains(q, "搜索") || strings.Contains(q, "是什么") {
		if _, ok := ts["search_web"]; ok {
			return &CallResult{ToolName: "search_web", Params: map[string]interface{}{"query": query}}
		}
	}

	// 无规则命中或命中工具不在集合中时，取集合中第一个工具兜底
	for name, _ := range ts {
		return &CallResult{ToolName: name, Params: map[string]interface{}{"query": query}}
	}
	return nil
}

// NewMCPTool 创建一个调用外部 HTTP 端点的 MCP 兼容工具。
// 请求体为 JSON 对象（params），响应体作为工具结果返回。
func NewMCPTool(name, description, endpoint string, params []Param) Tool {
	return Tool{
		Name:        name,
		Description: description,
		Parameters:  params,
		IsMCP:       true,
		Execute: func(p map[string]interface{}) (string, error) {
			body, err := json.Marshal(p)
			if err != nil {
				return "", fmt.Errorf("序列化参数失败: %w", err)
			}
			resp, err := http.Post(endpoint, "application/json", bytes.NewReader(body)) //nolint
			if err != nil {
				return "", fmt.Errorf("MCP 请求失败 [%s]: %w", endpoint, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 400 {
				return "", fmt.Errorf("MCP 返回错误状态 %d [%s]", resp.StatusCode, endpoint)
			}
			data, err := io.ReadAll(resp.Body)
			if err != nil {
				return "", fmt.Errorf("读取 MCP 响应失败: %w", err)
			}
			return string(data), nil
		},
	}
}
