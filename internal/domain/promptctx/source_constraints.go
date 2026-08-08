package promptctx

import (
	"context"
	"fmt"

	"agi-assistant/internal/domain/sandbox"
)

// ConstraintsSource 装填 Constraints 槽位
// 来源：sandbox 的静态安全政策（启动时一次性快照，运行期不变）
type ConstraintsSource struct {
	policies []sandbox.Policy
}

// NewConstraintsSource 创建 Constraints source。policies 通常通过 sandbox.PolicySnapshot() 传入
func NewConstraintsSource(policies []sandbox.Policy) *ConstraintsSource {
	cp := make([]sandbox.Policy, len(policies))
	copy(cp, policies)
	return &ConstraintsSource{policies: cp}
}

// ID 返回 source 标识
func (s *ConstraintsSource) ID() string { return "constraints" }

// Supports 仅声明支持 Constraints 槽位
func (s *ConstraintsSource) Supports(kind SlotKind) bool { return kind == SlotConstraints }

// Fetch 渲染政策：Block 优先于 Warn，按 slot.Filter.TopK 截断
func (s *ConstraintsSource) Fetch(ctx context.Context, slot Slot, q Query) ([]ContextItem, error) {
	if len(s.policies) == 0 {
		return nil, nil
	}

	// 按 Level 拆分：Block 在前
	var blocks, warns []sandbox.Policy
	for _, p := range s.policies {
		if p.Level == sandbox.RiskBlock {
			blocks = append(blocks, p)
		} else {
			warns = append(warns, p)
		}
	}

	ordered := append(blocks, warns...)
	topK := slot.Filter.TopK
	if topK > 0 && len(ordered) > topK {
		ordered = ordered[:topK]
	}

	items := make([]ContextItem, 0, len(ordered))
	for _, p := range ordered {
		level := "禁止"
		score := 1.0
		if p.Level != sandbox.RiskBlock {
			level = "告警"
			score = 0.5
		}
		items = append(items, ContextItem{
			Text:   fmt.Sprintf("[%s] %s", level, p.Reason),
			Score:  score,
			Source: s.ID(),
			Meta:   map[string]string{"level": string(p.Level), "pattern": p.Pattern},
		})
	}
	return items, nil
}
