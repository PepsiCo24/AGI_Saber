// llm_helper.go — chat 包内统一的 LLM 调用入口与流式事件辅助。
//
// 设计动机：
//   - 流式 / 非流式分发在 runOnce 层面统一了，但底层 LLM 调用仍分两套
//     （ChatContext / ChatStreamContext）。把"按 onEvent 是否为 nil 决定走哪条"
//     的开关收敛到这一处，避免每个 mode handler 都自己判断。
//   - emit：让 mode handler 可以无条件调用 onEvent(...)，不必每个调用点都
//     先 if onEvent != nil。
//
// 约定：onEvent == nil 表示 caller 不要流式（同步入口）。chatLLM 据此选择
// ChatContext；emit 只在 onEvent 非 nil 时才转发事件。
package chat

import (
	"context"

	"agi-assistant/internal/infrastructure/llm"
)

// emit 安全地推送一条 SSE 事件——若 onEvent 为 nil 直接静默丢弃。
// 让 mode handler 无需在每个事件点判空。
func emit(onEvent func(StreamEvent), eventType string, data interface{}) {
	if onEvent == nil {
		return
	}
	onEvent(NewStreamEvent(eventType, data))
}

// chatLLM 根据 onEvent 是否为 nil 自动选 stream / non-stream LLM 调用。
//
//   - onEvent == nil：ChatContext，一次性返回完整回复；
//   - onEvent != nil：ChatStreamContext，每个 token 通过 "token" 事件推送，
//     最终仍返回聚合后的完整回复。
//
// 让 mode_chat / mode_tool / mode_react / RAG generator 等所有 LLM 调用点
// 统一为单条代码路径，不再需要 sync/stream 双胞胎函数。
func (a *UnifiedAgent) chatLLM(ctx context.Context, system string, msgs []llm.Message, onEvent func(StreamEvent)) string {
	if onEvent == nil {
		return a.llm.ChatContext(ctx, system, msgs)
	}
	return a.llm.ChatStreamContext(ctx, system, msgs, func(token string) {
		onEvent(NewStreamEvent("token", map[string]string{"content": token}))
	})
}
