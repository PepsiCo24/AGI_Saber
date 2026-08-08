package promptctx

import (
	"fmt"
	"strings"
)

// RuntimeContext 是一次装配的全部结果，可通过 Render 得到 System Prompt 前缀。
type RuntimeContext struct {
	Schema RuntimeContextSchema
	Filled []FilledSlot
	Trace  []string // debug：装配过程中的决策记录
}

// SlotByKind 取出特定槽位（不存在返回 nil）
func (rc *RuntimeContext) SlotByKind(kind SlotKind) *FilledSlot {
	if rc == nil {
		return nil
	}
	for i := range rc.Filled {
		if rc.Filled[i].Kind == kind {
			return &rc.Filled[i]
		}
	}
	return nil
}

// Render 将所有非空槽位按 Schema 顺序渲染为 zh-CN 提示前缀
func (rc *RuntimeContext) Render() string {
	if rc == nil || len(rc.Filled) == 0 {
		return ""
	}
	var sections []string
	for _, fs := range rc.Filled {
		if fs.Skipped || len(fs.Items) == 0 {
			continue
		}
		sections = append(sections, renderSlot(fs))
	}
	return strings.Join(sections, "\n\n")
}

// renderSlot 按 SlotKind 选模板渲染单个槽位
func renderSlot(fs FilledSlot) string {
	title := slotTitle(fs.Kind)
	var lines []string
	for _, item := range fs.Items {
		if strings.TrimSpace(item.Text) == "" {
			continue
		}
		lines = append(lines, "- "+item.Text)
	}
	if len(lines) == 0 {
		return ""
	}
	return fmt.Sprintf("【%s】\n%s", title, strings.Join(lines, "\n"))
}

// slotTitle 返回每类槽位的中文标题
func slotTitle(kind SlotKind) string {
	switch kind {
	case SlotProfile:
		return "用户画像"
	case SlotPlanner:
		return "任务规划"
	case SlotTaskMem:
		return "任务记忆"
	case SlotToolState:
		return "可用工具"
	case SlotConstraints:
		return "硬性约束"
	case SlotRecall:
		return "相关回忆"
	}
	return string(kind)
}
