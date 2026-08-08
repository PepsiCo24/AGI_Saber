// Package promptctx 实现 Schema-driven Runtime Context Assembly。
//
// 每轮推理前，根据当前 Mode 选取一个 RuntimeContextSchema（认知槽位编排），
// 并通过注册到 ContextAssembler 的 ContextSource 填充各槽位：
//
//	Long-term Profile  — 用户稳定身份与偏好
//	Planner State      — 当前任务规划/阶段
//	Task Memory        — 当前任务的步骤观察缓存
//	Tool State         — 可用工具与近期调用结果
//	Constraints        — 沙箱政策、硬性约束
//	Recall Memory      — 受 SlotFilter 约束的语义召回（兜底）
//
// 仅匹配认知槽位的记忆被注入 Prompt，避免 Top-K 召回的污染。
package promptctx

// SlotKind 是认知槽位的类别标识
type SlotKind string

const (
	SlotProfile     SlotKind = "profile"
	SlotPlanner     SlotKind = "planner"
	SlotTaskMem     SlotKind = "task_memory"
	SlotToolState   SlotKind = "tool_state"
	SlotConstraints SlotKind = "constraints"
	SlotRecall      SlotKind = "recall_memory"
)

// SlotFilter 描述 Source 在填充槽位时的过滤约束
type SlotFilter struct {
	Categories  []string // 命中其一即可，空表示不限
	RequireTags []string // 必须全部包含
	MinScore    float64  // 召回综合分阈值
	TopK        int      // 单槽位最多返回项数（0 表示不截断）
	MaxAgeHours int      // 最大年龄（小时），0 表示不限
	TokenBudget int      // 单槽位字符预算（粗略以字符数近似 token）
}

// Slot 是 Schema 中的单个认知槽位定义
type Slot struct {
	Kind     SlotKind
	Required bool       // Required 槽位即使为空也会渲染占位
	Filter   SlotFilter // 传给 ContextSource 的过滤参数
	Template string     // render.go 中模板键，留空时使用 Kind
}

// ContextItem 是单条已装入槽位的内容
type ContextItem struct {
	Text   string
	Score  float64
	Source string            // 调试用：标记来自哪个 ContextSource
	Meta   map[string]string // 调试用元数据
}

// FilledSlot 是装配后的单个槽位结果
type FilledSlot struct {
	Kind    SlotKind
	Items   []ContextItem
	Skipped bool   // 因预算或无数据被跳过
	Reason  string // 跳过原因（debug）
}
