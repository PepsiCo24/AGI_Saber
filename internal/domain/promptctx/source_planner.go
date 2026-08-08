package promptctx

import (
	"context"
	"fmt"
)

// PlannerSnapshot 是 Planner 当前状态的只读视图。
// agent 包通过 PlannerProvider 暴露 TaskState 的快照，避免 runtime 反向依赖 agent。
type PlannerSnapshot struct {
	TaskID        string
	Query         string
	Status        string // running / completed / interrupted
	Phase         string // planning / executing / generating / done / interrupted
	TotalSteps    int
	CurrentStep   int
	InterruptedAt int
	NextStepName  string // 即将执行的步骤描述（若有）
	NextStepTool  string
}

// PlannerProvider 由 agent 实现，返回当前任务的 Planner 状态。
// 没有正在执行的任务时返回 nil。
type PlannerProvider func() *PlannerSnapshot

// PlannerSource 装填 Planner State 槽位
type PlannerSource struct {
	get PlannerProvider
}

// NewPlannerSource 创建 Planner source
func NewPlannerSource(get PlannerProvider) *PlannerSource {
	return &PlannerSource{get: get}
}

// ID 返回 source 标识
func (s *PlannerSource) ID() string { return "planner" }

// Supports 仅声明支持 Planner 槽位
func (s *PlannerSource) Supports(kind SlotKind) bool { return kind == SlotPlanner }

// Fetch 渲染 Planner 当前阶段与下一步
func (s *PlannerSource) Fetch(ctx context.Context, slot Slot, q Query) ([]ContextItem, error) {
	if s.get == nil {
		return nil, nil
	}
	snap := s.get()
	if snap == nil {
		return nil, nil
	}
	var items []ContextItem
	items = append(items, ContextItem{
		Text:   fmt.Sprintf("任务 %s 状态=%s 阶段=%s", snap.TaskID, snap.Status, snap.Phase),
		Source: s.ID(),
	})
	if snap.TotalSteps > 0 {
		items = append(items, ContextItem{
			Text:   fmt.Sprintf("进度：第 %d/%d 步", snap.CurrentStep+1, snap.TotalSteps),
			Source: s.ID(),
		})
	}
	if snap.NextStepName != "" {
		items = append(items, ContextItem{
			Text:   fmt.Sprintf("下一步：%s（工具=%s）", snap.NextStepName, snap.NextStepTool),
			Source: s.ID(),
		})
	}
	if snap.Status == "interrupted" && snap.InterruptedAt > 0 {
		items = append(items, ContextItem{
			Text:   fmt.Sprintf("上次在第 %d 步被中断，可从此处恢复", snap.InterruptedAt+1),
			Source: s.ID(),
		})
	}
	return items, nil
}
