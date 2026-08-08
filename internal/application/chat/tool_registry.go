// tool_registry.go — 工具集合的并发安全包装。
//
// UnifiedAgent 的工具 map 会被多路并发访问：
//   - RegisterTool / RegisterMCPTool（写）
//   - ReAct Planner、Decide、Stream/非流式 dispatch（读）
//
// Go 原生 map 并发读写直接 panic，必须串行化。这里把 map + RWMutex 打包成
// 一个独立类型，让 UnifiedAgent 不再持有锁字段；调用点通过 a.tools.xxx
// 访问，意图比 a.toolsMu.RLock() / a.tools[...] 更直白。
package chat

import (
	"sync"

	"agi-assistant/internal/domain/tool"
)

// toolRegistry 是 UnifiedAgent 的工具集合（并发安全）
type toolRegistry struct {
	mu    sync.RWMutex
	tools map[string]tool.Tool
}

// newToolRegistry 创建工具注册表，带初始默认工具集
func newToolRegistry(initial map[string]tool.Tool) *toolRegistry {
	if initial == nil {
		initial = make(map[string]tool.Tool)
	}
	return &toolRegistry{tools: initial}
}

// register 写入或覆盖一个工具
func (r *toolRegistry) register(t tool.Tool) {
	r.mu.Lock()
	r.tools[t.Name] = t
	r.mu.Unlock()
}

// snapshot 返回工具 map 的浅拷贝（Tool 内部字段不可变，浅拷贝足够）。
// 调用方拿到快照后可无锁遍历，且不会被并发的 register 干扰。
func (r *toolRegistry) snapshot() map[string]tool.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cp := make(map[string]tool.Tool, len(r.tools))
	for k, v := range r.tools {
		cp[k] = v
	}
	return cp
}

// filter 按名字白名单返回工具 map 的子集
func (r *toolRegistry) filter(names []string) map[string]tool.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]tool.Tool, len(names))
	for _, name := range names {
		if t, ok := r.tools[name]; ok {
			out[name] = t
		}
	}
	return out
}
