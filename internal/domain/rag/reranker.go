// Package rag 重排序层（rerank）。
//
// 在 RRF 融合之后、送给 LLM 合成之前插入一层精排：
//   - 召回侧扩大候选量（fetchK = topK * 4），覆盖更多相关 chunk
//   - rerank 把候选打分排序后截回 topK，去掉"相关但不够精确"的噪声
//   - LLM 合成 prompt 更短、更聚焦，回答质量与速度同时受益
//
// 设计要点：
//   - Reranker 是接口；HybridStore 持有指针，nil 时走老路径（优雅降级）
//   - 默认实现 LLMReranker 用 listwise 打分，复用注入的 generateFn，
//     不引入新依赖；后续可加 CrossEncoderReranker / CohereReranker 等
//   - 解析失败一律回退到原始 RRF 顺序，永不让 rerank 阻塞主链路
package rag

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"agi-assistant/internal/pkg/logger"
)

// Reranker 重排序器接口
type Reranker interface {
	// Rerank 对候选 results 做精排，返回截断到 topK 的结果。
	// 实现失败时应返回原始 results 截断到 topK，永不返回 nil 或 error。
	Rerank(query string, results []HybridResult, topK int) []HybridResult
}

// ─────────────────────────────── LLMReranker ──────────────────────────────

// LLMReranker 用一次 LLM listwise 调用对所有候选打分（0~10）
type LLMReranker struct {
	generateFn func(systemPrompt, userMsg string) string
	// previewLen 给 LLM 看的每条 chunk 的最大字符数，控制 prompt 大小
	previewLen int
}

// NewLLMReranker 创建 LLM listwise 重排器
func NewLLMReranker(generateFn func(systemPrompt, userMsg string) string, previewLen int) *LLMReranker {
	if previewLen <= 0 {
		previewLen = 200
	}
	return &LLMReranker{generateFn: generateFn, previewLen: previewLen}
}

const rerankSystemPrompt = `你是检索系统的精排器。给定用户问题和若干候选段落（每条带编号 idx），
判断每条段落对回答该问题的**相关性 + 信息密度**，给 0~10 的整数分。

打分准则：
- 10：直接回答了问题
- 7~9：包含明确相关事实
- 4~6：弱相关 / 部分相关
- 1~3：仅出现共现关键词，不能用来回答
- 0：无关 / 噪声

输出**严格 JSON**，不要任何说明文字、不要 markdown 代码块：
{"scores": [{"idx": 0, "score": 9}, {"idx": 1, "score": 3}, ...]}

约束：
- scores 数量严格等于候选数量
- score 是 0~10 的整数
- 不依赖你自己的知识，只看给出的段落`

// Rerank 实现接口
func (r *LLMReranker) Rerank(query string, results []HybridResult, topK int) []HybridResult {
	if r.generateFn == nil || len(results) == 0 {
		return truncate(results, topK)
	}
	if len(results) == 1 {
		return results
	}

	// 构造候选清单（截断每条 chunk 控制 prompt 大小）
	var sb strings.Builder
	sb.WriteString("用户问题：")
	sb.WriteString(query)
	sb.WriteString("\n\n候选段落：\n")
	for i, r0 := range results {
		preview := r0.Chunk.Content
		if runeLen(preview) > r.previewLen {
			preview = string([]rune(preview)[:r.previewLen]) + "…"
		}
		fmt.Fprintf(&sb, "[%d] %s\n", i, preview)
	}

	raw := r.generateFn(rerankSystemPrompt, sb.String())
	scores := parseRerankJSON(raw)
	if len(scores) == 0 {
		logger.L().Warn("rerank parse failed, falling back to RRF order", "raw_preview", firstN(raw, 100))
		return truncate(results, topK)
	}

	// 把 LLM 分数映射回结果上：以 LLM score 为主排序键，原 RRF score 作 tiebreaker
	type scored struct {
		idx int
		llm float64
		rrf float64
		hr  HybridResult
	}
	pool := make([]scored, 0, len(results))
	matched := make(map[int]float64, len(scores))
	for _, s := range scores {
		if s.Idx >= 0 && s.Idx < len(results) {
			matched[s.Idx] = s.Score
		}
	}
	for i, hr := range results {
		ls, ok := matched[i]
		if !ok {
			// 未给分的兜底为低分，但不丢弃，避免 LLM 漏判把好结果删掉
			ls = -1
		}
		pool = append(pool, scored{idx: i, llm: ls, rrf: hr.Score, hr: hr})
	}
	sort.SliceStable(pool, func(i, j int) bool {
		if pool[i].llm != pool[j].llm {
			return pool[i].llm > pool[j].llm
		}
		return pool[i].rrf > pool[j].rrf
	})

	// 把最终排名作为新分数写回（保持 0~1 区间，便于前端展示）
	out := make([]HybridResult, 0, len(pool))
	for rank, p := range pool {
		hr := p.hr
		if p.llm >= 0 {
			hr.Score = p.llm / 10.0
		}
		hr.Source = hr.Source + "+rerank"
		out = append(out, hr)
		_ = rank
	}
	return truncate(out, topK)
}

type rerankItem struct {
	Idx   int     `json:"idx"`
	Score float64 `json:"score"`
}

func parseRerankJSON(raw string) []rerankItem {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	var resp struct {
		Scores []rerankItem `json:"scores"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return nil
	}
	return resp.Scores
}

func truncate(results []HybridResult, topK int) []HybridResult {
	if topK > 0 && len(results) > topK {
		return results[:topK]
	}
	return results
}
