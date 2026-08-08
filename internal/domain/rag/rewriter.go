// Package rag Query 改写器：检索前的查询理解层。
//
// 解决两类问题：
//  1. 多轮指代 / 省略：用 STM 历史把 "那个再展开讲讲" 改写成自包含的独立查询
//  2. 单一查询召回偏窄：让 LLM 生成 N 条等价但措辞不同的查询，三路检索对每条
//     并发执行，结果用 RRF 合并，显著提升召回覆盖率
//
// 设计要点：
//   - Rewriter 是接口；默认实现为 LLMRewriter，复用注入的 generateFn，
//     不引入新依赖；llm 不可用时优雅降级为 [original_query]
//   - 失败 / 解析异常时一律 fallback 到原查询（never block）
//   - HistoryMessage 抽象成最小结构，避免 rag 包反向依赖 memory 包
package rag

import (
	"encoding/json"
	"strings"

	"agi-assistant/internal/pkg/logger"
)

// HistoryMessage 是 Rewriter 看到的对话历史最小结构
type HistoryMessage struct {
	Role    string // "user" | "assistant"
	Content string
}

// Rewriter 查询改写器接口
type Rewriter interface {
	// Rewrite 接收原始 query 和最近 N 轮对话历史，返回 ≥1 条改写后的查询。
	// 第一条建议是 history-aware 的"独立查询"，后续是同义改写。
	// 实现失败时应返回 [original]（永不返回空切片或 error 让上层降级）。
	Rewrite(query string, history []HistoryMessage) []string
}

// ─────────────────────────────── LLMRewriter ──────────────────────────────

// LLMRewriter 用 LLM 做改写，受 numQueries 控制最终条数
type LLMRewriter struct {
	generateFn func(systemPrompt, userMsg string) string
	numQueries int // 含原查询在内的目标改写条数（建议 3）
}

// NewLLMRewriter 创建 LLM 改写器；generateFn 为 nil 或 numQueries<=1 时
// Rewrite 直接返回原查询，等价于关闭改写
func NewLLMRewriter(generateFn func(systemPrompt, userMsg string) string, numQueries int) *LLMRewriter {
	if numQueries <= 0 {
		numQueries = 3
	}
	return &LLMRewriter{generateFn: generateFn, numQueries: numQueries}
}

const rewriteSystemPrompt = `你是检索系统的查询改写助手。给定用户当前问题和最近对话历史，你需要：
1) 先把当前问题改写成一句**自包含的独立查询**（消除指代、补全省略，只用 query 本身就能让人看懂）。
2) 再生成若干条**等价但措辞不同**的查询变体（同义词替换、抽象/具体切换、不同语序）。

输出**严格 JSON**，不要任何说明文字、不要 markdown 代码块：
{"queries": ["独立查询", "变体1", "变体2"]}

约束：
- 总条数严格等于 %d
- 每条不超过 50 字
- 不要编造历史中未出现的实体
- 第一条必须可独立检索（不依赖历史）`

// Rewrite 实现接口
func (r *LLMRewriter) Rewrite(query string, history []HistoryMessage) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	if r.generateFn == nil || r.numQueries <= 1 {
		return []string{query}
	}

	// 构造历史摘要，限制长度避免 prompt 过长
	var hb strings.Builder
	if len(history) > 0 {
		hb.WriteString("最近对话历史（按时间顺序）：\n")
		// 最多取最近 6 条，避免 prompt 失控
		start := 0
		if len(history) > 6 {
			start = len(history) - 6
		}
		for _, m := range history[start:] {
			role := m.Role
			if role == "" {
				role = "user"
			}
			content := strings.TrimSpace(m.Content)
			if runeLen(content) > 200 {
				content = string([]rune(content)[:200]) + "…"
			}
			hb.WriteString("[" + role + "] " + content + "\n")
		}
	} else {
		hb.WriteString("（无历史，直接改写当前问题）\n")
	}
	hb.WriteString("\n当前问题：" + query)

	systemPrompt := strings.Replace(rewriteSystemPrompt, "%d", itoa(r.numQueries), 1)
	raw := r.generateFn(systemPrompt, hb.String())
	queries := parseRewriteJSON(raw)
	if len(queries) == 0 {
		logger.L().Warn("query rewrite parse failed, using original query", "raw_preview", firstN(raw, 100))
		return []string{query}
	}

	// 兜底：始终保留原查询，避免改写完全跑偏后召回归零
	queries = dedupKeepOrder(append(queries, query))
	if len(queries) > r.numQueries {
		queries = queries[:r.numQueries]
	}
	return queries
}

// parseRewriteJSON 容忍 markdown 代码块包裹
func parseRewriteJSON(raw string) []string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	var resp struct {
		Queries []string `json:"queries"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return nil
	}
	out := make([]string, 0, len(resp.Queries))
	for _, q := range resp.Queries {
		q = strings.TrimSpace(q)
		if q != "" {
			out = append(out, q)
		}
	}
	return out
}

func dedupKeepOrder(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := in[:0]
	for _, s := range in {
		key := strings.ToLower(strings.TrimSpace(s))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, s)
	}
	return out
}

func itoa(n int) string {
	// 避免引入 strconv 依赖（保持本文件最小依赖面）；本函数只用于个位/十位数
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
