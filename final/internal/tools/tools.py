# tools — 工具定义、调用与注册（Python 版与 main 分支 Go 版 tools.go 对齐）
#
# 内置工具集合（与 Go 版 toolimpl.DefaultTools 对齐）：get_time / get_weather / search_web。
# 注意 rag_search 不在此处注册——它依赖 Agent 持有的 RAG 引擎实例，
# 由 internal/agent/agent.py 的 _register_builtin_tools 在 agent 启动期动态注入闭包。
#
# 此外提供以下能力：
#   - exec_command：通过 sandbox 在隔离环境执行终端命令
#   - tavily：调用 Tavily Search API（search_web 双层降级：tavily → LLM → mock）
#   - decide：基于关键字的简单工具选择器（对应 Go 版 tools.Decide）
import logging
import threading
import time
from dataclasses import dataclass, field
from typing import Any, Callable, Dict, List, Optional

import requests

logger = logging.getLogger(__name__)


# ─────────────────────────────── 数据结构 ────────────────────────────────────

@dataclass
class Tool:
    name: str
    description: str
    params: List[Dict[str, str]]
    func: Callable[[Dict[str, Any]], str]
    is_mcp: bool = False


@dataclass
class CallResult:
    success: bool
    content: str
    error: Optional[str] = None
    # 兼容 Go 版 CallResult 字段（部分调用方期望）
    tool_name: str = ""
    params: Dict[str, Any] = field(default_factory=dict)


# ─────────────────────────────── 内置工具 ────────────────────────────────────

def get_time(args: Dict[str, Any]) -> str:
    """返回当前时间，支持可选时区参数（与 Go 版 GetTime 对齐）。"""
    tz = args.get("timezone") if isinstance(args, dict) else None
    if tz:
        try:
            from datetime import datetime
            from zoneinfo import ZoneInfo

            return datetime.now(ZoneInfo(tz)).strftime("%Y-%m-%d %H:%M:%S")
        except Exception:
            pass
    return time.strftime("%Y-%m-%d %H:%M:%S", time.localtime())


def get_weather(args: Dict[str, Any]) -> str:
    """模拟天气查询（与 Go 版 GetWeather 对齐，含小型词表）。"""
    db = {
        "北京": "晴天 22°C",
        "东京": "多云 18°C 湿度65%",
        "上海": "小雨 20°C",
        "纽约": "晴天 15°C",
        "伦敦": "阴天 12°C",
        "广州": "晴天 28°C",
        "深圳": "晴天 26°C",
    }
    city = (args.get("city") or "北京").strip() if isinstance(args, dict) else "北京"
    if city in db:
        return f"{city}：{db[city]}"
    return f"{city}：晴天 20°C（模拟）"


def _mock_search(query: str) -> str:
    """search 工具的 mock 兜底实现（与 Go 版 SearchWeb 对齐）。"""
    db = {
        "AI应用工程师": "AI 应用工程师是将 AI 技术落地到业务的工程师，需具备 ML 基础、API 开发、Prompt 工程等能力。",
        "Go语言": "Go 是 Google 开发的开源编程语言，适用于高并发服务端应用。Docker 即用 Go 开发。",
    }
    for k, v in db.items():
        if k in query:
            return v
    return f"关于「{query}」的搜索结果（模拟）"


def search_web_factory(cfg=None, llm=None) -> Callable[[Dict[str, Any]], str]:
    """构造 search_web 工具的执行函数：

      1) 已配置 search_api_key → 调 Tavily 真实搜索
      2) Tavily 失败但 llm 可用 → 用 LLM 知识库回答
      3) 否则 → mock 搜索结果
    """
    from .tavily import tavily_search  # 延迟导入避免循环

    def _execute(args: Dict[str, Any]) -> str:
        query = args.get("query", "") if isinstance(args, dict) else ""
        if not query:
            return "请提供搜索关键词"

        api_key = getattr(cfg, "search_api_key", "") if cfg is not None else ""
        api_url = getattr(cfg, "search_api_url", "") if cfg is not None else ""

        # 1. Tavily 真实搜索
        if api_key:
            try:
                return tavily_search(query, api_key, api_url)
            except Exception as e:
                logger.warning("Tavily 搜索失败，降级: %s", e)

        # 2. LLM 知识库降级
        if llm is not None:
            try:
                from internal.llm.llm import Message  # 延迟导入

                resp = llm.chat(
                    [Message(role="user", content=f"请用简洁中文回答：{query}")],
                    system_prompt="你是搜索助手，基于已知知识简明回答用户问题。",
                )
                if resp:
                    return resp
            except Exception as e:
                logger.warning("LLM 降级搜索失败: %s", e)

        # 3. mock 兜底
        return _mock_search(query)

    return _execute


def search_web(args: Dict[str, Any]) -> str:
    """无依赖的 search_web 默认实现（仅 mock）。"""
    return _mock_search(args.get("query", "") if isinstance(args, dict) else "")


# ─────────────────────────────── tavily / exec_command 工具 ──────────────────

def build_tavily_tool(cfg=None) -> Tool:
    """单独以 'tavily' 名称暴露的工具：仅调用 Tavily（未配置时降级 mock）。"""
    from .tavily import tavily_search  # 延迟导入

    def _execute(args: Dict[str, Any]) -> str:
        query = args.get("query", "") if isinstance(args, dict) else ""
        if not query:
            return "请提供搜索关键词"
        api_key = getattr(cfg, "search_api_key", "") if cfg is not None else ""
        api_url = getattr(cfg, "search_api_url", "") if cfg is not None else ""
        if not api_key:
            return _mock_search(query)
        try:
            return tavily_search(query, api_key, api_url)
        except Exception as e:
            logger.warning("Tavily 调用失败: %s", e)
            return _mock_search(query)

    return Tool(
        name="tavily",
        description="使用 Tavily Search API 进行真实互联网搜索（需要配置 search_api_key）。",
        params=[{"name": "query", "type": "string", "description": "搜索关键词"}],
        func=_execute,
    )


def build_exec_command_tool(sandbox) -> Optional[Tool]:
    """构造 exec_command 工具（要求传入 sandbox 实例；为 None 时返回 None）。"""
    if sandbox is None:
        return None
    from .exec_command import exec_command_tool_factory  # 延迟导入

    return Tool(
        name="exec_command",
        description=(
            "在隔离沙箱中执行终端命令。支持 ls/cat/echo/python3/node 等常见操作；"
            "危险命令（rm -rf、sudo、网络外联等）会被自动拒绝；"
            "涉及删除/安装/管道等中等风险命令需通过 confirm=true 二次确认。"
        ),
        params=[
            {"name": "command", "type": "string", "description": "要执行的 Shell 命令（单条，禁止命令链）"},
            {"name": "confirm", "type": "boolean", "description": "对 warn 级命令的二次确认；默认 false"},
        ],
        func=exec_command_tool_factory(sandbox),
    )


# ─────────────────────────────── 默认工具集 ──────────────────────────────────

def default_tools(cfg=None, llm=None, sandbox=None) -> List[Tool]:
    """返回默认工具集合（与 Go 版 toolimpl.DefaultTools 对齐）。

    Args:
        cfg:     APIConfig 实例。提供后 search_web 将启用 Tavily / LLM 双层降级。
        llm:     LLM 客户端（提供 chat 方法）。用于 search_web 的 LLM 降级。
        sandbox: sandbox.Sandbox 实例。提供后会自动注册 exec_command 工具。

    返回的 Tool 列表包含：get_time / get_weather / search_web；
    并在条件满足时追加 tavily 与 exec_command。

    注意：rag_search 不在默认工具集中，因为它依赖 Agent 的 RAG 引擎实例，
    由 internal/agent/agent.py 的 _register_builtin_tools 在 agent 启动期动态注入。
    """
    search_func = search_web_factory(cfg=cfg, llm=llm) if (cfg is not None or llm is not None) else search_web

    tools: List[Tool] = [
        Tool(name="get_time", description="获取当前系统时间", params=[], func=get_time),
        Tool(
            name="get_weather",
            description="获取指定城市的天气信息",
            params=[{"name": "city", "type": "string", "description": "城市名称"}],
            func=get_weather,
        ),
        Tool(
            name="search_web",
            description="执行网络搜索（Tavily → LLM 知识库 → mock 三层降级）",
            params=[{"name": "query", "type": "string", "description": "搜索关键词"}],
            func=search_func,
        ),
    ]

    # 配置了 search_api_key 时额外暴露独立的 tavily 工具
    if cfg is not None and getattr(cfg, "search_api_key", ""):
        tools.append(build_tavily_tool(cfg))

    # sandbox 可用时注册 exec_command
    if sandbox is not None:
        tool = build_exec_command_tool(sandbox)
        if tool is not None:
            tools.append(tool)

    return tools


# ─────────────────────────────── 工具调用器 ──────────────────────────────────

class ToolExecutor:
    """与 main 分支 Go 版接口对齐的工具执行器。

    内部用 ``threading.RLock`` 串行化 ``_tool_map`` 的读写。多路并发调用方：
      - register / add_tool（写）
      - call / get_tool_descriptions / snapshot / filter_tools / 路由侧
        ``self.tool_executor._tool_map`` 直读（读）

    与 main toolRegistry 对齐：写持锁；读经 ``snapshot``/``filter_tools``
    返回浅拷贝供调用方无锁遍历。``call`` 内部以一次 snapshot 查询 + 锁外
    执行 ``tool.func`` 的方式，避免长时占锁。
    """

    def __init__(self, tools: Optional[List[Tool]] = None):
        self.tools = tools if tools else default_tools()
        self._lock = threading.RLock()
        self._tool_map = {t.name: t for t in self.tools}

    def call(self, tool_name: str, args: Dict[str, Any]) -> CallResult:
        with self._lock:
            tool = self._tool_map.get(tool_name)
        if tool is None:
            return CallResult(
                success=False, content="", error=f"工具 {tool_name} 不存在",
                tool_name=tool_name, params=args or {},
            )
        try:
            result = tool.func(args or {})
            return CallResult(success=True, content=str(result), tool_name=tool_name, params=args or {})
        except Exception as e:
            logger.error("工具调用失败: %s", e)
            return CallResult(
                success=False, content="", error=str(e),
                tool_name=tool_name, params=args or {},
            )

    def get_tool_descriptions(self) -> List[Dict[str, Any]]:
        with self._lock:
            tools = list(self.tools)
        out: List[Dict[str, Any]] = []
        for tool in tools:
            out.append({
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
                "is_mcp": tool.is_mcp,
            })
        return out

    def add_tool(self, tool: Tool) -> None:
        with self._lock:
            existed = tool.name in self._tool_map
            self._tool_map[tool.name] = tool
            if existed:
                # 覆盖既有同名工具：保持 self.tools 唯一性
                self.tools = [t for t in self.tools if t.name != tool.name]
            self.tools.append(tool)

    def snapshot(self) -> Dict[str, Tool]:
        """返回 _tool_map 的浅拷贝，供调用方无锁遍历（对应 main snapshot）。"""
        with self._lock:
            return dict(self._tool_map)

    def filter_tools(self, names: List[str]) -> Dict[str, Tool]:
        """按名单返回 _tool_map 的子集（对应 main filter）。"""
        with self._lock:
            return {n: self._tool_map[n] for n in names if n in self._tool_map}


# ─────────────────────────────── 工具选择器 ──────────────────────────────────

def decide(query: str, ts: Dict[str, Tool]) -> Optional[CallResult]:
    """基于关键字推断应调用的工具及参数（对应 Go 版 tools.Decide）。

    只会返回 ts 中实际存在的工具；若无任何工具命中，则取首个工具兜底。
    """
    if not ts:
        return None
    q = query.lower()

    if ("几点" in q) or ("时间" in q):
        if "get_time" in ts:
            params: Dict[str, Any] = {}
            if "东京" in q:
                params["timezone"] = "Asia/Tokyo"
            return CallResult(success=True, content="", tool_name="get_time", params=params)

    if "天气" in q:
        if "get_weather" in ts:
            city = "北京"
            for c in ["东京", "北京", "上海", "纽约", "伦敦", "广州", "深圳"]:
                if c in q:
                    city = c
                    break
            return CallResult(success=True, content="", tool_name="get_weather", params={"city": city})

    if ("查" in q) or ("搜索" in q) or ("是什么" in q):
        if "search_web" in ts:
            return CallResult(success=True, content="", tool_name="search_web", params={"query": query})

    if "exec_command" in ts:
        return CallResult(success=True, content="", tool_name="exec_command", params={"command": query})

    # 兜底：取集合中第一个工具，使用首个必填参数名（缺省 'query'）
    for name, t in ts.items():
        param_name = "query"
        for p in t.params:
            param_name = p.get("name", "query")
            break
        return CallResult(success=True, content="", tool_name=name, params={param_name: query})

    return None


# ─────────────────────────────── MCP 工具 ────────────────────────────────────

def new_mcp_tool(
    name: str,
    description: str,
    params: List[Dict[str, str]],
    func: Optional[Callable[[Dict[str, Any]], str]] = None,
    endpoint: str = "",
) -> Tool:
    """创建 MCP 工具：

    若提供 func 则直接使用（兼容旧调用）；否则当 endpoint 非空时构造 HTTP POST 调用器
    （对应 Go 版 NewMCPTool 行为）。
    """
    if func is None and endpoint:
        def _http_call(p: Dict[str, Any]) -> str:
            try:
                resp = requests.post(endpoint, json=p, timeout=30)
            except requests.RequestException as e:
                raise RuntimeError(f"MCP 请求失败 [{endpoint}]: {e}") from e
            if resp.status_code >= 400:
                raise RuntimeError(f"MCP 返回错误状态 {resp.status_code} [{endpoint}]")
            return resp.text

        func = _http_call

    if func is None:
        def _noop(_p: Dict[str, Any]) -> str:
            return f"[MCP] {name} 未配置 func 或 endpoint"

        func = _noop

    return Tool(name=name, description=description, params=params, func=func, is_mcp=True)
