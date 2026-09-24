# Python 分支对齐主分支实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**目标：** 将 Python 分支的 `final/` 实现对齐到最新 Go 主分支的 RAG、DAG Runtime、Prompt Context、配置和持久化能力。

**架构：** 保留 Python 分支现有 `final/` 项目根和模块风格，按能力等价翻译 Go 主分支。RAG 增强留在 `final/internal/rag/`，图数据结构放到 `final/internal/graph/`，执行 runtime 放到 `final/internal/agent/`，Prompt Context 通过 `UnifiedAgent` 统一接入。

**技术栈：** Python 3、pytest、threading、现有 FastAPI/infra/LLM 工具层、Milvus、Elasticsearch、Neo4j、PostgreSQL。

---

## 文件结构

- 新建 `final/tests/`：Python 单元测试入口。
- 新建 `final/internal/rag/splitter.py`：递归父子块切分。
- 新建 `final/internal/rag/rewriter.py`：history-aware multi-query rewrite。
- 新建 `final/internal/rag/reranker.py`：LLM listwise rerank。
- 修改 `final/internal/rag/hybrid.py`：`HybridResult.parent`、`set_reranker`、`search_multi`、父块加载。
- 修改 `final/internal/rag/rag.py`：父子块 ingest、`query_with_history`、small-to-big 合成、删除/恢复。
- 修改 `final/internal/infra/infra.py`：`parent_content` schema、父块感知 chunk save/load/delete。
- 修改 `final/config/config.py` 和 `final/config/config.yaml`：RAG rewrite/rerank、graph runtime 配置。
- 新建 `final/internal/graph/task_graph.py`：TaskGraph、Node、拓扑层级、竞速组。
- 新建 `final/internal/agent/graph_runtime.py`：图执行 runtime。
- 修改 `final/internal/agent/planner.py`：输出 graph node plan。
- 修改 `final/internal/agent/agent.py`：接入 promptctx、RAG history、DAG ReAct runtime。
- 视需要小改 `final/internal/promptctx/*`：只补适配缺口，不重写已有包。

## Task 1: 建立测试入口和 RAG 基础增强

**文件：**
- Create: `final/tests/test_rag_alignment.py`
- Create: `final/internal/rag/splitter.py`
- Create: `final/internal/rag/rewriter.py`
- Create: `final/internal/rag/reranker.py`
- Modify: `final/internal/rag/__init__.py`

- [ ] **Step 1: 写 RAG 红灯测试**

覆盖：
- parent splitter 生成大块，child splitter 生成小块。
- rewrite JSON 解析成功时返回改写 query；解析失败时回退原 query。
- rerank JSON 解析成功时按 LLM 分数排序；解析失败时保持原顺序。

- [ ] **Step 2: 运行红灯测试**

Run: `cd final && python -m pytest tests/test_rag_alignment.py -v`

Expected: FAIL，原因是 `splitter.py`、`rewriter.py`、`reranker.py` 尚不存在或接口缺失。

- [ ] **Step 3: 实现最小 RAG 基础模块**

实现：
- `RecursiveSplitter.split(text) -> list[Chunk]`
- `HistoryMessage`
- `LLMRewriter.rewrite(query, history)`
- `LLMReranker.rerank(query, results, top_k)`

- [ ] **Step 4: 运行绿灯测试**

Run: `cd final && python -m pytest tests/test_rag_alignment.py -v`

Expected: PASS。

## Task 2: 对齐 RAG SearchMulti、small-to-big 和持久化

**文件：**
- Modify: `final/tests/test_rag_alignment.py`
- Modify: `final/internal/rag/hybrid.py`
- Modify: `final/internal/rag/rag.py`
- Modify: `final/internal/infra/infra.py`

- [ ] **Step 1: 写红灯测试**

覆盖：
- `HybridStore.search_multi` 对多 query 结果做跨 query RRF 合并。
- `HybridResult.parent` 优先进入 LLM 上下文。
- `Engine.ingest` 保存 child chunk 时携带 parent content。
- `Engine.query_with_history` 调 rewriter、search_multi、reranker 并返回父块上下文。

- [ ] **Step 2: 运行红灯测试**

Run: `cd final && python -m pytest tests/test_rag_alignment.py -v`

Expected: FAIL，原因是 search_multi、parent content 或 query_with_history 未实现。

- [ ] **Step 3: 实现 RAG 链路**

实现：
- `HybridStore.set_reranker`
- `HybridStore.search_multi`
- `HybridStore._finalize`
- `HybridResult.parent`
- `Engine.set_rewriter`
- `Engine.set_reranker`
- `Engine.query_with_history`
- `Infrastructure.save_rag_chunk_with_parent`
- `Infrastructure.load_rag_chunks_by_ids` 返回 `parent_content`
- PG schema 增加 `parent_content`

- [ ] **Step 4: 运行绿灯测试**

Run: `cd final && python -m pytest tests/test_rag_alignment.py -v`

Expected: PASS。

## Task 3: 对齐配置项

**文件：**
- Create: `final/tests/test_config_alignment.py`
- Modify: `final/config/config.py`
- Modify: `final/config/config.yaml`

- [ ] **Step 1: 写配置红灯测试**

覆盖：
- 默认 `rag_rewrite_enabled`、`rag_rewrite_num_queries`、`rag_rerank_enabled`、`rag_rerank_preview_len`。
- 默认 `graph_max_parallel`、`graph_race_timeout_ms`、`graph_enable_racing`。
- YAML 覆盖能正确解析。

- [ ] **Step 2: 运行红灯测试**

Run: `cd final && python -m pytest tests/test_config_alignment.py -v`

Expected: FAIL，原因是字段尚不存在。

- [ ] **Step 3: 实现配置解析**

在 `APIConfig` 增加字段并从 YAML 的 `rag.rewrite`、`rag.rerank`、`graph_runtime` 读取。

- [ ] **Step 4: 运行绿灯测试**

Run: `cd final && python -m pytest tests/test_config_alignment.py -v`

Expected: PASS。

## Task 4: 实现 TaskGraph

**文件：**
- Create: `final/tests/test_task_graph.py`
- Create: `final/internal/graph/task_graph.py`
- Modify: `final/internal/graph/__init__.py`

- [ ] **Step 1: 写 TaskGraph 红灯测试**

覆盖：
- 拓扑层级排序。
- 检测缺失依赖和环。
- race group 分组。
- 节点状态、结果、错误、重试次数写入。

- [ ] **Step 2: 运行红灯测试**

Run: `cd final && python -m pytest tests/test_task_graph.py -v`

Expected: FAIL。

- [ ] **Step 3: 实现 TaskGraph**

按 Go 主分支 `internal/domain/graph/graph.go` 行为翻译。

- [ ] **Step 4: 运行绿灯测试**

Run: `cd final && python -m pytest tests/test_task_graph.py -v`

Expected: PASS。

## Task 5: 实现 GraphRuntime 和 Planner 图输出

**文件：**
- Create: `final/tests/test_graph_runtime.py`
- Create: `final/internal/agent/graph_runtime.py`
- Modify: `final/internal/agent/planner.py`

- [ ] **Step 1: 写 GraphRuntime 红灯测试**

覆盖：
- 无依赖节点并行执行。
- 有依赖节点按层执行。
- race group 首个成功节点胜出。
- 失败节点记录 error。
- 重试次数生效。
- token cancel 时 pending/running 节点变 cancelled。

- [ ] **Step 2: 运行红灯测试**

Run: `cd final && python -m pytest tests/test_graph_runtime.py -v`

Expected: FAIL。

- [ ] **Step 3: 实现 GraphRuntime**

实现 Python 线程版 runtime，并让 planner 返回 graph nodes。

- [ ] **Step 4: 运行绿灯测试**

Run: `cd final && python -m pytest tests/test_graph_runtime.py -v`

Expected: PASS。

## Task 6: 接入 UnifiedAgent 和 Prompt Context

**文件：**
- Create: `final/tests/test_agent_promptctx_alignment.py`
- Modify: `final/internal/agent/agent.py`
- Modify: `final/internal/agent/status.py` if needed
- Modify: `final/internal/promptctx/*` if adapter gaps appear

- [ ] **Step 1: 写 Agent 接入红灯测试**

覆盖：
- `UnifiedAgent` 初始化后存在 prompt context bundle。
- chat/tool/react/rag 构造 prompt 时使用 `_build_context_prefix(query, mode)`。
- ReAct 路径调用 Planner -> TaskGraph -> GraphRuntime。
- RAG 路径调用 `query_with_history` 并传入历史。

- [ ] **Step 2: 运行红灯测试**

Run: `cd final && python -m pytest tests/test_agent_promptctx_alignment.py -v`

Expected: FAIL。

- [ ] **Step 3: 实现 Agent 接入**

替换串行 ReAct loop 主路径，保留旧方法作为降级或删除未使用路径；接入 promptctx source registry。

- [ ] **Step 4: 运行绿灯测试**

Run: `cd final && python -m pytest tests/test_agent_promptctx_alignment.py -v`

Expected: PASS。

## Task 7: 全量验证和清理

**文件：**
- Modify as needed: docs and README only if behavior说明必须同步。

- [ ] **Step 1: 跑全部测试**

Run: `cd final && python -m pytest -v`

Expected: PASS。

- [ ] **Step 2: 跑基础 import/startup 检查**

Run: `cd final && python -m py_compile main.py internal/rag/rag.py internal/rag/hybrid.py internal/agent/agent.py internal/agent/planner.py internal/agent/graph_runtime.py internal/graph/task_graph.py`

Expected: exit 0。

- [ ] **Step 3: 查看 git diff**

Run: `git diff --stat`

Expected: 只包含本次对齐相关文件。

- [ ] **Step 4: 提交实现**

Run: `git add final docs/superpowers/plans/2026-06-14-python-main-alignment.md && git commit -m "feat: align python branch with main runtime features"`

