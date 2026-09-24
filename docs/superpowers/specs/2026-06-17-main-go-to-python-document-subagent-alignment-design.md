# 主分支 Go 能力翻译到 Python 分支设计

## 目标

把最新 `origin/main` 中 Go 版新增能力翻译到当前 Python 分支的 `final/` 目录下，并尽量保持行为和 API 契约 1:1 对齐。这不是 mock、接口空壳或兼容适配层。Python 实现必须走真实持久化、真实文档解析、真实 RAG 入库、真实工具执行和真实子代理编排。

Go 版源代码规范如下：

- `origin/main:internal/domain/document/library.go`
- `origin/main:internal/domain/document/parser.go`
- `origin/main:internal/infrastructure/persistence/documentrepo/documentrepo.go`
- `origin/main:internal/application/chat/documents.go`
- `origin/main:internal/application/chat/tool_documents.go`
- `origin/main:internal/application/chat/subagents.go`
- `origin/main:internal/interfaces/http/handler/handler.go`
- `origin/main:frontend/index.html`

## 范围

在 Python 版中实现主分支最新能力：

- 文档领域模型和带版本的本地文档库。
- PDF/文本上传解析，返回 parser、页数、文本字符数、是否需要 OCR 等元信息。
- PostgreSQL 文档库仓储。
- 文档库 HTTP API。
- Agent 文档方法。
- 文档工具：`write_document`、`list_documents`、`read_document`、`ingest_document`。
- 子代理：`research_agent`、`writer_agent`、`review_agent`、`doc_agent`。
- 前端文档库 UI 和上传响应展示。
- 覆盖真实实现路径的测试。

保留当前 Python 运行结构和已调通集成：

- `final/` 下的 FastAPI 应用。
- 现有 RAG、PostgreSQL、Elasticsearch、Neo4j、LLM 和前端。
- 尽量保持已有 API 兼容。

## 不做的事

- 不用 Go 服务替换 Python 分支。
- 不给缺失基础设施做假成功路径。
- 不新增第二套服务或代理层。
- 不重写无关的 memory、sandbox、graph runtime 或 RAG 内部逻辑，除非新文档/子代理契约必须改。
- 不提交本地密钥配置文件。

## 架构

### 文档领域模型

新增 Python 文档领域模块，建议位置为 `final/internal/document/`，语义对齐 Go 的 domain 包。

核心类型：

- `Document`
- `DocumentVersion`
- `WriteRequest`
- `WriteResult`

常量：

- `DocumentStatusActive = "active"`
- `DocumentSourceAgent = "agent_generated"`
- `DocumentSourceUpload = "user_upload"`

辅助函数：

- `normalize_write_request(req)`
- `new_id(prefix)`

行为必须对齐 Go：

- 空 `doc_type` 默认 `note`。
- 空 `source` 默认 `agent_generated`。
- 空 `created_by` 默认 `agent`。
- 空 metadata 默认 `{}`。
- 新 ID 使用 `doc_`、`ver_` 这类稳定前缀。

### 文档解析

新增真实解析模块，对齐 `internal/domain/document/parser.go`。

`parse_bytes(filename, content_type, data)` 返回：

- `filename`
- `content_type`
- `parser`
- `content`
- `pages`
- `text_chars`
- `needs_ocr`

必需行为：

- 通过 content type 或 `.pdf` 后缀识别 PDF。
- 归一化抽取文本。
- 修复英文行尾连字符换行。
- PDF 有页数但抽取文本过少时返回 `needs_ocr=true`。
- 文档为空或 PDF 无可抽取文本时返回明确解析错误。

Python 解析策略：

- 优先使用运行时里的 `pdfplumber`。
- 回退到 `PyPDF2`。
- 如系统安装了 `pdftotext`，可再回退一次。
- 不能对空文本宣称解析成功。

### 文档仓储

新增 PostgreSQL 文档仓储，建议位置为 `final/internal/repo/documentrepo.py`。

表结构：

- `documents`
- `document_versions`

`documents` 字段：

- `id`
- `title`
- `doc_type`
- `source`
- `status`
- `created_by`
- `created_at`
- `updated_at`

`document_versions` 字段：

- `id`
- `document_id`
- `version`
- `content_md`
- `summary`
- `metadata`
- `created_at`

仓储方法：

- `write(req) -> WriteResult`
- `list() -> List[Document]`
- `get(document_id) -> (Document, DocumentVersion)`
- `get_version(version_id) -> DocumentVersion`

行为：

- 新建文档时，在同一事务内写入 `documents` 和第一条 `document_versions`。
- 更新已有文档时，版本号使用 `MAX(version)+1`。
- 列表只返回非 deleted 文档，并按更新时间倒序。
- 读取文档返回最新版本。
- PostgreSQL 不可用时返回明确的 “document library not configured” 类错误，不做本地假成功。

### Agent 集成

扩展 `UnifiedAgent`，加入与 Go 等价的文档库方法：

- `write_document(req, ingest_to_rag) -> DocumentWriteResult`
- `list_documents()`
- `get_document(document_id)`
- `ingest_document(document_id, version_id="")`

`write_document(..., ingest_to_rag=True)` 必须：

1. 持久化文档和版本。
2. 将 `version.content_md` 写入 RAG。
3. 返回文档写入结果和 RAG 入库结果。

`ingest_document(document_id, version_id)` 必须：

1. 读取指定版本或最新版本。
2. 将该 Markdown 内容写入 RAG。
3. 在 Python RAG 能支持的范围内携带与 Go 语义一致的 metadata。

### 文档工具

Agent 启动时注册真实工具：

- `write_document`
- `list_documents`
- `read_document`
- `ingest_document`

工具行为对齐 Go：

- `write_document` 校验 title/content，并可同步入 RAG。
- `list_documents` 返回包含 `documents` 的 JSON。
- `read_document` 返回 `document` 和 `version`。
- `ingest_document` 将某个版本入库 RAG，并返回入库结果。

这些工具必须出现在 `/api/tools`，并能被 ReAct/tool 执行路径调用。

### 子代理

新增 Python 子代理注册表，并翻译 Go 子代理：

- `research_agent`
- `writer_agent`
- `review_agent`
- `doc_agent`

核心契约：

- `SubAgentTask` 包含 `id`、`goal`、`query`、`upstream`。
- `SubAgent` 暴露 `name`、`description`、`run(task)`。
- 注册表支持 register/get/snapshot。

子代理行为：

- `research_agent`：规划 1-3 条查询；RAG 已加载时查 RAG，否则走 `search_web`；配置真实 LLM 时用 LLM 输出结构化研究结果。
- `writer_agent`：把上游材料整理成 Markdown 报告，走真实 LLM。
- `review_agent`：审查报告结构、事实一致性、证据覆盖和风险，走真实 LLM。
- `doc_agent`：将最终内容写入文档库，并同步入 RAG。

不能新增 mock 子代理成功路径。现有 LLM client 的降级行为可以保留，但子代理代码本身必须调用真实应用路径。

按照主分支语义接入图规划和运行时：

- Planner 可以为文档/报告工作流生成 sub-agent 节点。
- Runtime 执行 sub-agent 节点，并把上游输出传给依赖节点。
- 子代理输出进入 task state/steps。

### HTTP API

扩展 FastAPI 路由，对齐 Go：

- `GET /api/documents`
- `POST /api/documents`
- `GET /api/documents/{document_id}`
- `POST /api/documents/{document_id}/ingest`

保留已有接口：

- `POST /api/upload`
- `POST /api/docs/delete`

更新 `/api/upload` 响应字段，对齐 Go：

- `filename`
- `content_type`
- `parser`
- `pages`
- `text_chars`
- `needs_ocr`
- `chunk_count`
- `parent_count`
- `indexed_count`
- `chunk_preview`
- `doc_hash`
- `chunks`

当 `needs_ocr=true` 时，返回成功 JSON，`chunk_count=0`，不静默入库 RAG。

### 前端

把主分支文档库 UI 移植到 `final/frontend/index.html`，同时尊重当前 Python 前端结构。

必须支持：

- 上传 PDF/文本并展示解析元信息。
- 从 `/api/documents` 拉取文档列表。
- 创建/写入文档。
- 读取最新文档版本。
- 将文档或版本一键入库 RAG。
- 保留现有 chat、RAG toggle、工具选择器、上传列表和删除行为。

前端应按 Go 版字段消费 Python API 返回值。

### 测试

新增聚焦测试：

- 文档模型归一化和 ID 前缀生成。
- PDF/文本解析，包括可行时覆盖 OCR-needed 路径。
- 文档仓储 write/list/get/get_version，使用受控测试数据库或窄 repo double。
- HTTP 文档路由。
- `/api/upload` 响应契约。
- 文档工具注册和执行。
- 子代理注册表和 doc-agent 写入文档库路径。
- Planner/runtime 对 sub-agent 节点的处理。
- 现有 RAG/Neo4j 测试保持通过。

验证命令至少包括：

- `python -m pytest tests/test_document_*.py`
- `python -m pytest tests/test_frontend_main_alignment.py tests/test_rag_alignment.py`
- 本地 smoke check：`/api/documents` 和 `/api/upload`

## 错误处理

- 文档仓储未配置 PostgreSQL：文档 API/tools 返回明确错误。
- 文档写入参数非法：返回 400 和可读错误。
- 文档或版本不存在：返回 404。
- PDF 解析失败：返回 400 和 parser 错误。
- PDF 需要 OCR：返回 `needs_ocr=true`，不入库 RAG。
- RAG 入库失败：如果文档已经写入成功，保留文档写入结果，并显式返回 ingest 失败信息。
- 子代理失败：节点/工具结果包含错误，不记录为成功输出。

## 迁移说明

当前本地数据库已有历史 `rag_chunks` 异常，不应要求破坏性清理。新增 document 表必须幂等创建。现有 config 和本地密钥继续不提交。

## 验收标准

- Python 分支暴露与最新 main 一致的文档库 API。
- Python 分支注册与最新 main 一致的文档工具。
- Python 分支具备真实子代理，并调用真实 RAG/search/LLM/document 路径。
- 上传 PDF/文本返回 main 兼容的解析元信息，只在文本可用时入库。
- `ingest_to_rag=true` 写文档时，持久化版本并使其可被 RAG 检索。
- `doc_agent` 能创建文档版本并同步入 RAG。
- 前端能列出、创建、读取和入库文档。
- 相关测试通过。
