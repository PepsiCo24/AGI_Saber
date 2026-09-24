# Final Stage — 全阶段整合 AI 助手（Python 版）
#
# 启动入口：
# - 加载配置（路径与 cwd 解耦）
# - 初始化基础设施
# - 构建统一智能体
# - 注册 HTTP 路由
# - 启动 FastAPI 服务
import logging
import os
import sys
from dataclasses import dataclass

# 把项目根（final/）加入 sys.path，让 `config.config` / `internal.*` 可被绝对导入
PROJECT_ROOT = os.path.dirname(os.path.abspath(__file__))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

# 默认前端目录指向 final/frontend，避免 cwd 不在项目根时挂载失败
os.environ.setdefault("FRONTEND_DIR", os.path.join(PROJECT_ROOT, "frontend"))

from config.config import default_config  # noqa: E402
from internal.agent.agent import UnifiedAgent  # noqa: E402
from internal.handler.handler import setup_routes  # noqa: E402
from internal.infra.infra import Infrastructure  # noqa: E402

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s - %(name)s - %(levelname)s - %(message)s",
)
logger = logging.getLogger(__name__)


@dataclass
class Deps:
    cfg: object
    inf: Infrastructure
    agent: UnifiedAgent
    app: object


def build_deps():
    cfg = default_config()
    inf = Infrastructure(cfg)
    agent = UnifiedAgent(cfg, inf)
    app = setup_routes(agent, inf, cfg)
    return Deps(cfg=cfg, inf=inf, agent=agent, app=app)


def main():
    deps = None
    try:
        deps = build_deps()
        print_banner(deps.cfg, deps.inf)

        import uvicorn

        uvicorn.run(deps.app, host="0.0.0.0", port=int(deps.cfg.server_port))
    finally:
        if deps is not None:
            try:
                deps.inf.close()
            except Exception:
                pass


def print_banner(cfg, inf):
    addr = f":{cfg.server_port}"
    print("========================================")
    print("Final Stage · AGI 智能助手启动成功")
    print("========================================")
    print(f"[INFO] Service       http://localhost{addr}")
    print(f"[INFO] 通用模型           {cfg.llm_model}")
    print(f"[INFO] Embedding     {cfg.embedding_model}")
    print("----------------------------------------")
    print(f"[INFO] Milvus        {inf.ready.milvus}")
    print(f"[INFO] PostgreSQL    {cfg.pg_host}:{cfg.pg_port}")
    print(f"[INFO] ElasticSearch {inf.ready.elasticsearch}")
    print(f"[INFO] Kafka         {inf.ready.kafka}")
    print("----------------------------------------")
    print("[READY] 道阻且长，行则将至。")
    print("========================================")


if __name__ == "__main__":
    main()
