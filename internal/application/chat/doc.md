Package chat 是聊天会话的应用服务层（用例编排）。

## 总体设计

UnifiedAgent 是核心调度入口，但它本身只持 `*config / *llm.Client / *rag.Engine
/ *sandbox.Sandbox / *knowledge.KGStore` 这几个外部组件，加上 5 个内部聚合：

	mem      *memoryStack    短期 / 长期 / 图增强 / 偏好
	repos    *repoBundle     6 个 repo 接口 + 事件总线 + infraStatus
	tools    *toolRegistry   工具 map + RWMutex
	runtime  *taskRuntime    当前任务 / snapshots / cancelFns + Mutex
	pctx     *promptCtx      Schema-driven 上下文装配（assembler / taskMem / toolTracker）

聚合让 agent struct 不再铺锁字段；调用点用 `a.mem.stm` / `a.repos.chat` /
`a.tools.snapshot()` 等访问，意图比裸字段更直白。

## 文件分组

### 核心调度
	agent.go            UnifiedAgent struct 定义 + Deps + New + 4 个启动期 helper
	                    （wireRAGCallbacks / bootstrapConcurrent /
	                     registerBuiltinTools / buildPromptCtx）
	process.go          Process / ProcessStream / ProcessContext / ProcessWithOptions
	                    + runOnce + prepare / dispatch / finalize
	router.go           路由决策（needRAG / needTool / needReAct）
	types.go            请求 / 响应 / 任务 / SSE 事件类型

### 五个聚合（每个一个文件）
	mem_stack.go        memoryStack：三层记忆 + 偏好
	repos.go            repoBundle：repo / events / infraStatus
	tool_registry.go    toolRegistry：工具 map 并发安全
	task_runtime.go     taskRuntime：任务状态 + cancel 令牌
	prompt_ctx.go       promptCtx：assembler + taskMem + toolTracker

### Mode handler（流式 / 非流式合并为单一函数，靠 onEvent 是否为 nil 区分）
	mode_chat.go        默认对话（在 process.go::dispatch 的 default 分支）
	mode_tool.go        runTool：单工具调用
	mode_react.go       runReAct：DAG 任务图调度 + Generator LLM 合成
	planner.go          ReAct Planner LLM + 关键词规则降级
	runtime.go          GraphRuntime：DAG 并行 / 竞速调度引擎

### 工具与辅助
	llm_helper.go       chatLLM（按 onEvent 自动选 stream / non-stream）+ emit 事件辅助
	context_builder.go  buildContextPrefix / buildHistoryMessages / recentHistoryForRAG
	memory_writer.go    extractMemoryFromReply / classifyMemoryContent / syncConsolidationToDB
	cancel.go           UnifiedAgent → taskRuntime 的薄转发 + goSafe panic 兜底
	accessor.go         RAG / Tools / Memory 等字段 getter + RegisterTool + saveSnapshot + fillParamsFromPreference
	restore.go          启动期从 PG 恢复偏好 / LTM / 聊天 / RAG chunks + initKnowledgeGraph
	init_sandbox.go     沙箱初始化 + exec_command 工具注册
	status.go           系统状态 / 配置摘要（HTTP /api/status 用）

## 流式 / 非流式统一执行流

	Process / ProcessContext / ProcessWithOptions  →  runOnce(ctx, query, opts, nil)
	ProcessStream                                  →  runOnce(ctx, query, opts, onEvent)

runOnce 内部：

	prepare（STM 写入 + 偏好提取 + 路由 + 上下文装配 + 历史构建）
	  ↓
	dispatch（按 mode 调单一 mode handler，onEvent 透传到底层 LLM 调用）
	  ↓
	finalize（assistant STM 写入 + 异步记忆抽取 + 异步合并 + 事件发布 + 计数）

mode handler 不再有 sync / stream 双胞胎——onEvent 为 nil 时所有 emit(...)
变成 no-op，chatLLM(...) 走 ChatContext；非 nil 时推送 SSE 事件、走 ChatStreamContext。

## 学习入口建议

 1. agent.go 看 UnifiedAgent 持有哪 5 个聚合 + New 的四阶段启动序列
 2. process.go 跟着 runOnce → prepare → dispatch → finalize 走完一轮对话
 3. 按需挑 mode_react.go / mode_tool.go 深入某种模式
 4. mem_stack.go / repos.go / tool_registry.go / task_runtime.go / prompt_ctx.go
    是聚合定义，每个 50~90 行，可以分别独立阅读
