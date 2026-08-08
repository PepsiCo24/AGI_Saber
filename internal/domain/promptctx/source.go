package promptctx

import "context"

// Query 是装配一次上下文时的输入快照
type Query struct {
	Text      string    // 用户当前输入
	Embedding []float64 // 已计算的 query embedding（可为空）
	TaskID    string    // 当前任务 ID（用于 Task Memory）
	Mode      string    // chat / tool / react / rag
	UserID    string    // 当前请求归属用户——给 Profile/Recall 等 source 用作隔离过滤键
}

// ContextSource 是某类认知槽位的数据提供者。
// 一个 source 可声明支持多个 SlotKind（例如 Profile source 同时填 Profile/Recall 都行）。
type ContextSource interface {
	ID() string
	Supports(SlotKind) bool
	// Fetch 在不超过 slot.Filter.TokenBudget 的前提下，返回适合该槽位的 ContextItem。
	// 实现需自己做 TopK 截断与 budget 裁剪。
	Fetch(ctx context.Context, slot Slot, q Query) ([]ContextItem, error)
}
