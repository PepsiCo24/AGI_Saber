// Package tools — Tavily Search API 客户端。
//
// agent 在 search 工具触发时调用 TavilySearch；调用失败或未配置 API key 时
// 调用方应降级到 LLM 知识库直接回答。
package toolimpl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// TavilySearch 调用 Tavily Search API，返回格式化的搜索结果摘要。
// apiURL 为空时使用官方默认地址。
func TavilySearch(query, apiKey, apiURL string) (string, error) {
	if apiURL == "" {
		apiURL = "https://api.tavily.com/search"
	}
	body, _ := json.Marshal(map[string]interface{}{
		"api_key":      apiKey,
		"query":        query,
		"search_depth": "basic",
		"max_results":  5,
	})
	resp, err := http.Post(apiURL, "application/json", bytes.NewReader(body)) //nolint
	if err != nil {
		return "", fmt.Errorf("Tavily 请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("Tavily 返回错误状态: %d", resp.StatusCode)
	}
	var result struct {
		Answer  string `json:"answer"`
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("解析 Tavily 响应失败: %w", err)
	}
	// 优先返回 Tavily 合成的 answer
	if result.Answer != "" {
		var sb strings.Builder
		sb.WriteString(result.Answer)
		if len(result.Results) > 0 {
			sb.WriteString("\n\n**来源：**\n")
			for i, r := range result.Results {
				if i >= 3 {
					break
				}
				sb.WriteString(fmt.Sprintf("- [%s](%s)\n", r.Title, r.URL))
			}
		}
		return sb.String(), nil
	}
	// 无 answer 时拼接 top 结果摘要
	if len(result.Results) == 0 {
		return "", fmt.Errorf("Tavily 返回空结果")
	}
	var sb strings.Builder
	for i, r := range result.Results {
		if i >= 3 {
			break
		}
		sb.WriteString(fmt.Sprintf("**%s**\n%s\n%s\n\n", r.Title, r.Content, r.URL))
	}
	return strings.TrimSpace(sb.String()), nil
}
