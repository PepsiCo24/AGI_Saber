# agent — UnifiedAgent：整合全部 6 个阶段能力的核心调度器
#
# 路由策略（按优先级）：
#  1. ReAct + Harness — 复合查询（含 2+ 子需求，需多步推理）
#  2. Tool Agent      — 单一工具触发（时间 / 天气 / 搜索）
#  3. RAG             — 知识库已加载且无工具触发
#  4. Memory          — 存在用户偏好或长期记忆可利用
#  5. Chat            — 直接与 LLM 对话
import json
import logging
import re
import time
import threading
from dataclasses import dataclass, field
from typing import List, Optional, Dict, Any

import requests as http_requests

from config.config import APIConfig
from internal.infra.infra import Infrastructure
from internal.llm.llm import Client as LLMClient, Message
from internal.memory.memory import ShortTerm, LongTerm, Preference, Item
from internal.rag.rag import Engine as RAGEngine
from internal.tools.tools import Tool, CallResult, default_tools, ToolExecutor

logger = logging.getLogger(__name__)


# ─────────────────────────────── ReAct 数据结构 ──────────────────────────

class StepType:
    THOUGHT = "Thought"
    ACTION = "Action"
    OBSERVATION = "Observation"
    FINAL_ANSWER = "Final Answer"


@dataclass
class ReActStep:
    type: str
    content: str
    tool: str = ""
    params: Optional[Dict[str, str]] = None


# ─────────────────────────────── Harness 数据结构 ────────────────────────

class TaskStepStatus:
    PENDING = "pending"
    RUNNING = "running"
    COMPLETED = "completed"
    FAILED = "failed"


@dataclass
class TaskSnapshot:
    task_id: str
    steps: List[ReActStep]
    created_at: float = field(default_factory=lambda: time.time())


# ─────────────────────────────── UnifiedAgent ────────────────────────────

class UnifiedAgent:
    """整合所有能力的统一智能体"""

    def __init__(self, cfg: APIConfig, inf: Infrastructure):
        self.cfg = cfg
        self.inf = inf

        # 初始化核心组件
        self.llm = LLMClient(cfg)
        self.stm = ShortTerm(cfg.short_term_max_turns)
        self.ltm = LongTerm(cfg, inf)
        self.preference = Preference("default_user", inf)
        self.rag = RAGEngine(cfg, inf)
        self.tool_executor = ToolExecutor(default_tools())

        # 设置回调函数
        self.ltm.set_embed_fn(self.llm.embed)
        self.rag.set_generate_fn(self._llm_generate)

        # 从存储恢复长期记忆
        self.ltm.load_from_storage()

        # ReAct 相关
        self.max_iterations = cfg.max_iterations
        self.max_retries = cfg.max_retries

        logger.info("✅ UnifiedAgent 初始化完成")

    def _llm_generate(self, system_prompt: str, user_msg: str) -> str:
        """LLM 生成封装"""
        messages = [
            Message(role="system", content=system_prompt),
            Message(role="user", content=user_msg),
        ]
        return self.llm.chat(messages)

    # ─────────────────────────────── 智能路由 ────────────────────────────

    def route(self, user_input: str, use_rag: bool = False) -> str:
        """根据用户输入智能路由到不同处理模块"""
        # 1. 检查是否需要 ReAct（复杂任务）
        if self._needs_react(user_input):
            return self._react_flow(user_input)

        # 2. 检查是否需要工具调用
        tool_name = self._detect_tool(user_input)
        if tool_name:
            return self._tool_flow(tool_name, user_input)

        # 3. 检查 RAG 是否可用（根据 use_rag 参数或自动检测）
        if use_rag or (not use_rag is False and self.rag.loaded):
            answer, _ = self.rag.query(user_input)
            return answer

        # 4. 检查记忆
        memory_context = self._build_memory_context(user_input)
        if memory_context:
            return self._memory_flow(user_input, memory_context)

        # 5. 直接对话
        return self._chat_flow(user_input)

    def _needs_react(self, query: str) -> bool:
        """判断是否需要 ReAct 多步推理"""
        # 简单规则：包含多个子问题或需要多步操作
        indicators = ["先", "然后", "再", "接着", "步骤", "如何", "怎么"]
        count = sum(1 for ind in indicators if ind in query)
        return count >= 2

    def _detect_tool(self, query: str) -> Optional[str]:
        """检测应该调用的工具"""
        triggers = {
            "时间": "get_time",
            "几点": "get_time",
            "现在": "get_time",
            "天气": "get_weather",
            "搜索": "search_web",
            "查找": "search_web",
            "知识": "rag_search",
            "文档": "rag_search",
        }
        for trigger, tool_name in triggers.items():
            if trigger in query:
                return tool_name
        return None

    def _build_memory_context(self, query: str) -> str:
        """构建记忆上下文"""
        context_parts = []

        # 添加用户偏好
        prefs = self.preference.get_all()
        if prefs:
            context_parts.append(f"用户偏好: {json.dumps(prefs)}")

        # 添加长期记忆
        memories = self.ltm.recall(query, self.cfg.long_term_top_k)
        if memories:
            memory_text = "\n".join([f"- {m.content}" for m in memories])
            context_parts.append(f"相关记忆:\n{memory_text}")

        return "\n".join(context_parts)

    # ─────────────────────────────── 处理流程 ────────────────────────────

    def _react_flow(self, query: str) -> str:
        """ReAct 多步推理流程"""
        steps: List[ReActStep] = []
        task_id = f"task_{int(time.time())}"

        try:
            for iteration in range(self.max_iterations):
                # 1. 思考阶段
                thought = self._generate_thought(query, steps)
                steps.append(ReActStep(type=StepType.THOUGHT, content=thought))
                logger.info(f"💭 思考: {thought}")

                # 2. 检查是否完成
                if self._is_complete(thought):
                    final_answer = self._generate_final_answer(query, steps)
                    steps.append(ReActStep(type=StepType.FINAL_ANSWER, content=final_answer))
                    break

                # 3. 动作阶段
                action, tool_name, params = self._parse_action(thought)
                if not action:
                    final_answer = self._generate_final_answer(query, steps)
                    steps.append(ReActStep(type=StepType.FINAL_ANSWER, content=final_answer))
                    break

                steps.append(ReActStep(type=StepType.ACTION, content=action, tool=tool_name, params=params))
                logger.info(f"⚡ 动作: {tool_name}({params})")

                # 4. 执行工具并观察结果
                for retry in range(self.max_retries):
                    result = self.tool_executor.call(tool_name, params)
                    if result.success:
                        break
                    time.sleep(self.cfg.retry_delay_ms / 1000)

                observation = result.content if result.success else f"失败: {result.error}"
                steps.append(ReActStep(type=StepType.OBSERVATION, content=observation))
                logger.info(f"👁 观察: {observation}")

                # 保存快照
                self._save_snapshot(task_id, steps)

            else:
                final_answer = f"已达到最大迭代次数 ({self.max_iterations})，任务未完成。"
                steps.append(ReActStep(type=StepType.FINAL_ANSWER, content=final_answer))

            return self._format_react_response(steps)

        except Exception as e:
            logger.error("ReAct 流程失败: %s", e)
            return f"执行过程中发生错误: {str(e)}"

    def _generate_thought(self, query: str, steps: List[ReActStep]) -> str:
        """生成思考内容"""
        steps_str = "\n".join([f"{s.type}: {s.content}" for s in steps])
        prompt = f"""你是一个推理助手。基于以下对话历史和当前任务，给出下一步思考。

任务: {query}

历史步骤:
{steps_str}

请输出你的思考，格式为: 思考内容
"""
        messages = [
            Message(role="system", content="你是一个擅长推理的助手，能够分析问题并制定执行计划。"),
            Message(role="user", content=prompt),
        ]
        return self.llm.chat(messages)

    def _is_complete(self, thought: str) -> bool:
        """判断是否完成"""
        return any(keyword in thought for keyword in ["完成", "结束", "总结", "答案是"])

    def _parse_action(self, thought: str) -> tuple:
        """解析动作"""
        # 简单解析：查找工具调用模式
        match = re.search(r"(get_time|get_weather|search_web|rag_search)\s*\((.*?)\)", thought)
        if match:
            tool_name = match.group(1)
            params_str = match.group(2)
            params = {}
            for pair in params_str.split(","):
                if "=" in pair:
                    k, v = pair.split("=", 1)
                    params[k.strip()] = v.strip()
            return f"调用工具 {tool_name}", tool_name, params
        return "", "", {}

    def _generate_final_answer(self, query: str, steps: List[ReActStep]) -> str:
        """生成最终答案"""
        steps_str = "\n".join([f"{s.type}: {s.content}" for s in steps])
        prompt = f"""基于以下推理过程，给出最终答案。

任务: {query}

推理过程:
{steps_str}

请用自然语言总结最终答案。
"""
        messages = [
            Message(role="system", content="你是一个总结助手，能够基于推理过程给出简洁的最终答案。"),
            Message(role="user", content=prompt),
        ]
        return self.llm.chat(messages)

    def _save_snapshot(self, task_id: str, steps: List[ReActStep]):
        """保存任务快照"""
        snapshot = {
            "task_id": task_id,
            "steps": [
                {"type": s.type, "content": s.content, "tool": s.tool, "params": s.params}
                for s in steps
            ],
            "timestamp": time.time(),
        }
        self.inf.save_snapshot(task_id, json.dumps(snapshot))

    def _format_react_response(self, steps: List[ReActStep]) -> str:
        """格式化 ReAct 响应"""
        lines = []
        for s in steps:
            if s.type == StepType.THOUGHT:
                lines.append(f"💭 {s.content}")
            elif s.type == StepType.ACTION:
                lines.append(f"⚡ {s.tool}({s.params})")
            elif s.type == StepType.OBSERVATION:
                lines.append(f"👁 {s.content}")
            elif s.type == StepType.FINAL_ANSWER:
                lines.append(f"\n📝 最终答案:\n{s.content}")
        return "\n".join(lines)

    def _tool_flow(self, tool_name: str, user_input: str) -> str:
        """工具调用流程"""
        # 解析参数
        params = {}
        if tool_name == "get_weather":
            match = re.search(r"(天气|温度)\s*(\S+)", user_input)
            if match:
                params["city"] = match.group(2)
        elif tool_name in ["search_web", "rag_search"]:
            match = re.search(r"(搜索|查找|知识)\s*(.*)", user_input)
            if match:
                params["query"] = match.group(2)

        result = self.tool_executor.call(tool_name, params)
        if result.success:
            return result.content
        return f"工具调用失败: {result.error}"

    def _memory_flow(self, user_input: str, memory_context: str) -> str:
        """记忆增强对话流程"""
        prompt = f"""根据以下用户偏好和记忆信息，回答用户问题。

用户偏好和记忆:
{memory_context}

用户问题: {user_input}
"""
        messages = [
            Message(role="system", content="你是一个了解用户偏好的助手，请根据提供的上下文信息回答问题。"),
            Message(role="user", content=prompt),
        ]
        return self.llm.chat(messages)

    def _chat_flow(self, user_input: str) -> str:
        """直接对话流程"""
        # 添加到短期记忆
        self.stm.add("user", user_input)

        # 构建消息历史
        history = self.stm.get()
        messages = [Message(role=m["role"], content=m["content"]) for m in history]

        # 添加系统提示
        system_prompt = "你是一个友好的智能助手。"
        messages.insert(0, Message(role="system", content=system_prompt))

        # 调用 LLM
        response = self.llm.chat(messages)

        # 添加回复到短期记忆
        self.stm.add("assistant", response)

        # 异步更新长期记忆和偏好
        self._async_update_memory(user_input, response)

        return response

    def _async_update_memory(self, user_input: str, response: str):
        """异步更新记忆"""
        def update():
            # 更新长期记忆
            self.ltm.add(user_input)
            # 更新用户偏好
            messages = [{"role": "user", "content": user_input}]
            self.preference.update_from_messages(messages)
            # 记忆合并
            self.ltm.consolidate()

        threading.Thread(target=update, daemon=True).start()

    # ─────────────────────────────── RAG 操作 ────────────────────────────

    def rag_ingest(self, document: str) -> int:
        """向知识库添加文档"""
        return self.rag.ingest(document)

    def rag_query(self, question: str) -> tuple:
        """查询知识库"""
        return self.rag.query(question)

    # ─────────────────────────────── 工具管理 ────────────────────────────

    def get_tools(self) -> List[Dict[str, Any]]:
        """获取工具列表"""
        return self.tool_executor.get_tool_descriptions()

    def add_tool(self, tool: Tool):
        """添加工具"""
        self.tool_executor.add_tool(tool)
