// prompt_ctx.go — Schema-driven prompt 上下文装配的内部封装。
//
// 把 assembler / taskMem / toolTracker 三个 promptctx 子组件聚合到一处，
// 避免它们以独立字段散落在 UnifiedAgent 上、调用点都要 nil 检查。
//
// 设计：
//   - 这三个组件只能在 New 末尾构造（依赖 graphMem / pref / ltm 已就绪），
//     所以 promptCtx 同样是延后创建，UnifiedAgent.pctx 在初始化阶段会是 nil。
//   - 所有 nil 守卫（assembler == nil / taskMem != nil / ...）集中在 promptCtx
//     的方法里实现，调用方不必再判空。
package chat

import (
	"context"

	"agi-assistant/internal/domain/promptctx"
)

// promptCtx 聚合 Schema-driven prompt 装配相关组件
type promptCtx struct {
	assembler   *promptctx.ContextAssembler
	taskMem     *promptctx.TaskMemBuffer
	toolTracker *promptctx.ToolStateTracker
}

// assemble 调 ContextAssembler 装配本次推理的 RuntimeContext，并返回渲染后的字符串。
// 装配器未就绪时返回空串（启动期未完成时调用方仍能正常运行）。
func (p *promptCtx) assemble(ctx context.Context, q promptctx.Query) string {
	if p == nil || p.assembler == nil {
		return ""
	}
	rc := p.assembler.Assemble(ctx, q)
	return rc.Render()
}

// resetTaskMem 重置 ReAct 任务的临时记忆缓冲（新任务开始时调用）
func (p *promptCtx) resetTaskMem() {
	if p == nil || p.taskMem == nil {
		return
	}
	p.taskMem.Reset()
}

// pushTaskMem 把一条 StepObservation 写入任务记忆（每个工具节点执行后由 GraphRuntime 调用）
func (p *promptCtx) pushTaskMem(obs promptctx.StepObservation) {
	if p == nil || p.taskMem == nil {
		return
	}
	p.taskMem.Push(obs)
}

// recordToolCall 记录一次工具调用的成功/失败到 ToolStateTracker
func (p *promptCtx) recordToolCall(trace promptctx.ToolCallTrace) {
	if p == nil || p.toolTracker == nil {
		return
	}
	p.toolTracker.Record(trace)
}
