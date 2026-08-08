package promptctx

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"agi-assistant/internal/domain/tool"
)

// ToolCallTrace 是单次工具调用的简要记录（用于 Tool State 槽位）
type ToolCallTrace struct {
	ToolName  string
	Success   bool
	Summary   string // 截断后的结果或错误摘要
	CreatedAt time.Time
}

// ToolStateTracker 持有最近 N 次工具调用的环形缓冲，供 ToolStateSource 读取
type ToolStateTracker struct {
	mu  sync.RWMutex
	buf []ToolCallTrace
	max int
}

// NewToolStateTracker 创建最多保留 max 次调用的 tracker
func NewToolStateTracker(max int) *ToolStateTracker {
	if max <= 0 {
		max = 10
	}
	return &ToolStateTracker{max: max}
}

// Record 追加一次工具调用记录
func (t *ToolStateTracker) Record(trace ToolCallTrace) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if trace.CreatedAt.IsZero() {
		trace.CreatedAt = time.Now()
	}
	if len(trace.Summary) > 120 {
		trace.Summary = trace.Summary[:120] + "…"
	}
	t.buf = append(t.buf, trace)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
}

// Snapshot 返回当前调用历史的只读副本
func (t *ToolStateTracker) Snapshot() []ToolCallTrace {
	t.mu.RLock()
	defer t.mu.RUnlock()
	cp := make([]ToolCallTrace, len(t.buf))
	copy(cp, t.buf)
	return cp
}

// ToolRegistryProvider 由 agent 实现，返回当前可用工具映射
type ToolRegistryProvider func() map[string]tool.Tool

// ToolStateSource 装填 Tool State 槽位
type ToolStateSource struct {
	registry ToolRegistryProvider
	tracker  *ToolStateTracker
}

// NewToolStateSource 创建 Tool State source
func NewToolStateSource(registry ToolRegistryProvider, tracker *ToolStateTracker) *ToolStateSource {
	return &ToolStateSource{registry: registry, tracker: tracker}
}

// ID 返回 source 标识
func (s *ToolStateSource) ID() string { return "tool_state" }

// Supports 仅声明支持 ToolState 槽位
func (s *ToolStateSource) Supports(kind SlotKind) bool { return kind == SlotToolState }

// Fetch 输出工具清单（描述 + 必填参数）+ 近期调用结果
func (s *ToolStateSource) Fetch(ctx context.Context, slot Slot, q Query) ([]ContextItem, error) {
	var items []ContextItem

	if s.registry != nil {
		toolMap := s.registry()
		names := make([]string, 0, len(toolMap))
		for name := range toolMap {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			t := toolMap[name]
			var paramHint string
			for _, p := range t.Parameters {
				if p.Required {
					if paramHint != "" {
						paramHint += ", "
					}
					paramHint += p.Name
				}
			}
			if paramHint != "" {
				paramHint = "（必填 " + paramHint + "）"
			}
			items = append(items, ContextItem{
				Text:   fmt.Sprintf("%s — %s%s", name, t.Description, paramHint),
				Source: s.ID(),
				Meta:   map[string]string{"tool": name},
			})
		}
	}

	if s.tracker != nil {
		traces := s.tracker.Snapshot()
		topK := slot.Filter.TopK
		if topK > 0 && len(traces) > topK {
			traces = traces[len(traces)-topK:]
		}
		for _, tr := range traces {
			status := "成功"
			if !tr.Success {
				status = "失败"
			}
			items = append(items, ContextItem{
				Text:   fmt.Sprintf("近期调用 %s [%s]: %s", tr.ToolName, status, tr.Summary),
				Source: s.ID(),
				Meta:   map[string]string{"tool": tr.ToolName, "status": status},
			})
		}
	}

	return items, nil
}
