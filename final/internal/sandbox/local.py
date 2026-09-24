# sandbox.local — 本地直接执行后端（无容器隔离，仅用于降级场景）
import logging
import subprocess
import time

from .types import (
    ExecRequest,
    ExecResult,
    RISK_SAFE,
    SandboxConfig,
    SecurityConfig,
    ValidationResult,
)
from .validator import Validator

logger = logging.getLogger(__name__)


def _truncate_output(data: str, max_bytes: int) -> tuple:
    """按字节数截断，返回 (截断后字符串, 是否被截断)。"""
    if max_bytes <= 0:
        return data, False
    encoded = data.encode("utf-8", errors="replace")
    if len(encoded) <= max_bytes:
        return data, False
    return encoded[:max_bytes].decode("utf-8", errors="replace"), True


class LocalSandbox:
    """本地执行器（对应 Go 版 LocalSandbox）

    出于安全考虑，始终对命令做二次 block 校验，且超时强制终止。
    """

    def __init__(self, cfg: SandboxConfig):
        self.cfg = cfg
        # 本地模式：强制非白名单模式，但 block 规则仍然生效
        self._validator = Validator(SecurityConfig(max_command_length=cfg.max_output_bytes))

    def backend(self) -> str:
        return "local"

    def available(self) -> bool:
        return True

    def exec(self, ctx, req: ExecRequest) -> ExecResult:
        start = time.time()
        result = ExecResult(command=req.command, backend="local")

        timeout = req.timeout if req.timeout > 0 else self.cfg.timeout
        if timeout <= 0:
            timeout = 15.0  # 本地模式给更保守的超时

        # 本地模式不允许 warn 级命令
        v = self._validator.validate(req.command)
        if v.level != RISK_SAFE:
            result.exit_code = -1
            result.stderr = (
                f"[本地模式拒绝] 只允许 safe 级别命令，当前: {v.level} {v.violations}"
            )
            result.validation = v
            return result

        try:
            proc = subprocess.run(
                ["sh", "-c", req.command],
                capture_output=True,
                timeout=timeout,
                check=False,
            )
            stdout = proc.stdout.decode("utf-8", errors="replace") if proc.stdout else ""
            stderr = proc.stderr.decode("utf-8", errors="replace") if proc.stderr else ""

            stdout, t1 = _truncate_output(stdout, self.cfg.max_output_bytes)
            stderr, t2 = _truncate_output(stderr, self.cfg.max_output_bytes)

            result.stdout = stdout
            result.stderr = stderr
            result.exit_code = proc.returncode
            result.truncated = t1 or t2
        except subprocess.TimeoutExpired as e:
            stdout = (e.stdout or b"").decode("utf-8", errors="replace") if e.stdout else ""
            stderr = (e.stderr or b"").decode("utf-8", errors="replace") if e.stderr else ""
            result.stdout, _ = _truncate_output(stdout, self.cfg.max_output_bytes)
            result.stderr, _ = _truncate_output(stderr, self.cfg.max_output_bytes)
            result.killed = True
            result.exit_code = -4
            result.stderr += f"\n[超时] 执行超过 {timeout}s 被终止"
        except Exception as e:
            result.exit_code = -5
            result.stderr += f"\n{e}"
        finally:
            result.duration = time.time() - start
        return result


class MockSandbox:
    """Mock 后端：返回固定结果，用于测试或沙箱完全不可用时占位"""

    def backend(self) -> str:
        return "mock"

    def available(self) -> bool:
        return True

    def exec(self, ctx, req: ExecRequest) -> ExecResult:
        return ExecResult(
            command=req.command,
            stdout=f'[mock] 命令 "{req.command}" 在模拟沙箱中执行（Docker 不可用）',
            exit_code=0,
            backend="mock",
            duration=0.001,
        )
