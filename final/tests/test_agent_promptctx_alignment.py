from types import SimpleNamespace

from internal.agent.agent import UnifiedAgent
from internal.agent.cancel import CancelRegistry
from internal.llm.llm import Message
from internal.memory.memory import Item, LongTerm, Preference, ShortTerm
from internal.promptctx import TaskMemBuffer, ToolStateTracker
from internal.tools.tools import Tool, ToolExecutor


class _PrefRepo:
    def load(self, _user_id):
        return {"city": "上海"}

    def save(self, *_args):
        pass


class _LtmRepo:
    def load(self):
        return []

    def save(self, *_args):
        return 1


class _Infra:
    def __init__(self):
        self.repo = SimpleNamespace(
            preference=_PrefRepo(),
            ltm=_LtmRepo(),
        )


class _Cfg:
    short_term_max_turns = 5
    long_term_top_k = 3
    max_retries = 1
    retry_delay_ms = 1
    graph_max_parallel = 2
    graph_race_timeout_ms = 30000
    graph_enable_racing = True

    def is_real_llm(self):
        return False


class _LLM:
    def chat(self, messages, system_prompt=""):
        return "llm:" + system_prompt + "|" + messages[-1].content


def _agent_shell():
    agent = object.__new__(UnifiedAgent)
    agent.cfg = _Cfg()
    agent.inf = _Infra()
    agent.llm = _LLM()
    agent.stm = ShortTerm(5)
    agent.ltm = LongTerm(agent.cfg, agent.inf)
    agent.ltm.items = [Item(content="用户喜欢中文回答", importance=0.9, category="preference")]
    agent.preference = Preference("u", agent.inf)
    agent.tool_executor = ToolExecutor([
        Tool(name="search_web", description="搜索", params=[{"name": "query", "type": "string"}], func=lambda p: "web"),
        Tool(name="rag_search", description="知识库", params=[{"name": "query", "type": "string"}], func=lambda p: "rag"),
    ])
    agent._cancel_registry = CancelRegistry()
    agent.rag = SimpleNamespace(
        loaded=True,
        query_with_history=lambda query, history: ("rag-answer", [{"content": query, "history": len(history)}]),
    )
    return agent


def test_agent_builds_prompt_context_bundle_and_prefix():
    agent = _agent_shell()

    agent._build_prompt_context()
    prefix = agent._build_context_prefix("你好", "chat")

    assert isinstance(agent.task_mem, TaskMemBuffer)
    assert isinstance(agent.tool_tracker, ToolStateTracker)
    assert "city: 上海" in prefix
    assert "用户喜欢中文回答" in prefix


def test_agent_rag_query_uses_history_aware_entrypoint():
    agent = _agent_shell()
    agent._build_prompt_context()
    agent.stm.add("user", "上一轮")

    answer, results = agent._run_rag_query("当前问题")

    assert answer == "rag-answer"
    assert results == [{"content": "当前问题", "history": 1}]


def test_agent_react_uses_graph_runtime_path():
    agent = _agent_shell()
    agent._build_prompt_context()

    answer, steps, task = agent._run_react_with_tools(
        "搜索 RAG 是什么",
        agent.tool_executor._tool_map,
        agent._build_context_prefix("搜索 RAG 是什么", "react"),
        [Message(role="user", content="搜索 RAG 是什么")],
        None,
    )

    assert task["graph"]["nodes"]
    assert "web" in answer or "rag" in answer
    assert steps
