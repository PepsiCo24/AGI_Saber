# Python 分支对齐主分支设计说明

## 目标

将 `python` 分支对齐到最新 `origin/main` Go 版本的核心能力，同时保留 Python 分支现有可运行的 `final/` 项目结构。

这里的“对齐”指行为和运行能力等价：RAG 检索链路、图式 ReAct 执行、Prompt Context 装配、记忆系统接入、配置项和持久化能力应与主分支一致。它不要求照搬 Go 包名，也不要求把 Python 文件移动成 Go 主分支完全相同的目录布局。

## 当前状态

Python 分支已经完成了较早 `final/` 版本的大部分 Python 化迁移：

- `final/internal/rag/hybrid.py` 已支持 Milvus、Elasticsearch、Neo4j 和 RRF 融合。
- `final/internal/memory/graph_memory.py` 已支持 Neo4j 图记忆边，例如 `FOLLOWS` 和 `SIMILAR_TO`。
- `final/internal/promptctx/` 已有 Python 版 Prompt Context 装配包，但还没有接入 `UnifiedAgent` 主流程。
- `final/internal/agent/agent.py` 仍然使用串行 ReAct 循环，没有对齐 Go 主分支的 DAG Runtime。

最新 Go 主分支新增或强化了这些能力，Python 分支还没有完整实现或接入：

- RAG 查询改写、多 query 检索、rerank 精排、递归父子块切分，以及 small-to-big 父块上下文回填。
- 图式 Runtime：`TaskGraph`、依赖感知拓扑调度、竞速组、取消、快照、任务记忆和工具状态追踪。
- Schema-driven Prompt Context Assembly，并且 chat、tool、ReAct、RAG 都使用统一上下文装配。
- 新配置项和持久化 schema，尤其是 `rag_chunks.parent_content`。

## 范围

### 本次包含

1. 将 Go 主分支最新 RAG 行为翻译到 Python：
   - 递归 splitter 和父子块切分。
   - `Rewriter` 与 `LLMRewriter`。
   - `Reranker` 与 `LLMReranker`。
   - `HybridStore.search_multi`。
   - 检索结果携带父块内容，并在答案合成时使用 small-to-big 上下文。
   - `Engine.query_with_history`。
   - 在现有基础设施支持范围内对齐 RAG 文档删除和恢复语义。

2. 将 Go 主分支图式 Runtime 行为翻译到 Python：
   - `TaskGraph`、`Node`、节点状态、依赖校验、拓扑层级、竞速组工具方法。
   - Planner 输出 `id`、`tool`、`params`、`reason`、`depends_on`、`race_group`。
   - `GraphRuntime` 支持按层并行执行、竞速组、重试、取消、快照和结果聚合。
   - Agent 的 ReAct 路径从串行循环升级为 Planner -> TaskGraph -> GraphRuntime。

3. 将 Python `promptctx` 接入 `UnifiedAgent`：
   - 在 agent 级别构造 prompt context bundle，包含 assembler、task memory、tool tracker。
   - 注册 profile、planner state、task memory、tool state、sandbox constraints、recall 等 source。
   - chat、tool、ReAct planner、ReAct finalization、RAG answer generation 都使用统一装配出的上下文。

4. 对齐配置和持久化：
   - 增加 Python 配置字段：RAG rewrite/rerank、graph runtime。
   - 从 `final/config/config.yaml` 解析对应 YAML 配置。
   - 确保 PostgreSQL schema 支持 `rag_chunks.parent_content`。
   - 增加基础设施或 repo 辅助方法，用于保存、加载、删除和恢复带父块内容的 RAG chunks。

5. 增加聚焦测试：
   - RAG splitter、rewrite fallback、rerank 解析/fallback、search multi merge、small-to-big assembly。
   - TaskGraph 校验、拓扑层级、竞速分组和 runtime 执行。
   - Prompt Context 装配接入和 source 行为。
   - 新配置项的默认值和 YAML 覆盖解析。

### 本次不包含

- 将 Python 分支改造成 Go 主分支一模一样的目录结构。
- 实现 RAGAS、Golden Queries 或 benchmark 指标相关的简历描述，除非主分支已有可执行实现可翻译。当前对比分支时没有发现主分支存在可运行的评测模块。
- 改造前端交互，除非后端返回结构变化需要做最小兼容。
- 新增现有集成之外的外部服务。继续沿用 Milvus、Elasticsearch、Neo4j、PostgreSQL、Kafka、LLM、Tavily 等既有集成。

## 架构设计

Python 分支继续以 `final/` 作为项目根目录，用职责清晰的 Python 模块映射 Go 主分支能力。

### RAG

RAG 仍放在 `final/internal/rag/` 下：

- `splitter.py` 提供递归父块和子块切分。
- `rewriter.py` 提供带历史感知的多 query 改写，使用严格 JSON 解析，失败时回退原始 query。
- `reranker.py` 提供 listwise rerank，使用严格 JSON 解析，失败时回退 RRF 原始顺序。
- `hybrid.py` 扩展 `search_multi`、reranker 注入、`HybridResult.parent` 和父块内容加载。
- `rag.py` 负责父子块 ingest、携带 PG ID 的 KG 索引、query rewrite、多 query retrieval、rerank、small-to-big 上下文选择和答案生成。

所有 LLM 辅助层都必须可降级。rewrite 或 rerank 解析失败时，请求继续使用原始 query 或原始 RRF 排序，不阻塞主链路。

### 图式 Runtime

图式 Runtime 按职责拆分：

- `final/internal/graph/task_graph.py` 负责图数据结构、节点状态、依赖校验、拓扑排序和竞速组辅助方法。
- `final/internal/agent/graph_runtime.py` 负责执行，因为它依赖 agent、tools、snapshots 和 prompt context。
- `final/internal/agent/planner.py` 改为返回 graph nodes，而不是只返回线性 `PlanItem`。
- `final/internal/agent/agent.py` 将串行 ReAct loop 替换为 Planner -> TaskGraph -> GraphRuntime。

当前工具接口是同步的，因此 Runtime 使用 Python 线程执行同层独立节点。竞速组采用 first successful result wins。取消能力复用现有 cancel token registry。

### Prompt Context

现有 `final/internal/promptctx/` 包将成为 agent 主流程的一部分。

`UnifiedAgent` 启动时构造 prompt context bundle：

- `ContextAssembler`
- `TaskMemBuffer`
- `ToolStateTracker`
- `SourceRegistry`

Agent 通过一个集中方法，例如 `_build_context_prefix(query, mode)`，生成不同模式所需上下文，避免 chat、tool、RAG、ReAct 各自拼 prompt。

GraphRuntime 在每个工具节点成功或失败后，把观察结果写入 task memory，并记录 tool call state。

### 持久化

Python 基础设施层需要提供父块感知的 RAG chunk 操作：

- 保存子块时可同时保存父块内容。
- 按 ID 批量加载 chunk 时返回父块内容。
- 全量加载 chunk 时返回父块内容。
- 按 doc hash 删除文档对应 chunks。

PostgreSQL 初始化应使用 `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` 增加 `parent_content TEXT`，与 Go 主分支保持向后兼容。旧数据没有父块内容时，检索链路回退使用子块内容。

## 数据流

### RAG Ingest

1. 用户上传或摄入文档。
2. 父块 splitter 创建较大的上下文块。
3. 子块 splitter 在每个父块内部创建更小的检索块。
4. 子块写入 PostgreSQL，同时保存对应父块内容。
5. 在可用时，将子块索引到 Elasticsearch 和 Milvus。
6. KG 索引以 best-effort 方式执行，并携带真实 PG ID，让图检索结果能够参与 RRF 融合。

### RAG Query

1. Agent 获取最近聊天历史。
2. Rewriter 将当前问题改写为一条或多条检索 query。
3. HybridStore 对每条 query 检索，并使用跨 query RRF 合并候选。
4. 可选 reranker 对候选池精排，并截断到 `top_k`。
5. 命中结果有父块内容时，用父块替代子块作为 LLM 上下文。
6. LLM 基于 small-to-big 上下文生成答案。

### ReAct Query

1. Agent 为规划阶段装配上下文。
2. Planner LLM 输出带依赖和竞速组的图节点。
3. TaskGraph 校验计划；如果依赖无效，则降级为无依赖的并行图。
4. GraphRuntime 按拓扑层级执行。
5. 同一竞速组节点并发运行，首个成功节点胜出。
6. 工具观察结果更新 task memory 和 tool state。
7. 最终答案基于图执行观察结果和统一上下文生成。

## 错误处理

- RAG rewrite 和 rerank 不允许阻断主回答链路。LLM 失败或解析失败时，分别回退原始 query 和原始 RRF 排序。
- Milvus、Elasticsearch、Neo4j 仍为可选后端。不可用的检索路径跳过，可用路径继续工作。
- Graph planning 失败、LLM 不可用或 JSON 无法解析时，回退规则规划。
- 图依赖无效时，降级为无依赖图，尽量保留可执行工具调用。
- Runtime 节点失败会记录到 node result 和 prompt context；其他无依赖节点继续执行，除非用户触发取消。
- 持久化辅助方法延续现有 best-effort 风格，对可降级基础设施错误做保护。

## 测试策略

所有行为组先写测试再实现。

RAG 测试使用 fake infrastructure 和 fake LLM callback，不依赖真实 Milvus、Elasticsearch、Neo4j 或 PostgreSQL。覆盖父子块 ingest、search multi 合并、rewrite fallback、rerank 排序和父块上下文使用。

图式 Runtime 测试使用确定性 fake tools，包括延迟和失败场景。覆盖拓扑执行顺序、竞速胜出、取消状态传播、重试行为，以及 task memory/tool tracking 调用。

Prompt Context 测试使用 fake sources 和 agent 依赖，验证 `UnifiedAgent` 能按模式装配上下文，并验证 GraphRuntime 会记录观察结果。

配置测试使用临时 YAML 文件，断言 rewrite、rerank、graph runtime 新字段的默认值和覆盖解析。

## 验收标准

- Python 分支具备与主分支等价的 RAG rewrite、rerank、SearchMulti、small-to-big 能力。
- Python ReAct 流程使用支持依赖和竞速组的 DAG Runtime。
- Python `promptctx` 包接入 agent 主流程和 runtime 观察记录。
- 新配置字段能从 YAML 解析，并且默认值合理。
- RAG chunk 持久化支持父块内容，并兼容旧数据。
- 聚焦 Python 测试通过。
- `final/main.py` 和关键模块的基础 import/startup 检查通过。

