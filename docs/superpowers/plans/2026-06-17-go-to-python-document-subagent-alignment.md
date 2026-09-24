# 主分支 Go 到 Python 文档库与子代理对齐实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 `origin/main` 最新 Go 版文档库、PDF 解析、文档工具、子代理工作流和前端文档库 UI 1:1 翻译到当前 Python 分支 `final/`。

**Architecture:** 保持 Python 现有 FastAPI/Agent/Repo/RAG 结构，在 `final/internal/document` 增加领域与解析模块，在 `final/internal/repo/documentrepo.py` 增加 PostgreSQL 文档库仓储，在 `UnifiedAgent` 接入文档方法、工具和子代理。HTTP 与前端字段对齐 Go 主分支，不做 mock 成功路径。

**Tech Stack:** Python 3.9+, FastAPI, PostgreSQL/psycopg2, PyPDF2/pdfplumber optional, existing RAG/Neo4j/ES/LLM, plain HTML/JS frontend.

---

## 文件结构

- 新增 `final/internal/document/__init__.py`：导出文档领域类型和解析函数。
- 新增 `final/internal/document/library.py`：翻译 Go `library.go`，定义 `Document`、`DocumentVersion`、`WriteRequest`、`WriteResult`、`normalize_write_request`、`new_id`。
- 新增 `final/internal/document/parser.py`：翻译 Go `parser.go`，实现文本/PDF 真实解析。
- 新增 `final/internal/repo/documentrepo.py`：翻译 Go `documentrepo.go`，实现 PostgreSQL 文档库。
- 修改 `final/internal/infra/infra.py`：幂等建 `documents`、`document_versions` 表，并把 `repo.documents` 注入 repo bundle。
- 修改 `final/internal/agent/agent.py`：挂载文档库方法，注册文档工具和子代理。
- 新增 `final/internal/agent/subagents.py`：翻译 Go `subagents.go`。
- 修改 `final/internal/agent/planner.py`：识别文档/报告类任务并生成 sub-agent 节点。
- 修改 `final/internal/agent/graph_runtime.py`：执行 sub-agent 节点并传递 upstream 输出。
- 修改 `final/internal/graph/task_graph.py`：允许 `NodeType`/节点表达 sub-agent 类型。
- 修改 `final/internal/handler/handler.py`：补 `/api/documents` 系列 API，升级 `/api/upload` 返回字段。
- 修改 `final/frontend/index.html`：移植文档库 UI 和上传元信息展示。
- 新增测试：
  - `final/tests/test_document_library.py`
  - `final/tests/test_document_parser.py`
  - `final/tests/test_document_repo.py`
  - `final/tests/test_document_api.py`
  - `final/tests/test_document_tools.py`
  - `final/tests/test_subagents.py`

---

### Task 1: 文档领域模型

**Files:**
- Create: `final/internal/document/__init__.py`
- Create: `final/internal/document/library.py`
- Test: `final/tests/test_document_library.py`

- [ ] **Step 1: 写失败测试**

覆盖默认值、ID 前缀、版本结构：

```python
from internal.document.library import (
    DOCUMENT_SOURCE_AGENT,
    DOCUMENT_STATUS_ACTIVE,
    WriteRequest,
    new_id,
    normalize_write_request,
)


def test_normalize_write_request_defaults():
    req = normalize_write_request(WriteRequest(title="  报告  ", content_md="  # 内容  "))

    assert req.title == "报告"
    assert req.content_md == "# 内容"
    assert req.doc_type == "note"
    assert req.source == DOCUMENT_SOURCE_AGENT
    assert req.created_by == "agent"
    assert req.metadata == {}


def test_new_id_uses_prefix():
    assert new_id("doc").startswith("doc_")
    assert new_id("ver").startswith("ver_")
    assert DOCUMENT_STATUS_ACTIVE == "active"
```

- [ ] **Step 2: 确认测试失败**

Run: `/Users/lby/Documents/trae_projects/py_projiet/venv/bin/python -m pytest tests/test_document_library.py -v`

Expected: `ModuleNotFoundError: No module named 'internal.document'`

- [ ] **Step 3: 实现领域模型**

实现 dataclass 和 helper，字段名使用 snake_case，JSON 输出时由 API 层保持字段一致。

- [ ] **Step 4: 跑测试**

Run: `/Users/lby/Documents/trae_projects/py_projiet/venv/bin/python -m pytest tests/test_document_library.py -v`

Expected: pass

- [ ] **Step 5: 提交**

```bash
git add final/internal/document final/tests/test_document_library.py
git commit -m "feat: add document domain model"
```

### Task 2: 文档解析器

**Files:**
- Create: `final/internal/document/parser.py`
- Modify: `final/internal/document/__init__.py`
- Test: `final/tests/test_document_parser.py`

- [ ] **Step 1: 写失败测试**

测试文本解析和空文档错误：

```python
import pytest

from internal.document.parser import parse_bytes


def test_parse_plain_text_normalizes_content():
    res = parse_bytes("note.txt", "text/plain", b"hello-\nworld\n\nAGI")

    assert res.filename == "note.txt"
    assert res.content_type == "text/plain"
    assert res.parser == "plain_text"
    assert "helloworld" in res.content
    assert res.text_chars > 0
    assert res.needs_ocr is False


def test_parse_empty_text_rejects_document():
    with pytest.raises(ValueError, match="empty"):
        parse_bytes("empty.txt", "text/plain", b"   ")
```

- [ ] **Step 2: 确认测试失败**

Run: `/Users/lby/Documents/trae_projects/py_projiet/venv/bin/python -m pytest tests/test_document_parser.py -v`

Expected: import/function missing failure

- [ ] **Step 3: 实现解析器**

实现 `ParseResult`、`parse_bytes`、PDF 解析路径：优先 `pdfplumber`，回退 `PyPDF2`，再尝试 `pdftotext`。空 PDF 返回 `needs_ocr=True` 并抛出可读错误，上传 API 后续会转换为 Go 对齐响应。

- [ ] **Step 4: 跑测试**

Run: `/Users/lby/Documents/trae_projects/py_projiet/venv/bin/python -m pytest tests/test_document_parser.py -v`

Expected: pass

- [ ] **Step 5: 提交**

```bash
git add final/internal/document final/tests/test_document_parser.py
git commit -m "feat: add real document parser"
```

### Task 3: PostgreSQL 文档仓储

**Files:**
- Create: `final/internal/repo/documentrepo.py`
- Modify: `final/internal/infra/infra.py`
- Modify: `final/internal/repo/__init__.py`
- Test: `final/tests/test_document_repo.py`

- [ ] **Step 1: 写失败测试**

用窄 fake connection 验证 write/list/get 的 SQL 路径和版本递增语义，不返回假成功：

```python
import pytest

from internal.document.library import WriteRequest
from internal.repo.documentrepo import Store


def test_document_repo_requires_pg():
    store = Store(None)

    with pytest.raises(RuntimeError, match="document library not configured"):
        store.list()


def test_document_repo_write_validates_content():
    class PG:
        conn = object()
        def is_real(self): return True

    store = Store(PG())

    with pytest.raises(ValueError, match="title is required"):
        store.write(WriteRequest(content_md="body"))
```

- [ ] **Step 2: 确认测试失败**

Run: `/Users/lby/Documents/trae_projects/py_projiet/venv/bin/python -m pytest tests/test_document_repo.py -v`

Expected: module missing failure

- [ ] **Step 3: 实现仓储和表初始化**

`Store` 接收现有 `PostgresClient` 或 `None`；PG 可用时用 `self.pg.conn.cursor()` 事务写入。`infra.py` 的建表列表新增 `documents`、`document_versions`。`Infrastructure` 初始化 repo 时挂 `repo.documents = documentrepo.Store(self.pg)`。

- [ ] **Step 4: 跑测试**

Run: `/Users/lby/Documents/trae_projects/py_projiet/venv/bin/python -m pytest tests/test_document_repo.py -v`

Expected: pass

- [ ] **Step 5: 提交**

```bash
git add final/internal/repo/documentrepo.py final/internal/repo/__init__.py final/internal/infra/infra.py final/tests/test_document_repo.py
git commit -m "feat: add document repository"
```

### Task 4: Agent 文档方法和工具

**Files:**
- Modify: `final/internal/agent/agent.py`
- Modify: `final/internal/tools/tools.py` if helper reuse is needed
- Test: `final/tests/test_document_tools.py`

- [ ] **Step 1: 写失败测试**

构造 agent shell，验证工具注册和真实调用边界：

```python
from types import SimpleNamespace

from internal.agent.agent import UnifiedAgent
from internal.document.library import WriteResult, Document, DocumentVersion


def test_agent_registers_document_tools():
    agent = object.__new__(UnifiedAgent)
    agent.tool_executor = SimpleNamespace(add_tool=lambda tool: tools.append(tool))
    agent.rag = SimpleNamespace(ingest=lambda content: 1)
    agent.inf = SimpleNamespace(repo=SimpleNamespace(documents=None))
    tools = []

    agent._register_document_tools()

    assert {t.name for t in tools} >= {"write_document", "list_documents", "read_document", "ingest_document"}
```

- [ ] **Step 2: 确认测试失败**

Run: `/Users/lby/Documents/trae_projects/py_projiet/venv/bin/python -m pytest tests/test_document_tools.py -v`

Expected: `_register_document_tools` missing

- [ ] **Step 3: 实现 Agent 方法和工具**

在 `_register_builtin_tools` 后调用 `_register_document_tools`。实现 `write_document`、`list_documents`、`get_document`、`ingest_document`，工具函数返回 JSON 字符串，错误真实抛出。

- [ ] **Step 4: 跑测试**

Run: `/Users/lby/Documents/trae_projects/py_projiet/venv/bin/python -m pytest tests/test_document_tools.py -v`

Expected: pass

- [ ] **Step 5: 提交**

```bash
git add final/internal/agent/agent.py final/tests/test_document_tools.py
git commit -m "feat: register document tools"
```

### Task 5: HTTP 文档 API 和上传契约

**Files:**
- Modify: `final/internal/handler/handler.py`
- Test: `final/tests/test_document_api.py`
- Modify: `final/tests/test_frontend_main_alignment.py`

- [ ] **Step 1: 写失败测试**

用 FastAPI TestClient 验证 `/api/documents` 和 `/api/upload` 字段：

```python
def test_upload_returns_main_document_fields(client):
    res = client.post("/api/upload", json={"content": "hello document"})
    data = res.json()

    assert res.status_code == 200
    for key in ["filename", "content_type", "parser", "text_chars", "needs_ocr", "chunk_count", "doc_hash"]:
        assert key in data
```

- [ ] **Step 2: 确认测试失败**

Run: `/Users/lby/Documents/trae_projects/py_projiet/venv/bin/python -m pytest tests/test_document_api.py tests/test_frontend_main_alignment.py -v`

Expected: missing fields/routes

- [ ] **Step 3: 实现路由**

新增：

- `GET /api/documents`
- `POST /api/documents`
- `GET /api/documents/{document_id}`
- `POST /api/documents/{document_id}/ingest`

`/api/upload` 改用 `document.parser.parse_bytes`，响应字段对齐 Go。

- [ ] **Step 4: 跑测试**

Run: `/Users/lby/Documents/trae_projects/py_projiet/venv/bin/python -m pytest tests/test_document_api.py tests/test_frontend_main_alignment.py -v`

Expected: pass

- [ ] **Step 5: 提交**

```bash
git add final/internal/handler/handler.py final/tests/test_document_api.py final/tests/test_frontend_main_alignment.py
git commit -m "feat: add document HTTP api"
```

### Task 6: 子代理和图运行时

**Files:**
- Create: `final/internal/agent/subagents.py`
- Modify: `final/internal/agent/agent.py`
- Modify: `final/internal/agent/planner.py`
- Modify: `final/internal/agent/graph_runtime.py`
- Modify: `final/internal/graph/task_graph.py`
- Test: `final/tests/test_subagents.py`
- Test: `final/tests/test_graph_runtime.py`

- [ ] **Step 1: 写失败测试**

验证 registry、doc_agent 和 runtime 可执行 sub-agent 节点：

```python
from internal.agent.subagents import SubAgentTask, SubAgentRegistry


def test_subagent_registry_snapshot():
    registry = SubAgentRegistry()
    assert registry.snapshot() == {}


def test_doc_agent_writes_document(agent_shell):
    task = SubAgentTask(id="n1", goal="保存报告", query="保存", upstream={"writer_agent": "# 报告"})
    result = agent_shell.subagents.get("doc_agent").run(task)
    assert "document" in result
```

- [ ] **Step 2: 确认测试失败**

Run: `/Users/lby/Documents/trae_projects/py_projiet/venv/bin/python -m pytest tests/test_subagents.py tests/test_graph_runtime.py -v`

Expected: subagents module missing

- [ ] **Step 3: 实现子代理**

翻译 Go `subagents.go`：`ResearchAgent`、`WriterAgent`、`ReviewAgent`、`DocAgent`。接入 `agent.rag`、`agent.tool_executor`、`agent.llm`、`agent.write_document`。不做假成功。

- [ ] **Step 4: 接入 planner/runtime**

`planner.py` 对“报告/文档/总结/保存/研究”等任务生成 sub-agent 节点。`graph_runtime.py` 对 `NodeType.SUBAGENT` 或 `tool_name` 命中 sub-agent 时调用 registry，并把依赖节点结果作为 upstream。

- [ ] **Step 5: 跑测试**

Run: `/Users/lby/Documents/trae_projects/py_projiet/venv/bin/python -m pytest tests/test_subagents.py tests/test_graph_runtime.py -v`

Expected: pass

- [ ] **Step 6: 提交**

```bash
git add final/internal/agent/subagents.py final/internal/agent/agent.py final/internal/agent/planner.py final/internal/agent/graph_runtime.py final/internal/graph/task_graph.py final/tests/test_subagents.py final/tests/test_graph_runtime.py
git commit -m "feat: add subagent document workflow"
```

### Task 7: 前端文档库 UI

**Files:**
- Modify: `final/frontend/index.html`
- Test: `final/tests/test_frontend_main_alignment.py`

- [ ] **Step 1: 写失败测试**

检查前端包含主分支新增 API 调用：

```python
def test_frontend_contains_document_library_endpoints():
    html = FRONTEND.read_text(encoding="utf-8")
    assert "/api/documents" in html
    assert "ingest_to_rag" in html
    assert "text_chars" in html
    assert "needs_ocr" in html
```

- [ ] **Step 2: 确认测试失败**

Run: `/Users/lby/Documents/trae_projects/py_projiet/venv/bin/python -m pytest tests/test_frontend_main_alignment.py -v`

Expected: assertions fail

- [ ] **Step 3: 移植前端**

从 `origin/main:frontend/index.html` 移植文档库区域和 JS：文档列表、创建文档、读取文档、一键 ingest、上传元信息展示。保留当前聊天 UI。

- [ ] **Step 4: 跑测试**

Run: `/Users/lby/Documents/trae_projects/py_projiet/venv/bin/python -m pytest tests/test_frontend_main_alignment.py -v`

Expected: pass

- [ ] **Step 5: 提交**

```bash
git add final/frontend/index.html final/tests/test_frontend_main_alignment.py
git commit -m "feat: align document library frontend"
```

### Task 8: 端到端验证和收尾

**Files:**
- Modify tests only if integration contract needs final assertion.

- [ ] **Step 1: 跑核心测试**

Run:

```bash
/Users/lby/Documents/trae_projects/py_projiet/venv/bin/python -m pytest \
  tests/test_document_library.py \
  tests/test_document_parser.py \
  tests/test_document_repo.py \
  tests/test_document_api.py \
  tests/test_document_tools.py \
  tests/test_subagents.py \
  tests/test_frontend_main_alignment.py \
  tests/test_rag_alignment.py
```

Expected: pass

- [ ] **Step 2: 重启项目**

停止旧 8090 进程后运行：

```bash
/Users/lby/Documents/trae_projects/py_projiet/venv/bin/python main.py
```

Expected: 启动日志显示 PostgreSQL/Elasticsearch/Neo4j 连接状态，FastAPI 监听 8090。

- [ ] **Step 3: smoke check**

Run:

```bash
curl -sS http://127.0.0.1:8090/api/documents
curl -sS -X POST http://127.0.0.1:8090/api/documents \
  -H 'Content-Type: application/json' \
  -d '{"title":"对齐验证","content_md":"# 对齐验证\n\n主分支文档库翻译到 Python。","ingest_to_rag":true}'
```

Expected: 第一个返回 `documents`，第二个返回 `document`、`version`，并包含 ingest 结果。

- [ ] **Step 4: 最终状态检查**

Run: `git status --short`

Expected: 只剩用户明确不提交的本地配置或运行态文件；功能代码已分任务提交。
