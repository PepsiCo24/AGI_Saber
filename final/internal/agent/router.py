# router — UnifiedAgent 的模式路由判断
#
# 对应 Go 版 internal/agent/router.go。把基于关键词的启发式判断从 agent.py
# 抽出，让主流程更聚焦。
from typing import Dict


# 关键字触发表（与 main 分支保持一致）
_TOOL_TRIGGERS = [
    ("时间", "get_time"),
    ("几点", "get_time"),
    ("现在", "get_time"),
    ("天气", "get_weather"),
    ("搜索", "search_web"),
    ("查找", "search_web"),
    ("查询", "search_web"),
    ("是什么", "search_web"),
    ("知识", "rag_search"),
    ("文档", "rag_search"),
]


def need_tool(query: str) -> bool:
    """判断 query 是否触发单一工具（时间 / 天气 / 搜索 / 查询）。"""
    q = query.lower()
    return (
        ("几点" in q) or ("时间" in q) or ("天气" in q)
        or ("查" in q) or ("搜索" in q) or ("是什么" in q)
    )


def need_rag(query: str, rag_loaded: bool) -> bool:
    """知识库已加载且本次不走工具/ReAct 时启用 RAG。"""
    return rag_loaded and not need_tool(query) and not need_react(query)


def need_react(query: str) -> bool:
    """当 query 涉及 2+ 个子需求时触发多步推理。"""
    q = query.lower()
    count = 0
    if ("时间" in q) or ("几点" in q):
        count += 1
    if "天气" in q:
        count += 1
    if ("总结" in q) or ("汇总" in q):
        count += 1
    if ("查" in q) or ("搜索" in q):
        count += 1
    return count >= 2


def need_react_from_tools(query: str, tools_map: Dict[str, object]) -> bool:
    """显式指定工具集时直接走 ReAct 路径。"""
    return len(tools_map) > 0


def detect_tool(query: str, tools_map: Dict[str, object]):
    """按关键字命中检测应调用的工具名（仅返回 tools_map 中存在的）。"""
    q = query.lower()
    for trigger, tool_name in _TOOL_TRIGGERS:
        if trigger in q and tool_name in tools_map:
            return tool_name
    return None
