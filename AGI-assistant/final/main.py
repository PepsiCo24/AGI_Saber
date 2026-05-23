# Final Stage — 全阶段整合 AI 助手
#
# 目录结构：
#
#      config/           配置（环境变量读取 + 默认值）
#      internal/
#        infra/          基础设施连接（Milvus / PostgreSQL / ES / Kafka）
#        llm/            LLM 客户端（真实 API + Mock 降级）
#        rag/            RAG 引擎（文本切分 + TF 向量检索）
#        tools/          工具定义与调用（time / weather / search）
#        memory/         三层记忆（短期 / 长期 / 用户偏好）
#        agent/          UnifiedAgent（ReAct + Harness + 智能路由）
#        handler/        HTTP API 路由处理
#      frontend/         单文件前端 HTML
import logging
import sys
import os

# 添加项目路径
sys.path.insert(0, os.path.join(os.path.dirname(__file__), '..'))

from config.config import default_config
from internal.agent.agent import UnifiedAgent
from internal.handler.handler import setup_routes
from internal.infra.infra import Infrastructure

logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(name)s - %(levelname)s - %(message)s'
)
logger = logging.getLogger(__name__)


def main():
    cfg = default_config()

    # 初始化基础设施（失败则降级，不阻塞启动）
    logger.info("🔧 正在连接基础设施...")
    inf = Infrastructure(cfg)

    try:
        # 初始化 UnifiedAgent
        agent = UnifiedAgent(cfg, inf)

        # 注册 HTTP 路由
        app = setup_routes(agent, inf, cfg)

        print_banner(cfg, inf)

        # 启动服务
        import uvicorn
        uvicorn.run(app, host="0.0.0.0", port=int(cfg.server_port))
    finally:
        inf.close()


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
