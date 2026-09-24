# planner — UnifiedAgent 的工具规划器
#
# 对应 Go 版 internal/agent/planner.go：在 ReAct 模式下由 Planner LLM 根据
# 可用工具集和用户问题产出一组 PlanItem，Harness 再逐项重试执行。
# LLM 不可用或解析失败时降级到 rule_plan_items 关键字规则。
import json
import logging
import re
from dataclasses import dataclass, field
from typing import Any, Dict, List

from internal.graph.task_graph import Node, NodeType
from internal.llm.llm import Message

logger = logging.getLogger(__name__)


@dataclass
class PlanItem:
    """Planner LLM 输出的单个工具调用计划。"""
    tool: str = ""
    params: Dict[str, str] = field(default_factory=dict)
    reason: str = ""


def llm_plan_steps(agent, query: str, tools_map: Dict[str, Any], mem_prefix: str) -> List[PlanItem]:
    """调用 Planner LLM 选择需要调用的工具及参数。

    LLM 不可用或解析失败时降级到关键字规则。
    """
    if not agent.cfg.is_real_llm():
        return rule_plan_items(agent, query, tools_map)

    # 构造工具描述
    tool_lines: List[str] = []
    for name, t in tools_map.items():
        p_descs: List[str] = []
        for p in getattr(t, "params", []) or []:
            req = "（必填）" if p.get("required") else ""
            p_descs.append(f"{p.get('name','')}({p.get('type','string')}){req}")
        params = ", ".join(p_descs) if p_descs else "无"
        tool_lines.append(f"- {name}: {t.description} [参数: {params}]")

    plan_prompt = (
        "你是一个任务规划器。\n"
        "根据用户问题，从可用工具中选出真正需要调用的工具（不要为了用工具而用工具，按需选择）。\n"
        f"用户问题：{query}\n"
        f"可用工具：\n{chr(10).join(tool_lines)}\n"
        "请以 JSON 数组格式输出执行计划，格式如下：\n"
        '[{"tool":"工具名","params":{"参数名":"参数值"},"reason":"一句话说明为什么调用这个工具"}]\n'
        "如果无需工具直接回答，输出 []。只输出 JSON，不要其他内容。"
    )

    planner_base = "你是一个精准的任务规划器，只在必要时才调用工具，不做无意义的调用。"
    if mem_prefix:
        planner_base = mem_prefix + "\n\n" + planner_base + "\n注意：用户偏好可能影响工具参数选择（如城市、时区等），请在参数中体现。"

    try:
        raw = agent.llm.chat(
            [Message(role="user", content=plan_prompt)],
            system_prompt=planner_base,
        )
    except Exception as e:
        logger.warning("Planner LLM 调用失败: %s，降级到规则", e)
        return rule_plan_items(agent, query, tools_map)

    # 清洗 LLM 输出
    raw = (raw or "").strip()
    # 剥离模型输出的 <|FunctionCallBegin|>...<|FunctionCallEnd|> 包装
    if "<|FunctionCallBegin|>" in raw:
        idx = raw.index("<|FunctionCallBegin|>") + len("<|FunctionCallBegin|>")
        raw = raw[idx:]
        if "<|FunctionCallEnd|>" in raw:
            raw = raw[: raw.index("<|FunctionCallEnd|>")]
    raw = re.sub(r"^```json", "", raw)
    raw = re.sub(r"^```", "", raw)
    raw = re.sub(r"```$", "", raw)
    raw = raw.strip()

    # 尝试解析为 [{"tool":...,"params":...}] 格式
    items: List[PlanItem] = []
    try:
        data = json.loads(raw)
        if isinstance(data, list):
            for d in data:
                if not isinstance(d, dict):
                    continue
                if "tool" in d:
                    items.append(PlanItem(
                        tool=str(d.get("tool", "")),
                        params={k: str(v) for k, v in (d.get("params") or {}).items()},
                        reason=str(d.get("reason", "")),
                    ))
                elif "name" in d:
                    # 兼容部分模型的 function-calling 格式 [{"name":...,"parameters":...}]
                    items.append(PlanItem(
                        tool=str(d.get("name", "")),
                        params={k: str(v) for k, v in (d.get("parameters") or {}).items()},
                        reason="LLM 规划调用",
                    ))
    except Exception as e:
        logger.warning("⚠️  Planner LLM 解析失败 (%s)，降级到规则规划。原始: %s", e, raw)
        return rule_plan_items(agent, query, tools_map)

    # 过滤：只保留工具集中实际存在的工具
    valid: List[PlanItem] = []
    for item in items:
        if item.tool in tools_map:
            valid.append(item)
    return valid


def llm_plan_graph(agent, query: str, tools_map: Dict[str, Any], mem_prefix: str) -> List[Node]:
    """调用 Planner LLM 产出图节点，支持 depends_on 和 race_group。"""
    if not agent.cfg.is_real_llm():
        return rule_plan_nodes(agent, query, tools_map)

    tool_lines: List[str] = []
    for name, t in tools_map.items():
        p_descs: List[str] = []
        for p in getattr(t, "params", []) or []:
            req = "（必填）" if p.get("required") else ""
            p_descs.append(f"{p.get('name','')}({p.get('type','string')}){req}")
        params = ", ".join(p_descs) if p_descs else "无"
        tool_lines.append(f"- {name}: {getattr(t, 'description', '')} [参数: {params}]")

    plan_prompt = (
        "你是一个任务规划器。根据用户问题，从可用工具中选出需要调用的工具，并标注依赖关系。\n"
        "- 给每个工具调用分配唯一 id，如 n1、n2。\n"
        "- 如果工具 B 需要工具 A 的输出，则 B 的 depends_on 包含 A 的 id。\n"
        "- 如果两个工具功能类似，可设置相同 race_group，系统会并行竞速。\n"
        f"用户问题：{query}\n"
        f"可用工具：\n{chr(10).join(tool_lines)}\n"
        '请只输出 JSON 数组：[{"id":"n1","tool":"工具名","params":{},'
        '"reason":"原因","depends_on":[],"race_group":""}]。无需工具则输出 []。'
    )
    planner_base = "你是一个精准的任务规划器，只在必要时才调用工具。"
    if mem_prefix:
        planner_base = mem_prefix + "\n\n" + planner_base
    try:
        raw = agent.llm.chat([Message(role="user", content=plan_prompt)], system_prompt=planner_base)
        data = json.loads(_clean_json(raw))
    except Exception as e:
        logger.warning("⚠️  Planner LLM 图解析失败 (%s)，降级到规则规划。", e)
        return rule_plan_nodes(agent, query, tools_map)

    nodes: List[Node] = []
    if not isinstance(data, list):
        return nodes
    for idx, item in enumerate(data):
        if not isinstance(item, dict):
            continue
        tool = str(item.get("tool") or item.get("name") or "")
        if tool not in tools_map:
            continue
        params = item.get("params")
        if params is None:
            params = item.get("parameters")
        if not isinstance(params, dict):
            params = {}
        node_id = str(item.get("id") or f"n{idx + 1}")
        depends = item.get("depends_on") or []
        if not isinstance(depends, list):
            depends = []
        nodes.append(Node(
            id=node_id,
            type=NodeType.TOOL,
            name=str(item.get("reason") or "LLM 规划调用"),
            tool_name=tool,
            params={k: str(v) for k, v in params.items()},
            depends_on=[str(dep) for dep in depends],
            race_group=str(item.get("race_group") or ""),
        ))
    return nodes


def rule_plan_items(agent, query: str, tools_map: Dict[str, Any]) -> List[PlanItem]:
    """关键字规则降级规划（无真实 LLM 时使用）。"""
    q = query.lower()
    items: List[PlanItem] = []

    if "get_time" in tools_map:
        if ("时间" in q) or ("几点" in q) or ("现在" in q):
            params: Dict[str, str] = {}
            if "东京" in q:
                params["timezone"] = "Asia/Tokyo"
            items.append(PlanItem(tool="get_time", params=params, reason="查询当前时间"))

    if "get_weather" in tools_map:
        if "天气" in q:
            city = "北京"
            for c in ["东京", "北京", "上海", "广州", "深圳", "纽约", "伦敦"]:
                if c in q:
                    city = c
                    break
            items.append(PlanItem(tool="get_weather", params={"city": city}, reason=f"查询{city}天气"))

    if "search_web" in tools_map:
        if any(k in q for k in ["搜索", "查询", "介绍", "是什么", "怎么", "如何"]):
            items.append(PlanItem(tool="search_web", params={"query": query}, reason="搜索相关信息"))

    if "exec_command" in tools_map:
        if any(k in q for k in ["执行", "运行", "命令", "终端", "lscpu", "cpu", "磁盘", "内存", "系统信息"]):
            from .init_sandbox import extract_shell_command
            cmd = extract_shell_command(query)
            items.append(PlanItem(tool="exec_command", params={"command": cmd}, reason="执行终端命令"))

    if "rag_search" in tools_map:
        items.append(PlanItem(tool="rag_search", params={"query": query}, reason="检索个人知识库"))

    # MCP / 自定义工具：默认填首个必填参数
    builtins = {"get_time", "get_weather", "search_web", "rag_search", "exec_command"}
    for name, t in tools_map.items():
        if name in builtins:
            continue
        params: Dict[str, str] = {}
        for p in getattr(t, "params", []) or []:
            if p.get("required"):
                params[p.get("name", "")] = query
                break
        items.append(PlanItem(tool=name, params=params, reason=f"调用工具 {name}"))

    return items


def rule_plan_nodes(agent, query: str, tools_map: Dict[str, Any]) -> List[Node]:
    """关键字规则降级规划，返回图节点。"""
    if _looks_like_document_workflow(query):
        return [
            Node(
                id="n1",
                type=NodeType.SUBAGENT,
                name="研究资料",
                tool_name="research_agent",
                params={"goal": query},
            ),
            Node(
                id="n2",
                type=NodeType.SUBAGENT,
                name="撰写报告",
                tool_name="writer_agent",
                params={"goal": query},
                depends_on=["n1"],
            ),
            Node(
                id="n3",
                type=NodeType.SUBAGENT,
                name="审查报告",
                tool_name="review_agent",
                params={"goal": query},
                depends_on=["n2"],
            ),
            Node(
                id="n4",
                type=NodeType.SUBAGENT,
                name="保存文档",
                tool_name="doc_agent",
                params={"goal": query},
                depends_on=["n2", "n3"],
            ),
        ]
    items = rule_plan_items(agent, query, tools_map)
    nodes: List[Node] = []
    for idx, item in enumerate(items):
        race_group = "search" if item.tool in {"search_web", "rag_search"} else ""
        nodes.append(Node(
            id=f"n{idx + 1}",
            type=NodeType.TOOL,
            name=item.reason,
            tool_name=item.tool,
            params=item.params,
            depends_on=[],
            race_group=race_group,
        ))
    return nodes


def _looks_like_document_workflow(query: str) -> bool:
    q = (query or "").lower()
    return any(k in q for k in ["报告", "文档", "保存到文档库", "写成markdown", "markdown", "调研"]) and any(
        k in q for k in ["生成", "撰写", "写", "保存", "调研", "总结"]
    )


def _clean_json(raw: str) -> str:
    raw = (raw or "").strip()
    if "<|FunctionCallBegin|>" in raw:
        raw = raw[raw.index("<|FunctionCallBegin|>") + len("<|FunctionCallBegin|>"):]
        if "<|FunctionCallEnd|>" in raw:
            raw = raw[: raw.index("<|FunctionCallEnd|>")]
    raw = re.sub(r"^```json", "", raw)
    raw = re.sub(r"^```", "", raw)
    raw = re.sub(r"```$", "", raw)
    return raw.strip()
