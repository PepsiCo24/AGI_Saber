// cancel.go — UnifiedAgent 的并发执行 helper（薄包装）。
//
// 取消 / 任务状态的真正实现在 taskRuntime（task_runtime.go）；这里只做转发，
// 让上层调用代码保持 a.registerCancel / a.currentTask / a.setTask / a.Cancel
// 的一贯写法不变。
//
// goSafe 也留在这里：所有 fire-and-forget 异步任务（偏好提取 / 记忆挖掘 /
// 记忆合并 / Neo4j 写 / KG 索引）都通过它启动，捕获 panic 避免拖崩进程。
package chat

import (
	"context"
	"runtime/debug"

	"agi-assistant/internal/pkg/logger"
)

// registerCancel 转发到 taskRuntime
func (a *UnifiedAgent) registerCancel(cancel context.CancelFunc) func() {
	return a.runtime.registerCancel(cancel)
}

// currentTask 转发到 taskRuntime
func (a *UnifiedAgent) currentTask() *TaskState {
	return a.runtime.currentTask()
}

// setTask 转发到 taskRuntime
func (a *UnifiedAgent) setTask(t *TaskState) {
	a.runtime.setTask(t)
}

// Cancel 触发所有 in-flight 请求的 ctx 取消（用于 /api/chat/cancel）
func (a *UnifiedAgent) Cancel() {
	a.runtime.cancelAll()
}

// goSafe 启动一个带 panic recover 的后台 goroutine。
//
// agent 有大量 fire-and-forget 异步任务（偏好提取、记忆挖掘、记忆合并、
// Neo4j 异步写、KG 索引等）。任意一处 panic（比如 Neo4j 突然断连后某处空指针）
// 在裸 go func() 下会让整个进程崩溃，影响其他正常请求。
//
// 这个 helper 给所有异步任务统一兜底：
//   - 捕获 panic 并打印 stack trace（便于事后排查）
//   - name 标记任务来源，方便日志检索
//   - 不影响业务返回值（任务失败时静默丢弃）
func (a *UnifiedAgent) goSafe(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.L().Error("goroutine panic",
					"task", name,
					"panic", r,
					"stack", string(debug.Stack()),
				)
			}
		}()
		fn()
	}()
}
