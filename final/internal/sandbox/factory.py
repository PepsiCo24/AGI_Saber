from config.config import APIConfig

from .executor import Sandbox
from .types import SandboxConfig, SecurityConfig


def create_sandbox(cfg: APIConfig) -> Sandbox:
    sandbox_cfg = SandboxConfig(
        image=cfg.sandbox_image,
        timeout=(cfg.sandbox_timeout_ms or 0) / 1000.0,
        max_output_bytes=cfg.sandbox_max_output,
        memory_limit_mb=cfg.sandbox_memory_mb,
        cpu_percent=cfg.sandbox_cpu_percent,
        max_pids=cfg.sandbox_max_pids,
        network_disabled=cfg.sandbox_net_disabled,
        readonly_rootfs=cfg.sandbox_readonly,
    )
    security_cfg = SecurityConfig(
        max_command_length=cfg.sec_max_cmd_length,
        allowlist_mode=cfg.sec_allowlist_mode,
        allowlist=list(cfg.sec_allowlist or []),
    )
    return Sandbox(cfg.sandbox_backend, sandbox_cfg, security_cfg)
