# status — 系统状态视图聚合（对应 Go 版 internal/agent/status.go）
#
# 把 RAG / memory / preferences / Infrastructure 健康快照聚合为一个 dict，
# 让 handler 不必直接读取 agent 内部组件。
from typing import Any, Dict, List


def infra_status(agent) -> Dict[str, str]:
    """暴露平台层连接健康快照（供 status 端点使用）。"""
    inf = getattr(agent, "inf", None)
    if inf is None or not hasattr(inf, "ready"):
        return {}
    ready = inf.ready
    return {
        "milvus": getattr(ready, "milvus", "disconnected"),
        "postgresql": getattr(ready, "postgresql", "disconnected"),
        "elasticsearch": getattr(ready, "elasticsearch", "disconnected"),
        "kafka": getattr(ready, "kafka", "disconnected"),
    }


def status(agent) -> Dict[str, Any]:
    """构造系统状态视图模型，供 GET /api/status 渲染。"""
    rag = getattr(agent, "rag", None)
    chunk_previews: List[Dict[str, Any]] = []
    rag_loaded = False
    rag_mode = ""
    if rag is not None:
        rag_loaded = bool(getattr(rag, "loaded", False))
        if hasattr(rag, "mode"):
            try:
                rag_mode = rag.mode() if callable(rag.mode) else str(rag.mode)
            except Exception:
                rag_mode = ""
        chunks = []
        if hasattr(rag, "chunks"):
            try:
                chunks = rag.chunks() if callable(rag.chunks) else list(rag.chunks)
            except Exception:
                chunks = []
        for c in chunks or []:
            content = getattr(c, "content", None) or (c.get("content", "") if isinstance(c, dict) else "")
            cid = getattr(c, "id", None) or (c.get("id") if isinstance(c, dict) else None)
            preview = content if len(content) <= 60 else content[:60] + "..."
            chunk_previews.append({"id": cid, "content": preview})

    stm = getattr(agent, "stm", None)
    ltm = getattr(agent, "ltm", None)
    pref = getattr(agent, "preference", None)
    tools = []
    try:
        tools = agent.get_tools() if hasattr(agent, "get_tools") else []
    except Exception:
        tools = []

    return {
        "rag_loaded": rag_loaded,
        "rag_mode": rag_mode,
        "rag_chunks": chunk_previews,
        "short_term_count": stm.count() if stm and hasattr(stm, "count") else 0,
        "long_term_count": len(getattr(ltm, "items", []) or []) if ltm else 0,
        "preferences": pref.get_all() if pref and hasattr(pref, "get_all") else {},
        "tools_count": len(tools),
        "llm_model": getattr(agent.cfg, "llm_model", ""),
        "embedding_model": getattr(agent.cfg, "embedding_model", ""),
        "is_mock": not bool(agent.cfg.is_real_llm()) if hasattr(agent.cfg, "is_real_llm") else True,
        "infrastructure": infra_status(agent),
    }
