# tools.exec_command — 调用 sandbox 执行终端命令的工具
import logging
from typing import Dict

from internal.sandbox import (
    ExecRequest,
    ExecResult,
    RISK_BLOCK,
    RISK_WARN,
    Sandbox,
)

logger = logging.getLogger(__name__)


def exec_command_tool_factory(sb: Sandbox):
    """创建 exec_command 工具的执行函数

    工具流程：
      1. 接收 command 参数（字符串）和可选 confirm 参数（布尔）
      2. 委托给 Sandbox（内部串行执行校验 → 执行 → 审计）
      3. 将 ExecResult 格式化为人类可读的 Markdown 返回
    """

    def _execute(params: Dict[str, object]) -> str:
        if sb is None:
            return "sandbox unavailable"

        cmd_str = params.get("command")
        if not isinstance(cmd_str, str) or not cmd_str.strip():
            return "[exec_command] 参数 command 不能为空"

        # confirm 可能是 bool 或 string
        confirm_raw = params.get("confirm", False)
        if isinstance(confirm_raw, bool):
            confirm = confirm_raw
        elif isinstance(confirm_raw, str):
            confirm = confirm_raw.lower() == "true" or confirm_raw == "1"
        else:
            confirm = bool(confirm_raw)

        try:
            result = sb.exec(None, ExecRequest(command=cmd_str, confirm=confirm))
        except Exception as e:
            logger.error("exec_command 执行失败: %s", e)
            return "sandbox unavailable"

        return format_exec_result(result)

    return _execute


def format_exec_result(r: ExecResult) -> str:
    """将 ExecResult 渲染为对 LLM 友好的字符串"""
    parts = []
    validation = r.validation

    if validation is not None:
        if validation.level == RISK_BLOCK:
            return f"🛑 **命令被拒绝**\n原因：{validation.reason}\n"

        if validation.level == RISK_WARN:
            if r.exit_code == -2:
                violations = "、".join(validation.violations)
                return (
                    f"⚠️ **命令需要确认**\n触发规则：{violations}\n"
                    "如确认无误，请在调用参数中加入 `confirm=true` 后重新执行。\n"
                )
            violations = "、".join(validation.violations)
            parts.append(f"⚠️ 警告级命令已执行（触发规则：{violations}）\n")

    duration_ms = int(round(r.duration * 1000))
    parts.append(
        f"**沙箱后端**: {r.backend} | **退出码**: {r.exit_code} | **耗时**: {duration_ms}ms\n"
    )

    if r.killed:
        parts.append("⏱ 因超时被强制终止\n")
    if r.truncated:
        parts.append("✂️ 输出过长已被截断\n")

    if r.stdout:
        suffix = "" if r.stdout.endswith("\n") else "\n"
        parts.append(f"\n**stdout**\n```\n{r.stdout}{suffix}```\n")
    if r.stderr:
        suffix = "" if r.stderr.endswith("\n") else "\n"
        parts.append(f"\n**stderr**\n```\n{r.stderr}{suffix}```\n")
    if not r.stdout and not r.stderr:
        parts.append("（无输出）\n")

    return "".join(parts)
