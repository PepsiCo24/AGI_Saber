package promptctx

import (
	"context"
	"fmt"

	"agi-assistant/internal/domain/memory/longterm"
)

// Recaller 抽象 LongTerm / GraphMemory 共有的过滤召回能力
type Recaller interface {
	RecallByFilter(query string, queryEmbedding []float64, filter longterm.RecallFilter) []longterm.Item
}

// RecallSource 装填 Recall 槽位（兜底语义召回）
// 实现 Recaller 即可作为 source，因此 LongTerm 与 GraphMemory 都可挂接
type RecallSource struct {
	recaller Recaller
}

// NewRecallSource 创建 Recall source
func NewRecallSource(recaller Recaller) *RecallSource {
	return &RecallSource{recaller: recaller}
}

// ID 返回 source 标识
func (s *RecallSource) ID() string { return "recall" }

// Supports 仅声明支持 Recall 槽位
func (s *RecallSource) Supports(kind SlotKind) bool { return kind == SlotRecall }

// Fetch 按 SlotFilter 做语义召回
func (s *RecallSource) Fetch(ctx context.Context, slot Slot, q Query) ([]ContextItem, error) {
	if s.recaller == nil {
		return nil, nil
	}
	// UserID 为空时不召回——避免登录前的请求把别人的记忆灌进 prompt
	if q.UserID == "" {
		return nil, nil
	}
	filter := longterm.RecallFilter{
		UserID:      q.UserID,
		Categories:  slot.Filter.Categories,
		RequireTags: slot.Filter.RequireTags,
		MinScore:    slot.Filter.MinScore,
		TopK:        slot.Filter.TopK,
		MaxAgeHours: slot.Filter.MaxAgeHours,
	}
	hits := s.recaller.RecallByFilter(q.Text, q.Embedding, filter)
	if len(hits) == 0 {
		return nil, nil
	}
	items := make([]ContextItem, 0, len(hits))
	for _, h := range hits {
		meta := map[string]string{}
		if h.Category != "" {
			meta["category"] = h.Category
		}
		if h.SlotHint != "" {
			meta["slot_hint"] = h.SlotHint
		}
		items = append(items, ContextItem{
			Text:   fmt.Sprintf("%s（重要性=%.2f, 综合分=%.2f）", h.Content, h.Importance, h.Score),
			Score:  h.Score,
			Source: s.ID(),
			Meta:   meta,
		})
	}
	return items, nil
}
