# sandbox.validator — 命令安全校验（block/warn/safe 三级）
import re
from typing import List, Tuple

from .types import (
    Policy,
    RISK_BLOCK,
    RISK_SAFE,
    RISK_WARN,
    SecurityConfig,
    ValidationResult,
)


# ─────────────────────────────── 规则表 ──────────────────────────────────────

# (pattern, reason)
_BLOCK_RULES: List[Tuple[re.Pattern, str]] = [
    # 文件系统破坏
    (re.compile(r"rm\s+(-[rfRF]+\s+)?/"), "禁止删除根路径"),
    (re.compile(r"rm\s+-[rfRF]*r[fF]*\s"), "禁止 rm -rf"),
    (re.compile(r"\bdd\s+if="), "禁止 dd 设备写入"),
    (re.compile(r"\bmkfs\b"), "禁止格式化文件系统"),
    (re.compile(r">\s*/dev/(sd|hd|nvme|vd|xvd)"), "禁止写入块设备"),
    (re.compile(r":\s*\(\s*\)\s*\{.*:\s*\|"), "禁止 Fork 炸弹"),

    # 权限提升
    (re.compile(r"\bsudo\b"), "禁止 sudo"),
    (re.compile(r"\bsu\s"), "禁止 su"),
    (re.compile(r"\bchmod\s+[0-7]*7[0-7][0-7]\b"), "禁止 chmod 777"),
    (re.compile(r"\bchown\s+root\b"), "禁止变更为 root 属主"),

    # 系统控制
    (re.compile(r"\b(shutdown|reboot|halt|poweroff|init 0)\b"), "禁止系统关机/重启"),
    (re.compile(r"\bsystemctl\s+(stop|disable|mask)\b"), "禁止停止系统服务"),
    (re.compile(r"\biptables\b"), "禁止修改防火墙规则"),

    # Shell 注入风险（命令替换、进程替换）
    (re.compile(r"\$\("), "禁止命令替换 $()"),
    (re.compile(r"`"), "禁止反引号命令替换"),
    (re.compile(r"\beval\b"), "禁止 eval"),

    # 敏感文件访问
    (re.compile(r"/etc/(passwd|shadow|sudoers|ssh)"), "禁止访问系统凭证文件"),
    (re.compile(r"~/?\.(ssh|aws|docker|kube)/"), "禁止访问凭证目录"),

    # 路径遍历
    (re.compile(r"\.\./\.\./"), "禁止多级路径遍历"),

    # 网络（沙箱已断网，拦截外联意图）
    (re.compile(r"\b(curl|wget|nc|netcat|ncat)\s.*http"), "禁止网络外联（沙箱无网）"),
    (re.compile(r"\bssh\b"), "禁止 SSH 连接"),

    # 进程/资源滥用
    (re.compile(r"\bkillall\b"), "禁止 killall"),
    (re.compile(r"\bnohup\b"), "禁止 nohup 后台驻留"),
]

_WARN_RULES: List[Tuple[re.Pattern, str]] = [
    (re.compile(r"\brm\s"), "删除文件操作"),
    (re.compile(r">\s*\w"), "输出重定向（可能覆盖文件）"),
    (re.compile(r"\bkill\s"), "进程终止操作"),
    (re.compile(r"\bpip\s+install\b"), "安装 Python 包"),
    (re.compile(r"\bnpm\s+install\b"), "安装 Node 包"),
    (re.compile(r"\bapt(-get)?\s+install\b"), "安装系统包"),
    (re.compile(r"\bapk\s+add\b"), "安装 Alpine 包"),
    (re.compile(r";\s*\S"), "命令链（分号分隔）"),
    (re.compile(r"\|"), "管道符"),
    (re.compile(r"&&"), "条件命令链 &&"),
    (re.compile(r"\|\|"), "条件命令链 ||"),
]


# ─────────────────────────────── Validator ───────────────────────────────────

class Validator:
    """对命令做静态安全校验，输出 safe / warn / block 三种级别"""

    def __init__(self, cfg: SecurityConfig):
        self.cfg = cfg

    def validate(self, command: str) -> ValidationResult:
        # 1. 长度检查
        if self.cfg.max_command_length and len(command) > self.cfg.max_command_length:
            return ValidationResult(level=RISK_BLOCK, reason="命令超过最大长度限制")

        # 2. 空命令
        if not command.strip():
            return ValidationResult(level=RISK_BLOCK, reason="命令不能为空")

        # 3. 白名单模式
        if self.cfg.allowlist_mode and self.cfg.allowlist:
            tokens = command.split()
            first_word = tokens[0] if tokens else ""
            allowed = any(first_word.lower() == a.lower() for a in self.cfg.allowlist)
            if not allowed:
                return ValidationResult(
                    level=RISK_BLOCK,
                    reason=f'白名单模式：命令 "{first_word}" 未在允许列表中',
                )

        # 4. block 规则
        for pattern, reason in _BLOCK_RULES:
            if pattern.search(command):
                return ValidationResult(level=RISK_BLOCK, reason=reason)

        # 5. warn 规则：收集所有命中
        violations: List[str] = []
        for pattern, violation in _WARN_RULES:
            if pattern.search(command):
                violations.append(violation)
        if violations:
            return ValidationResult(level=RISK_WARN, violations=violations)

        return ValidationResult(level=RISK_SAFE)


def policy_snapshot() -> List[Policy]:
    """返回当前所有静态安全政策的只读快照（block + warn）。"""
    out: List[Policy] = []
    for pattern, reason in _BLOCK_RULES:
        out.append(Policy(level=RISK_BLOCK, pattern=pattern.pattern, reason=reason))
    for pattern, violation in _WARN_RULES:
        out.append(Policy(level=RISK_WARN, pattern=pattern.pattern, reason=violation))
    return out
