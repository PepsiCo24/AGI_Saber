// Package handler 暴露 HTTP API（interfaces 层）。
//
// 路由表：
//
//	POST /api/chat            统一对话入口（同步）
//	POST /api/chat/stream     SSE 流式对话
//	POST /api/chat/cancel     取消正在执行的对话
//	POST /api/upload          上传文档到 RAG 知识库
//	POST /api/docs/delete     按 docHash 删除文档
//	POST /api/tools/mcp       动态注册 MCP 工具
//	GET  /api/memory          查看三层记忆状态
//	GET  /api/tools           列出所有可用工具
//	GET  /api/snapshots       列出任务执行快照摘要
//	GET  /api/status          系统状态与配置摘要
//
// handler 不持有业务逻辑，只做：参数解析 → 调用 application/chat 的方法 → 序列化响应。
package handler
