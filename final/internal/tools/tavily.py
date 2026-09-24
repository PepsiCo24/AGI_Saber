# tools.tavily — Tavily Search API 客户端
"""
agent 在 search 工具触发时调用 tavily_search；调用失败或未配置 api key 时
应降级到 LLM 知识库直接回答。
"""
import logging
from typing import Optional

import requests

logger = logging.getLogger(__name__)

_DEFAULT_TAVILY_URL = "https://api.tavily.com/search"


def tavily_search(query: str, api_key: str, api_url: str = "", timeout: float = 15.0) -> str:
    """调用 Tavily Search API，返回格式化的搜索结果摘要。

    Raises:
        RuntimeError: 当请求失败或返回结构异常时抛出。
    """
    if not api_key:
        raise RuntimeError("Tavily api_key 未配置")

    url = api_url or _DEFAULT_TAVILY_URL
    payload = {
        "api_key": api_key,
        "query": query,
        "search_depth": "basic",
        "max_results": 5,
    }
    try:
        resp = requests.post(url, json=payload, timeout=timeout)
    except requests.RequestException as e:
        raise RuntimeError(f"Tavily 请求失败: {e}") from e

    if resp.status_code >= 400:
        raise RuntimeError(f"Tavily 返回错误状态: {resp.status_code}")

    try:
        data = resp.json()
    except ValueError as e:
        raise RuntimeError(f"解析 Tavily 响应失败: {e}") from e

    answer: Optional[str] = data.get("answer") if isinstance(data, dict) else None
    results = data.get("results") if isinstance(data, dict) else None
    if not isinstance(results, list):
        results = []

    # 优先返回 Tavily 合成的 answer
    if answer:
        out = [answer]
        if results:
            out.append("\n\n**来源：**\n")
            for i, r in enumerate(results):
                if i >= 3:
                    break
                if not isinstance(r, dict):
                    continue
                title = r.get("title", "")
                rurl = r.get("url", "")
                out.append(f"- [{title}]({rurl})\n")
        return "".join(out)

    # 无 answer 时拼接 top 结果摘要
    if not results:
        raise RuntimeError("Tavily 返回空结果")

    out = []
    for i, r in enumerate(results):
        if i >= 3:
            break
        if not isinstance(r, dict):
            continue
        title = r.get("title", "")
        content = r.get("content", "")
        rurl = r.get("url", "")
        out.append(f"**{title}**\n{content}\n{rurl}\n\n")
    return "".join(out).strip()
