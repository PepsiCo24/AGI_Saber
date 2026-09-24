# sandbox.docker — 通过 docker CLI 在隔离容器内执行命令
import logging
import subprocess
import time
from typing import List

from .types import ExecRequest, ExecResult, SandboxConfig

logger = logging.getLogger(__name__)


def _truncate_bytes(data: bytes, max_bytes: int) -> tuple:
    """按字节数截断，返回 (截断后字符串, 是否被截断)。"""
    if not data:
        return "", False
    if max_bytes <= 0:
        return data.decode("utf-8", errors="replace"), False
    if len(data) <= max_bytes:
        return data.decode("utf-8", errors="replace"), False
    return data[:max_bytes].decode("utf-8", errors="replace"), True


class DockerSandbox:
    """通过 docker CLI 执行命令的沙箱后端

    关键安全约束（作为 docker run 参数传入）:
        --rm                  执行完自动清理容器
        --network none        禁用网络
        --read-only           根文件系统只读
        --tmpfs /tmp:size=...允许 /tmp 临时写入
        --memory / --cpus / --pids-limit  cgroup 资源硬限制
        --security-opt no-new-privileges  禁止权限提升
        --cap-drop ALL        放弃所有 Linux capabilities
    """

    def __init__(self, cfg: SandboxConfig):
        self.cfg = cfg
        self._available = self._probe()

    def backend(self) -> str:
        return "docker"

    def available(self) -> bool:
        return self._available

    def _probe(self) -> bool:
        """通过 docker version 检测 daemon 是否就绪（1.5s 超时）"""
        try:
            proc = subprocess.run(
                ["docker", "version", "--format", "{{.Server.Version}}"],
                capture_output=True,
                timeout=1.5,
                check=False,
            )
            return proc.returncode == 0
        except (FileNotFoundError, subprocess.TimeoutExpired, OSError):
            return False
        except Exception:
            return False

    def exec(self, ctx, req: ExecRequest) -> ExecResult:
        start = time.time()
        result = ExecResult(command=req.command, backend="docker")

        if not self._available:
            result.exit_code = -3
            result.stderr = "Docker 后端不可用"
            return result

        timeout = req.timeout if req.timeout > 0 else self.cfg.timeout
        if timeout <= 0:
            timeout = 30.0

        args = self._build_docker_args(req.command)

        try:
            proc = subprocess.run(
                ["docker"] + args,
                capture_output=True,
                timeout=timeout,
                check=False,
            )
            stdout, t1 = _truncate_bytes(proc.stdout, self.cfg.max_output_bytes)
            stderr, t2 = _truncate_bytes(proc.stderr, self.cfg.max_output_bytes)

            result.stdout = stdout
            result.stderr = stderr
            result.exit_code = proc.returncode
            result.truncated = t1 or t2
        except subprocess.TimeoutExpired as e:
            stdout, _ = _truncate_bytes(e.stdout or b"", self.cfg.max_output_bytes)
            stderr, _ = _truncate_bytes(e.stderr or b"", self.cfg.max_output_bytes)
            result.stdout = stdout
            result.stderr = stderr
            result.killed = True
            result.exit_code = -4
            if "超时" not in result.stderr:
                result.stderr += f"\n[超时] 执行超过 {timeout}s 被强制终止"
        except FileNotFoundError:
            result.exit_code = -3
            result.stderr = "Docker CLI 不可用"
        except Exception as e:
            result.exit_code = -5
            result.stderr += f"\n[沙箱内部错误] {e}"
        finally:
            result.duration = time.time() - start

        return result

    def _build_docker_args(self, command: str) -> List[str]:
        """构造 docker run 的完整参数列表"""
        args = [
            "run",
            "--rm",
            "-i",
            "--security-opt", "no-new-privileges",
            "--cap-drop", "ALL",
        ]

        if self.cfg.network_disabled:
            args += ["--network", "none"]
        if self.cfg.readonly_rootfs:
            args += ["--read-only", "--tmpfs", "/tmp:rw,size=64m"]
        if self.cfg.memory_limit_mb > 0:
            args += ["--memory", f"{self.cfg.memory_limit_mb}m"]
        if self.cfg.cpu_percent > 0:
            # docker 的 --cpus 接受小数核心数；50% → 0.5
            args += ["--cpus", f"{self.cfg.cpu_percent / 100.0:.2f}"]
        if self.cfg.max_pids > 0:
            args += ["--pids-limit", str(self.cfg.max_pids)]

        image = self.cfg.image or "ubuntu:22.04"
        args += [image, "sh", "-c", command]
        return args
