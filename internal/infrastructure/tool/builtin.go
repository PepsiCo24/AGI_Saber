// Package toolimpl 提供 domain/tool.Tool 的内置实现。
//
// 本次裁剪后内置通用工具只保留 search_web（联网搜索）；
// get_time / get_weather / exec_command / 文档工具 / rag_search 已下线。
// 内置工具被分到 infrastructure 层是因为它们涉及外部 IO（HTTP 调用等）。
package toolimpl

import (
	"fmt"
	"strings"

	"agi-assistant/internal/domain/tool"
)

// SearchWeb 模拟互联网关键词搜索（真实实现由 agent 的 registerBuiltinTools 用
// Tavily + LLM 降级覆盖；此处作为注册表初始占位）。
func SearchWeb() tool.Tool {
	db := map[string]string{
		"AI应用工程师": "AI 应用工程师是将 AI 技术落地到业务的工程师，需具备 ML 基础、API 开发、Prompt 工程等能力。",
		"Go语言":    "Go 是 Google 开发的开源编程语言，适用于高并发服务端应用。Docker 即用 Go 开发。",
	}
	return tool.Tool{
		Name:        "search_web",
		Description: "搜索互联网获取最新信息",
		Parameters:  []tool.Param{{Name: "query", Type: "string", Description: "搜索关键词", Required: true}},
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

// DefaultTools 返回内置工具映射表：裁剪后只含 search_web。
func DefaultTools() map[string]tool.Tool {
	list := []tool.Tool{SearchWeb()}
	m := make(map[string]tool.Tool, len(list))
	for _, t := range list {
		m[t.Name] = t
	}
	return m
}
