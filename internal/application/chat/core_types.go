// types.go — UnifiedAgent 用到的数据结构（请求 / 响应 / 任务 / SSE 事件）。
//
// 这些类型同时出现在 process.go / mode_*.go / handler 等多处，单独抽出来便于阅读。
package chat

import (
	"agi-assistant/internal/domain/graph"
	"agi-assistant/internal/domain/rag"
	"agi-assistant/internal/domain/tool"
)

// ─────────────────────────────── ReAct 数据结构 ──────────────────────────

// StepType 是 ReAct 循环中的步骤类型
type StepType string

const (
	StepThought     StepType = "Thought"
	StepAction      StepType = "Action"
	StepObservation StepType = "Observation"
	StepFinalAnswer StepType = "Final Answer"
)

// ReActStep 记录 ReAct 循环的单个步骤
type ReActStep struct {
	Type    StepType          `json:"type"`
	Content string            `json:"content"`
	Tool    string            `json:"tool,omitempty"`
	Params  map[string]string `json:"params,omitempty"`
}

// ─────────────────────────────── Harness 数据结构 ────────────────────────

// TaskStepStatus 是任务步骤的执行状态
type TaskStepStatus string

const (
	StepPending     TaskStepStatus = "pending"
	StepRunning     TaskStepStatus = "running"
	StepDone        TaskStepStatus = "done"
	StepFailed      TaskStepStatus = "failed"
	StepInterrupted TaskStepStatus = "interrupted"
)

// TaskStep 是 Harness 中可重试的原子执行单元
type TaskStep struct {
	ID         int               `json:"id"`
	Name       string            `json:"name"`
	ToolName   string            `json:"tool_name"`
	Params     map[string]string `json:"params"`
	Status     TaskStepStatus    `json:"status"`
	Result     string            `json:"result,omitempty"`
	Error      string            `json:"error,omitempty"`
	RetryCount int               `json:"retry_count"`
}

// TaskState 描述一次任务的完整执行状态
type TaskState struct {
	TaskID        string           `json:"task_id"`
	Query         string           `json:"query"`
	Status        string           `json:"status"` // "running" | "completed" | "interrupted"
	Phase         string           `json:"phase"`  // "planning" | "executing" | "generating" | "done" | "interrupted"
	Steps         []TaskStep       `json:"steps"`
	CurrentStep   int              `json:"current_step"`
	InterruptedAt int              `json:"interrupted_at,omitempty"` // 在第几步被中断的（0-based）
	Result        string           `json:"result,omitempty"`
	Graph         *graph.TaskGraph `json:"graph,omitempty"` // 图执行时关联的 TaskGraph
}

// Snapshot 是某一时刻的任务状态快照（用于故障恢复）
type Snapshot struct {
	State     TaskState `json:"state"`
	Timestamp string    `json:"timestamp"`
}

// ─────────────────────────────── 统一响应 ────────────────────────────────

// Response 是 UnifiedAgent.Process 的输出，携带本次请求的全部上下文
type Response struct {
	Query          string             `json:"query"`
	Answer         string             `json:"answer"`
	Mode           string             `json:"mode"` // chat / tool / rag / memory / react
	Steps          []ReActStep        `json:"steps,omitempty"`
	ToolCall       *tool.CallResult   `json:"tool_call,omitempty"`
	SearchResults  []rag.SearchResult `json:"search_results,omitempty"`
	Task           *TaskState         `json:"task,omitempty"`
	ExtractedInfo  string             `json:"extracted_info,omitempty"`
	ShortTermCount int                `json:"short_term_count"`
	LongTermCount  int                `json:"long_term_count"`
	Preferences    map[string]string  `json:"preferences"`
	Interrupted    bool               `json:"interrupted,omitempty"`
}

// ─────────────────────────────── SSE 流式事件 ────────────────────────────────

// StreamEvent 是 SSE 流式推送的事件，handler 逐条写入 EventStream
type StreamEvent struct {
	Type string      `json:"type"` // route / step / token / tool_call / rag_result / memory / done
	Data interface{} `json:"data"`
}

// NewStreamEvent 创建一个 SSE 事件
func NewStreamEvent(eventType string, data interface{}) StreamEvent {
	return StreamEvent{Type: eventType, Data: data}
}

// ─────────────────────────────── 主处理流程 ──────────────────────────────

// ChatOptions 控制本次对话的路由行为
type ChatOptions struct {
	UseRAG bool // 是否使用 RAG 知识库
}
