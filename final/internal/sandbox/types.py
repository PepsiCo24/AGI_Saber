# sandbox.types — Sandbox 数据结构与配置（与 main 分支 Go 版字段对齐）
"""
sandbox 包提供安全的终端命令执行能力：
  - Validator：静态安全校验（block/warn/safe 三级）
  - Executor：在隔离环境中执行命令（docker / local / mock 多后端）
  - Audit：每条命令的执行结果记录
"""
from dataclasses import dataclass, field
from typing import List, Optional


# ── 风险级别（与 Go 版 RiskLevel 字符串对齐） ────────────────────────────────
RISK_SAFE = "safe"
RISK_WARN = "warn"
RISK_BLOCK = "block"


@dataclass
class ValidationResult:
    """单次安全校验的结果"""
    level: str = RISK_SAFE
    violations: List[str] = field(default_factory=list)
    reason: str = ""

    def to_dict(self) -> dict:
        return {
            "level": self.level,
            "violations": list(self.violations),
            "reason": self.reason,
        }


@dataclass
class ExecRequest:
    """命令执行请求"""
    command: str = ""
    timeout: float = 0.0  # 秒；0 表示使用 sandbox 默认值
    confirm: bool = False  # 对 warn 级命令的二次确认


@dataclass
class ExecResult:
    """命令执行的完整结果"""
    command: str = ""
    validation: Optional[ValidationResult] = None
    stdout: str = ""
    stderr: str = ""
    exit_code: int = 0
    duration: float = 0.0  # 秒
    killed: bool = False
    backend: str = ""
    truncated: bool = False

    def to_dict(self) -> dict:
        return {
            "command": self.command,
            "validation": self.validation.to_dict() if self.validation else None,
            "stdout": self.stdout,
            "stderr": self.stderr,
            "exit_code": self.exit_code,
            "duration": self.duration,
            "killed": self.killed,
            "backend": self.backend,
            "truncated": self.truncated,
        }


@dataclass
class SandboxConfig:
    """单次执行的资源约束（与 config.APIConfig 解耦）"""
    image: str = "ubuntu:22.04"
    timeout: float = 30.0  # 秒
    max_output_bytes: int = 65536
    memory_limit_mb: int = 256
    cpu_percent: int = 50
    max_pids: int = 64
    network_disabled: bool = True
    readonly_rootfs: bool = True


@dataclass
class SecurityConfig:
    """Validator 的策略"""
    max_command_length: int = 500
    allowlist_mode: bool = False
    allowlist: List[str] = field(default_factory=list)


@dataclass
class Policy:
    """单条静态安全政策（用于装配 Constraints 槽位）"""
    level: str = ""
    pattern: str = ""
    reason: str = ""
