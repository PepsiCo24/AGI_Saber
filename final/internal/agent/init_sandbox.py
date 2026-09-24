# init_sandbox — 沙箱初始化与 shell 命令解析
#
# 对应 Go 版 internal/agent/init_sandbox.go：
#   - init_sandbox：构造 Sandbox 实例 + 注册 exec_command 工具 + 审计回调
#   - extract_shell_command：从用户自然语言中提取实际的 shell 命令
#
# Sandbox 不可用（如 docker 缺失）时降级为 None，不阻塞 agent 启动。
import json
import logging

logger = logging.getLogger(__name__)


def init_sandbox(agent):
    """初始化命令执行沙箱并注册 exec_command 工具。

    失败时把 agent.sandbox 置 None，agent 仍可运行其他工具。
    """
    if not agent.cfg.sandbox_enabled:
        logger.info("ℹ️  沙箱未启用（config.sandbox.enabled=false），跳过 exec_command 工具")
        agent.sandbox = None
        return

    try:
        from internal.sandbox.factory import create_sandbox

        sb = create_sandbox(agent.cfg)

        # 审计：将每条命令执行结果发送到 Kafka（字段对齐 Go 版 initSandbox）
        def _audit(r):
            try:
                validation = getattr(r, "validation", None)
                event = {
                    "command": getattr(r, "command", ""),
                    "level": getattr(validation, "level", "") if validation else "",
                    "exit_code": getattr(r, "exit_code", None),
                    "duration_ms": int(getattr(r, "duration_ms", 0) or 0),
                    "backend": getattr(r, "backend", ""),
                    "killed": getattr(r, "killed", False),
                    "truncated": getattr(r, "truncated", False),
                    "reason": getattr(validation, "reason", "") if validation else "",
                    "violations": list(getattr(validation, "violations", []) or []) if validation else [],
                }
                _publish_event(agent, "sandbox.exec", json.dumps(event, ensure_ascii=False))
            except Exception as e:
                logger.warning("⚠️  sandbox 审计回调失败: %s", e)

        if hasattr(sb, "set_audit_fn"):
            try:
                sb.set_audit_fn(_audit)
            except Exception:
                pass

        agent.sandbox = sb

        # 注册 exec_command 工具
        try:
            from internal.tools.tools import build_exec_command_tool
            tool = build_exec_command_tool(sb)
            if tool is not None:
                agent.tool_executor.add_tool(tool)
                logger.info("🛡️  沙箱已就绪，exec_command 工具已注册")
        except Exception as e:
            logger.warning("⚠️  注册 exec_command 失败: %s", e)
    except Exception as e:
        logger.warning("⚠️  沙箱初始化失败: %s（agent 继续运行）", e)
        agent.sandbox = None


def _publish_event(agent, event_type: str, payload: str):
    inf = getattr(agent, "inf", None)
    repo = getattr(inf, "repo", None) if inf is not None else None
    events = getattr(repo, "events", None)
    if events is not None and hasattr(events, "publish"):
        try:
            events.publish(event_type, payload)
            return
        except Exception:
            pass
    logger.info("📋 [event-fallback] %s: %s", event_type, payload)


def extract_shell_command(query: str) -> str:
    """从用户自然语言查询中提取实际的 shell 命令。

    简单提取：去掉常见中文前缀/后缀。
    """
    q = query
    for prefix in ("执行", "运行", "请执行", "请运行", "帮我执行", "帮我运行"):
        if q.startswith(prefix):
            q = q[len(prefix):]
            break
    for suffix in ("命令", "查看CPU信息", "查看内存信息", "查看磁盘信息", "查看系统信息", "查看信息"):
        if q.endswith(suffix):
            q = q[: -len(suffix)]
            break
    q = q.strip()
    return q if q else query
