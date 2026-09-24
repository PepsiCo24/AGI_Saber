# subagents - document-oriented agentic workflow workers.
import json
import re
import threading
from dataclasses import dataclass, field
from typing import Dict, List

from internal.document.library import DOCUMENT_SOURCE_AGENT, WriteRequest


@dataclass
class SubAgentTask:
    id: str = ""
    goal: str = ""
    query: str = ""
    upstream: Dict[str, str] = field(default_factory=dict)


class SubAgentRegistry:
    def __init__(self):
        self._agents = {}
        self._lock = threading.RLock()

    def register(self, agent) -> None:
        with self._lock:
            self._agents[agent.name()] = agent

    def get(self, name: str):
        with self._lock:
            return self._agents.get(name)

    def snapshot(self) -> Dict[str, object]:
        with self._lock:
            return dict(self._agents)


def register_builtin_subagents(agent) -> SubAgentRegistry:
    registry = SubAgentRegistry()
    for subagent in [
        ResearchAgent(agent),
        WriterAgent(agent),
        ReviewAgent(agent),
        DocAgent(agent),
    ]:
        registry.register(subagent)
    return registry


class ResearchAgent:
    def __init__(self, agent):
        self.agent = agent

    def name(self) -> str:
        return "research_agent"

    def description(self) -> str:
        return "Agentic RAG researcher: 多轮改写、知识库/搜索检索、证据整理。"

    def run(self, task: SubAgentTask) -> str:
        query = (task.goal or task.query).strip()
        observations: List[str] = []
        rag = getattr(self.agent, "rag", None)
        if rag is not None and getattr(rag, "loaded", False):
            answer, results = rag.query(query)
            observations.append(f"Query: {query}\nRAG Answer: {answer}")
            for item in results or []:
                content = (item.get("content") or "").strip() if isinstance(item, dict) else ""
                if content:
                    observations.append("- " + _first_runes(content, 180))
        elif hasattr(self.agent, "tool_executor"):
            tool = self.agent.tool_executor.snapshot().get("search_web")
            if tool is not None:
                observations.append(f"Query: {query}\nSearch Result: {tool.func({'query': query})}")
        if not observations:
            observations.append("未找到可用知识库或搜索结果。")
        return "## Research Findings\n\n" + "\n\n".join(observations)


class WriterAgent:
    def __init__(self, agent):
        self.agent = agent

    def name(self) -> str:
        return "writer_agent"

    def description(self) -> str:
        return "将上游研究结果整理为 Markdown 报告。"

    def run(self, task: SubAgentTask) -> str:
        material = _upstream_text(task)
        if not _is_real_llm(self.agent):
            return "# " + _safe_title(task.goal, task.query) + "\n\n" + material
        return self.agent._llm_generate(
            "你是 writer_agent。请把输入整理为清晰 Markdown 报告，包含摘要、分析、建议和下一步。",
            f"写作目标：{task.goal}\n\n材料：\n{material}",
        )


class ReviewAgent:
    def __init__(self, agent):
        self.agent = agent

    def name(self) -> str:
        return "review_agent"

    def description(self) -> str:
        return "检查报告结构、事实一致性、证据覆盖和风险。"

    def run(self, task: SubAgentTask) -> str:
        material = _upstream_text(task)
        if not _is_real_llm(self.agent):
            return "Review: 内容已整理；建议人工确认关键事实。"
        return self.agent._llm_generate(
            "你是 review_agent。请审查输入，输出问题清单、可信度和需要补证据的点。",
            material,
        )


class DocAgent:
    def __init__(self, agent):
        self.agent = agent

    def name(self) -> str:
        return "doc_agent"

    def description(self) -> str:
        return "将上游结果保存到本地文档库，并同步写入 RAG。"

    def run(self, task: SubAgentTask) -> str:
        content = _document_content(task) or task.query
        title = _document_title(content, task.goal, task.query)
        result = self.agent.write_document(
            WriteRequest(
                title=title,
                doc_type="report",
                source=DOCUMENT_SOURCE_AGENT,
                created_by=self.name(),
                content_md=content,
                summary=_first_runes(content, 180),
                metadata={
                    "sub_agent": self.name(),
                    "task_id": task.id,
                    "review": _first_runes(_upstream_by_agent(task, "review_agent"), 1200),
                },
            ),
            True,
        )
        return json.dumps(_jsonable(result), ensure_ascii=False, indent=2)


def _upstream_text(task: SubAgentTask) -> str:
    if not task.upstream:
        return task.query
    parts = []
    for key in sorted(task.upstream):
        parts.append(f"## {key}\n\n{task.upstream[key]}")
    return "\n\n".join(parts).strip()


def _document_content(task: SubAgentTask) -> str:
    writer = _upstream_by_agent(task, "writer_agent")
    if writer.strip():
        return _strip_markdown_fence(writer)
    for key in sorted(task.upstream):
        value = (task.upstream[key] or "").strip()
        if value:
            return _strip_markdown_fence(value)
    return task.query.strip()


def _upstream_by_agent(task: SubAgentTask, agent_name: str) -> str:
    for key in sorted(task.upstream):
        if agent_name in key:
            return task.upstream[key] or ""
    return ""


def _document_title(content: str, goal: str, query: str) -> str:
    for text in (query, goal):
        explicit = _explicit_requested_title(text)
        if explicit:
            return _first_runes(explicit, 80)
    heading = _markdown_title(content)
    if heading:
        return _first_runes(heading, 80)
    return _safe_title("", query or goal)


def _markdown_title(content: str) -> str:
    content = _strip_markdown_fence(content)
    fallback = ""
    in_fence = False
    for raw in content.splitlines():
        line = raw.strip()
        if line.startswith("```"):
            in_fence = not in_fence
            continue
        if in_fence:
            continue
        match = re.match(r"^(#{1,6})\s+(.+)$", line)
        if not match:
            continue
        title = match.group(2).strip("# \t*_`")
        if _is_generic_heading(title):
            continue
        if len(match.group(1)) == 1:
            return title
        if not fallback:
            fallback = title
    return fallback


def _explicit_requested_title(text: str) -> str:
    text = (text or "").strip()
    for marker, end in [("标题为《", "》"), ("标题是《", "》"), ("题为《", "》"), ('标题为"', '"'), ('标题是"', '"'), ('题为"', '"')]:
        start = text.find(marker)
        if start < 0:
            continue
        rest = text[start + len(marker):]
        stop = rest.find(end)
        if stop > 0:
            return rest[:stop].strip()
    return ""


def _strip_markdown_fence(text: str) -> str:
    trimmed = (text or "").strip()
    lines = trimmed.splitlines()
    if len(lines) >= 2 and lines[0].strip().startswith("```") and lines[-1].strip().startswith("```"):
        return "\n".join(lines[1:-1]).strip()
    return trimmed


def _is_generic_heading(title: str) -> bool:
    value = (title or "").strip().lower()
    return value in {"摘要", "分析", "建议", "下一步", "结论", "review", "findings", "evidence", "open questions", "research findings"}


def _safe_title(goal: str, query: str) -> str:
    title = (goal or query or "").strip()
    for prefix in ("生成", "撰写"):
        if title.startswith(prefix):
            title = title[len(prefix):].strip()
    return _first_runes(title or "Agent Report", 60)


def _first_runes(text: str, n: int) -> str:
    text = (text or "").strip()
    return text if len(text) <= n else text[:n] + "..."


def _is_real_llm(agent) -> bool:
    fn = getattr(getattr(agent, "cfg", None), "is_real_llm", None)
    return bool(callable(fn) and fn())


def _jsonable(value):
    if isinstance(value, dict):
        return {str(k): _jsonable(v) for k, v in value.items()}
    if isinstance(value, list):
        return [_jsonable(v) for v in value]
    if hasattr(value, "isoformat"):
        try:
            return value.isoformat()
        except Exception:
            pass
    if hasattr(value, "__dataclass_fields__"):
        from dataclasses import asdict

        return _jsonable(asdict(value))
    return value
