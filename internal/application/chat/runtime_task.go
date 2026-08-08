// task_runtime.go — per-request 共享状态聚合：当前任务、快照列表、in-flight cancel 函数。
//
// 设计动机：旧实现把这三个字段放在 UnifiedAgent 上，多请求并发时数据竞争 +
// Cancel() 因 cancelFn 互相覆盖只能取消最近一次请求；并且 mu 只服务这块状态，
// 跟工具锁、记忆锁混在一起读起来很乱。这里聚合到独立类型：
//
//   - mu             串行化 task / snapshots / cancelFns 的全部读写
//   - cancelFns map  每个 in-flight 请求一个 token，Cancel() 触发全部
//   - task           当前正在执行的 ReAct 任务（可能为 nil）
//   - snapshots      ReAct 各步骤快照（用于 /api/snapshots + 中断恢复）
package chat

import (
	"context"
	"sync"
)

// taskRuntime 聚合 ReAct 任务状态和取消令牌
type taskRuntime struct {
	mu sync.Mutex

	task         *TaskState
	snapshots    []Snapshot
	cancelFns    map[int64]context.CancelFunc
	nextCancelID int64
}

// newTaskRuntime 创建空的运行时状态容器
func newTaskRuntime() *taskRuntime {
	return &taskRuntime{cancelFns: make(map[int64]context.CancelFunc)}
}

// registerCancel 把本次请求的 cancel 函数挂到运行时；返回反注册函数。
// 多请求并发时每个请求拿到独立 id；CancelAll() 触发全部 in-flight 中断。
func (r *taskRuntime) registerCancel(cancel context.CancelFunc) func() {
	r.mu.Lock()
	r.nextCancelID++
	id := r.nextCancelID
	r.cancelFns[id] = cancel
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.cancelFns, id)
		r.mu.Unlock()
		cancel() // 幂等：context.CancelFunc 自带 once 保护
	}
}

// cancelAll 触发所有 in-flight 请求的 ctx 取消
func (r *taskRuntime) cancelAll() {
	r.mu.Lock()
	fns := make([]context.CancelFunc, 0, len(r.cancelFns))
	for _, fn := range r.cancelFns {
		fns = append(fns, fn)
	}
	r.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// currentTask 持锁读当前 task 引用（可能为 nil）
func (r *taskRuntime) currentTask() *TaskState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.task
}

// setTask 持锁设置当前 task，并清空 snapshots（新任务开始）
func (r *taskRuntime) setTask(t *TaskState) {
	r.mu.Lock()
	r.task = t
	r.snapshots = nil
	r.mu.Unlock()
}

// appendSnapshot 持锁追加一条快照
func (r *taskRuntime) appendSnapshot(s Snapshot) {
	r.mu.Lock()
	r.snapshots = append(r.snapshots, s)
	r.mu.Unlock()
}

// snapshotList 返回 snapshots 的拷贝（持锁）
func (r *taskRuntime) snapshotList() []Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]Snapshot, len(r.snapshots))
	copy(cp, r.snapshots)
	return cp
}
