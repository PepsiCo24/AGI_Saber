每轮推理前，根据当前 Mode（chat / tool / rag / react）选取一个
RuntimeContextSchema，并通过注册到 ContextAssembler 的 ContextSource
并发填充 6 类认知槽位：

	SlotProfile      用户画像（稳定身份）         ← Preference + LTM(category=identity|preference)
	SlotPlanner      任务规划状态                ← agent.currentTask 快照
	SlotTaskMem      当前任务步骤观察缓冲         ← TaskMemBuffer ring buffer
	SlotToolState    可用工具 + 近期调用记录      ← agent.toolsSnapshot + ToolStateTracker
	SlotConstraints  沙箱政策 / 硬性约束         ← sandbox.PolicySnapshot()
	SlotRecall       兜底语义召回                ← LongTerm 或 GraphMemory（取后者优先）

关键设计：
  - SlotFilter（Categories / Tags / TopK / TokenBudget）让"想要什么"和"召回什么"对得上
  - 单槽位 budget 自治，全局 budget 超限时按 slotPriority 倒序裁剪
  - 每个 ContextSource 独立可测试，按 SlotKind 注册到 SourceRegistry

学习入口建议：从 schema.go 看 4 个 Mode 的槽位编排，理解"为什么 react 必填 Constraints"，
再看 assembler.go 的 Assemble 流程，最后读各 source_*.go 了解各槽位的数据来源。
