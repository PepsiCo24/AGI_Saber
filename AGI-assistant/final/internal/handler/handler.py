# handler — HTTP API 路由处理
import json
import logging
from typing import Dict, Any
from io import BytesIO

from fastapi import FastAPI, HTTPException, UploadFile, File
from fastapi.staticfiles import StaticFiles

from config.config import APIConfig
from internal.agent.agent import UnifiedAgent
from internal.infra.infra import Infrastructure

logger = logging.getLogger(__name__)

# PDF 解析支持
try:
    from PyPDF2 import PdfReader
    _HAS_PDF = True
except ImportError:
    _HAS_PDF = False
    logger.warning("⚠️  PyPDF2 未安装，PDF 解析不可用")


def extract_text_from_file(file: UploadFile, content: bytes) -> str:
    """根据文件类型提取文本内容"""
    filename = file.filename or ""
    
    # PDF 文件处理
    if filename.lower().endswith(".pdf"):
        if not _HAS_PDF:
            raise HTTPException(status_code=500, detail="PyPDF2 未安装，无法解析 PDF")
        try:
            reader = PdfReader(BytesIO(content))
            text = ""
            for page in reader.pages:
                text += page.extract_text() or ""
            return text
        except Exception as e:
            logger.error("PDF 解析失败: %s", e)
            raise HTTPException(status_code=500, detail=f"PDF 解析失败: {str(e)}")
    
    # 文本文件处理（.txt, .md 等）
    try:
        return content.decode("utf-8")
    except UnicodeDecodeError:
        # 尝试其他编码
        return content.decode("gbk", errors="ignore")


def setup_routes(agent: UnifiedAgent, inf: Infrastructure, cfg: APIConfig) -> FastAPI:
    """设置 HTTP 路由"""
    app = FastAPI(title="AGI Assistant", version="1.0")

    # ─────────────────────────────── 聊天接口 ────────────────────────────

    @app.post("/api/chat")
    async def chat(request: Dict[str, Any]):
        """聊天接口"""
        try:
            message = request.get("message", "")
            use_rag = request.get("use_rag", False)
            
            if not message:
                raise HTTPException(status_code=400, detail="缺少 message 参数")

            # 路由到 agent
            response = agent.route(message, use_rag=use_rag)

            return {"answer": response, "success": True}

        except Exception as e:
            logger.error("聊天接口错误: %s", e)
            raise HTTPException(status_code=500, detail=str(e))

    # ─────────────────────────────── 文档上传 ────────────────────────────

    @app.post("/api/upload")
    async def upload(file: UploadFile = File(...)):
        """上传文档到知识库"""
        try:
            content = await file.read()
            
            # 根据文件类型提取文本
            text = extract_text_from_file(file, content)
            
            if not text.strip():
                return {"chunk_count": 0, "success": False, "message": "文件内容为空"}

            # 向 RAG 引擎添加文档
            chunk_count = agent.rag_ingest(text)

            return {"chunk_count": chunk_count, "success": True}

        except Exception as e:
            logger.error("上传接口错误: %s", e)
            raise HTTPException(status_code=500, detail=str(e))

    # ─────────────────────────────── RAG 查询 ────────────────────────────

    @app.post("/api/rag/query")
    async def rag_query(request: Dict[str, Any]):
        """RAG 知识库查询"""
        try:
            question = request.get("question", "")
            if not question:
                raise HTTPException(status_code=400, detail="缺少 question 参数")

            answer, results = agent.rag_query(question)

            return {
                "answer": answer,
                "results": [{"content": r["content"], "score": r["score"], "source": r.get("source", "unknown")} for r in results],
                "success": True
            }

        except Exception as e:
            logger.error("RAG 查询接口错误: %s", e)
            raise HTTPException(status_code=500, detail=str(e))

    # ─────────────────────────────── 服务状态 ────────────────────────────

    @app.get("/api/status")
    async def status():
        """获取服务状态"""
        return {
            "status": "running",
            "milvus": inf.ready.milvus,
            "postgresql": inf.ready.postgresql,
            "elasticsearch": inf.ready.elasticsearch,
            "kafka": inf.ready.kafka,
            "llm": cfg.llm_model,
            "embedding": cfg.embedding_model,
        }

    # ─────────────────────────────── 工具列表 ────────────────────────────

    @app.get("/api/tools")
    async def tools():
        """获取可用工具列表"""
        return {"tools": agent.get_tools()}

    # ─────────────────────────────── 记忆系统状态 ────────────────────────

    @app.get("/api/memory")
    async def memory():
        """获取记忆系统状态"""
        return {
            "short_term_turns": agent.stm.count(),
            "long_term_count": len(agent.ltm.items),
            "preferences": agent.preference.get_all(),
        }

    # ─────────────────────────────── 挂载前端静态资源 ────────────────────

    app.mount("/", StaticFiles(directory="frontend", html=True), name="frontend")

    return app
