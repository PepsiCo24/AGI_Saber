package promptctx

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// StepObservation 是任务执行过程中单步工具观察的快照
type StepObservation struct {
	StepID    int
	ToolName  string
	Result    string
	Error     string
	Success   bool
	CreatedAt time.Time
}

// TaskMemBuffer 是当前任务的步骤观察缓冲区（in-memory ring buffer）。
// agent 在每步工具执行后 Push；TaskMemSource 从中读取。
type TaskMemBuffer struct {
	mu  sync.RWMutex
	buf []StepObservation
	max int
}

// NewTaskMemBuffer 创建最多保留 max 条观察的缓冲区
func NewTaskMemBuffer(max int) *TaskMemBuffer {
	if max <= 0 {
		max = 20
	}
	return &TaskMemBuffer{max: max}
}

// Push 追加一条步骤观察（超出 max 时丢弃最早条目）
func (b *TaskMemBuffer) Push(obs StepObservation) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if obs.CreatedAt.IsZero() {
		obs.CreatedAt = time.Now()
	}
	b.buf = append(b.buf, obs)
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
}

// Reset 清空缓冲区（新任务开始时调用）
func (b *TaskMemBuffer) Reset() {
	b.mu.Lock()
	b.buf = b.buf[:0]
	b.mu.Unlock()
}

// Snapshot 返回当前全部观察的只读副本
func (b *TaskMemBuffer) Snapshot() []StepObservation {
	b.mu.RLock()
	defer b.mu.RUnlock()
	cp := make([]StepObservation, len(b.buf))
	copy(cp, b.buf)
	return cp
}

// TaskMemSource 装填 Task Memory 槽位
type TaskMemSource struct {
	buf *TaskMemBuffer
}

// NewTaskMemSource 创建 Task Memory source
func NewTaskMemSource(buf *TaskMemBuffer) *TaskMemSource {
	return &TaskMemSource{buf: buf}
}

// ID 返回 source 标识
func (s *TaskMemSource) ID() string { return "task_memory" }

// Supports 仅声明支持 TaskMem 槽位
func (s *TaskMemSource) Supports(kind SlotKind) bool { return kind == SlotTaskMem }

// Fetch 返回近期步骤观察
func (s *TaskMemSource) Fetch(ctx context.Context, slot Slot, q Query) ([]ContextItem, error) {
	if s.buf == nil {
		return nil, nil
	}
	obs := s.buf.Snapshot()
	if len(obs) == 0 {
		return nil, nil
	}

	topK := slot.Filter.TopK
	if topK > 0 && len(obs) > topK {
		obs = obs[len(obs)-topK:]
	}

	var items []ContextItem
	for _, o := range obs {
		text := fmt.Sprintf("步骤%d [%s]", o.StepID, o.ToolName)
		if o.Success {
			r := o.Result
			if len(r) > 200 {
				r = r[:200] + "…"
			}
			text += "→" + r
		} else {
			text += " 失败: " + o.Error
		}
		items = append(items, ContextItem{
			Text:   text,
			Source: s.ID(),
			Meta:   map[string]string{"tool": o.ToolName},
		})
	}
	return items, nil
}
