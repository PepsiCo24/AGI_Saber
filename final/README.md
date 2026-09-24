# AGI Assistant · 新同学启动指南

一份"从 git clone 到看到首页"的最短路径。读完按步骤操作即可启动。

> 项目位置：本仓库的 `python` 分支，代码在 `final/` 目录下。
> 后端：FastAPI + Uvicorn（端口 **8090**）；前端：单页 HTML，由后端静态挂载在 `/`。

---

## 0. 环境要求

| 依赖 | 版本 | 说明 |
|---|---|---|
| Python | 3.10 ~ 3.11（推荐 3.11） | 不建议 3.13+ |
| pip | ≥ 23 | |
| Docker / Docker Compose | 可选 | 启动 PG/ES/Milvus/Neo4j/Kafka 全套基础设施时需要 |
| Git | any | |

> macOS / Linux 均已验证；Windows 推荐 WSL2。

---

## 1. 拉代码 & 切到 python 分支

```bash
git clone git@github.com:AGI-Core/AGI-saber.git
cd AGI-saber
git checkout python
cd final
```

---

## 2. 两种启动方式（任选其一）

### 方式 A：纯本地，不起任何基础设施（最快，1 分钟跑通）

所有外部依赖（PG / ES / Milvus / Neo4j / Kafka）都做了 **优雅降级**，没装也能起。

```bash
# 1) 创建虚拟环境（强烈推荐，避免污染系统）
python3.11 -m venv .venv
source .venv/bin/activate

# 2) 安装依赖
pip install -r requirements.txt

# 3) 启动
python main.py
```

看到这一行就成功了：

```
INFO:     Uvicorn running on http://0.0.0.0:8090 (Press CTRL+C to quit)
```

打开浏览器：

- 前端：http://localhost:8090/
- 健康检查：http://localhost:8090/health

> 此模式下 RAG/记忆/图谱使用内存 mock，重启会丢；调试 UI / 调通 LLM 调用时够用。

---

### 方式 B：完整基础设施（推荐用于真实测试）

```bash
# 1) 起 PG / ES / Milvus / Neo4j / Kafka（首次拉镜像可能需要 5 分钟）
docker-compose up -d

# 2) 等待容器全部 healthy（大约 30 ~ 60 秒）
docker-compose ps

# 3) 安装 Python 依赖（同方式 A）
python3.11 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt

# 4) 启动应用
python main.py
```

成功后，`/health` 返回的字段全部不再是 `disconnected`。

如果只想用 Docker 跑应用本身：

```bash
docker-compose up -d   # 包含 app 服务，会自动构建镜像
docker-compose logs -f app
```

---

## 3. 配置 LLM Key（重要）

默认配置文件：[final/config/config.yaml](./config/config.yaml)

```yaml
llm:
  api_url: https://ark.cn-beijing.volces.com/api/v3/chat/completions
  api_key: ""        # ← 这里填你自己的火山方舟 Key
  model: deepseek-v4-flash-260425

embedding:
  api_url: https://ark.cn-beijing.volces.com/api/v3/embeddings/multimodal
  api_key: ""        # ← 这里填你自己的 Key
  model: doubao-embedding-vision-250615
```

> **不要把 Key 提交到 Git**。可以用环境变量覆盖，也可以本地改完后 `git update-index --assume-unchanged config/config.yaml` 防误提交。

获取 Key：
- 火山方舟控制台：https://console.volcengine.com/ark
- 创建一个有 `Chat Completions` + `Embedding` 权限的 API Key

---

## 4. 自检清单

```bash
# 健康检查
curl http://localhost:8090/health

# 前端首页（应返回 HTML）
curl -s http://localhost:8090/ | head -c 100

# 简单聊天（流式 SSE）
curl -N -X POST http://localhost:8090/api/chat \
  -H "Content-Type: application/json" \
  -d '{"message":"你好","user_id":"u_test"}'
```

---

## 5. 常见问题

### Q1：`AttributeError: module 'marshmallow' has no attribute '__version_info__'`

依赖冲突。`pymilvus` 间接依赖 `environs` → `marshmallow`，新版 `marshmallow 4.x` 不兼容。

**修复**：本仓库 requirements.txt 已 pin `marshmallow<4` 和 `environs<10`。重新安装即可：

```bash
pip install -r requirements.txt --upgrade
```

### Q2：`RuntimeError: Form data requires "python-multipart" to be installed.`

```bash
pip install python-multipart
```

（已写入 requirements.txt，正常 `pip install -r` 就有）

### Q3：`ImportError: cannot import name 'SearchResult' from 'internal.rag.rag'`

旧代码遗留，最新 `python` 分支已修。`git pull origin python` 即可。

### Q4：端口 8090 被占用

```bash
# macOS / Linux
lsof -i :8090
kill -9 <PID>
```

或修改 [main.py](./main.py) 末尾的 `port=8090`。

### Q5：基础设施全部 `disconnected`

属于 **预期行为**，应用做了优雅降级，可以直接用。要接通就跑 `docker-compose up -d`。

### Q6：Milvus 启动慢 / 卡死

Milvus 单机版依赖 etcd + minio，**首次启动需 60s+**。先看日志：

```bash
docker-compose logs -f milvus
```

如果实在不需要 Milvus，可以在 `docker-compose.yml` 注释掉 milvus / etcd / minio 三个服务。

---

## 6. 项目结构速览

```
final/
├── main.py                 # 入口，启 FastAPI app
├── config/
│   ├── config.yaml         # 配置文件（API Key 在这）
│   └── config.py           # 配置加载
├── frontend/
│   └── index.html          # 单页前端（被 / 静态挂载）
├── internal/
│   ├── handler/            # HTTP 路由
│   ├── agent/              # ReAct Agent / Router / Planner
│   ├── llm/                # LLM 客户端
│   ├── rag/                # 三路 RRF 检索
│   ├── memory/             # 三层记忆 + 图记忆
│   ├── graph/              # Neo4j 知识图谱
│   ├── promptctx/          # Prompt 多源装配
│   ├── platform/           # PG/ES/Milvus/Kafka/Neo4j 客户端
│   ├── repo/               # 数据访问层
│   ├── sandbox/            # Docker / 本地沙箱
│   ├── tools/              # exec_command / tavily 等工具
│   └── infra/              # Infrastructure 初始化
├── Dockerfile
├── docker-compose.yml
└── requirements.txt
```

---

## 7. 开发约定

- 提交信息使用 [Conventional Commits](https://www.conventionalcommits.org/)：`feat(xxx): ...` / `fix(xxx): ...`
- 新增依赖一定要写进 `requirements.txt` 并明确版本
- API Key / 私钥 **绝不提交**
- `python` 分支为 Python 主线，`main` 分支为 Go 版本

---

有任何卡点直接问，或者把启动日志贴给维护者。Happy hacking 🚀
