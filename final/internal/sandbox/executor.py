# sandbox.executor — Sandbox 顶层封装（Validator + Executor + 审计）
import logging
import threading
from typing import Callable, Optional

from .docker import DockerSandbox
from .local import LocalSandbox, MockSandbox
from .types import (
    ExecRequest,
    ExecResult,
    RISK_BLOCK,
    RISK_WARN,
    SandboxConfig,
    SecurityConfig,
)
from .validator import Validator

logger = logging.getLogger(__name__)


class SandboxUnavailableError(RuntimeError):
    """底层沙箱后端不可用"""


class Sandbox:
    """封装 Validator + Executor + 审计回调（对应 Go 版 Sandbox）"""

    def __init__(self, backend: str, sandbox_cfg: SandboxConfig, sec_cfg: SecurityConfig):
        self._validator = Validator(sec_cfg)
        self._audit_fn: Optional[Callable[[ExecResult], None]] = None

        if backend == "docker":
            ds = DockerSandbox(sandbox_cfg)
            if ds.available():
                self._executor = ds
            else:
                logger.warning("⚠️  Docker 不可用，沙箱降级到 mock 模式")
                self._executor = MockSandbox()
        elif backend == "local":
            self._executor = LocalSandbox(sandbox_cfg)
        elif backend == "mock":
            self._executor = MockSandbox()
        else:
            logger.warning("⚠️  未知沙箱后端 %r，使用 mock", backend)
            self._executor = MockSandbox()

    # ── 公共方法 ─────────────────────────────────────────────────────────────

    def set_audit_fn(self, fn: Callable[[ExecResult], None]) -> None:
        """注入审计回调（Exec 完成后异步触发）"""
        self._audit_fn = fn

    def backend(self) -> str:
        return self._executor.backend()

    def validator(self) -> Validator:
        return self._validator

    def exec(self, ctx, req: ExecRequest) -> ExecResult:
        """主入口：先校验，再执行，最后审计"""
        validation = self._validator.validate(req.command)

        result = ExecResult(
            command=req.command,
            validation=validation,
            backend=self._executor.backend(),
        )

        # block 级直接拒绝
        if validation.level == RISK_BLOCK:
            result.exit_code = -1
            result.stderr = "[拒绝执行] " + validation.reason
            self._audit(result)
            return result

        # warn 级要求 confirm
        if validation.level == RISK_WARN and not req.confirm:
            result.exit_code = -2
            result.stderr = (
                f"[需要确认] 该命令触发以下规则：{validation.violations}；"
                "请重新调用并设置 confirm=true"
            )
            self._audit(result)
            return result

        # 进入沙箱执行
        exec_result = self._executor.exec(ctx, req)
        exec_result.command = req.command
        exec_result.validation = validation
        exec_result.backend = self._executor.backend()

        self._audit(exec_result)
        return exec_result

    # ── 内部 ────────────────────────────────────────────────────────────────

    def _audit(self, r: ExecResult) -> None:
        if self._audit_fn is not None:
            try:
                threading.Thread(target=self._audit_fn, args=(r,), daemon=True).start()
            except Exception as e:  # 审计失败不能影响主流程
                logger.warning("审计回调启动失败: %s", e)


# 类型别名：Executor 对应任意提供 exec/backend/available 接口的对象
Executor = Sandbox
