# tools — 工具定义与调用（time / weather / search）
import json
import logging
import time
from dataclasses import dataclass, field
from typing import List, Optional, Dict, Any, Callable

import requests

logger = logging.getLogger(__name__)


@dataclass
class Tool:
    name: str
    description: str
    params: List[Dict[str, str]]
    func: Callable


@dataclass
class CallResult:
    success: bool
    content: str
    error: Optional[str] = None


# ─────────────────────────────── 内置工具 ────────────────────────────────

def get_time(args: Dict[str, str]) -> str:
    """获取当前时间"""
    return time.strftime("%Y-%m-%d %H:%M:%S", time.localtime())


def get_weather(args: Dict[str, str]) -> str:
    """获取天气信息（模拟）"""
    city = args.get("city", "北京")
    return f"{city} 今天天气晴朗，温度 25°C，风力 3 级。"


def search_web(args: Dict[str, str]) -> str:
    """网络搜索（模拟）"""
    query = args.get("query", "")
    if not query:
        return "请提供搜索关键词"
    return f"搜索结果：关于 '{query}' 的相关信息...（模拟搜索）"


def rag_search(args: Dict[str, str]) -> str:
    """RAG 知识库检索"""
    query = args.get("query", "")
    return f"知识库检索结果：关于 '{query}' 的相关知识...（需要连接 RAG 引擎）"


# ─────────────────────────────── 工具列表 ────────────────────────────────

def default_tools() -> List[Tool]:
    """获取默认工具列表"""
    return [
        Tool(
            name="get_time",
            description="获取当前系统时间",
            params=[],
            func=get_time,
        ),
        Tool(
            name="get_weather",
            description="获取指定城市的天气信息",
            params=[{"name": "city", "type": "string", "description": "城市名称"}],
            func=get_weather,
        ),
        Tool(
            name="search_web",
            description="执行网络搜索",
            params=[{"name": "query", "type": "string", "description": "搜索关键词"}],
            func=search_web,
        ),
        Tool(
            name="rag_search",
            description="在知识库中检索相关信息",
            params=[{"name": "query", "type": "string", "description": "检索关键词"}],
            func=rag_search,
        ),
    ]


# ─────────────────────────────── 工具调用器 ──────────────────────────────

class ToolExecutor:
    """工具执行器"""

    def __init__(self, tools: Optional[List[Tool]] = None):
        self.tools = tools if tools else default_tools()
        self._tool_map = {t.name: t for t in self.tools}

    def call(self, tool_name: str, args: Dict[str, str]) -> CallResult:
        """调用指定工具"""
        if tool_name not in self._tool_map:
            return CallResult(
                success=False,
                content="",
                error=f"工具 {tool_name} 不存在"
            )

        tool = self._tool_map[tool_name]
        try:
            result = tool.func(args)
            return CallResult(
                success=True,
                content=str(result)
            )
        except Exception as e:
            logger.error("工具调用失败: %s", e)
            return CallResult(
                success=False,
                content="",
                error=str(e)
            )

    def get_tool_descriptions(self) -> List[Dict[str, Any]]:
        """获取所有工具的描述信息（用于 LLM 选择）"""
        descriptions = []
        for tool in self.tools:
            descriptions.append({
                "name": tool.name,
                "description": tool.description,
                "parameters": [
                    {
                        "name": p["name"],
                        "type": p["type"],
                        "description": p.get("description", ""),
                    }
                    for p in tool.params
                ],
            })
        return descriptions

    def add_tool(self, tool: Tool):
        """添加自定义工具"""
        self.tools.append(tool)
        self._tool_map[tool.name] = tool


# ─────────────────────────────── 工具选择器 ──────────────────────────────

def decide(query: str, tools: List[Tool]) -> Optional[str]:
    """根据用户查询决定是否调用工具（简单规则匹配）"""
    query_lower = query.lower()

    # 简单的规则匹配
    tool_triggers = {
        "时间": "get_time",
        "几点": "get_time",
        "现在": "get_time",
        "天气": "get_weather",
        "搜索": "search_web",
        "查找": "search_web",
        "知识": "rag_search",
        "文档": "rag_search",
    }

    for trigger, tool_name in tool_triggers.items():
        if trigger in query_lower:
            return tool_name

    return None


def new_mcp_tool(name: str, description: str, params: List[Dict[str, str]], func: Callable) -> Tool:
    """创建新的 MCP 工具"""
    return Tool(
        name=name,
        description=description,
        params=params,
        func=func,
    )
