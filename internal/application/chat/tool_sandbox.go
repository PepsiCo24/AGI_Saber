// init_sandbox.go — 沙箱初始化与 shell 命令解析。
package chat

import (
	"encoding/json"
	"time"

	"agi-assistant/internal/domain/sandbox"
	sandboximpl "agi-assistant/internal/infrastructure/sandbox"
	"agi-assistant/internal/pkg/logger"
)

// initSandbox 初始化命令执行沙箱（供 loop 产物物化使用）。
// 裁剪后不再注册 exec_command 工具——沙箱不再作为对话工具暴露，
// 只在正常 loop 的产物物化阶段用于「在容器内生成文件并落到宿主机桌面」。
func (a *UnifiedAgent) initSandbox() {
	if !a.cfg.SandboxEnabled {
		logger.L().Info("sandbox disabled (config.sandbox.enabled=false), artifact production will fall back to local write")
		return
	}

	sbCfg := sandbox.SandboxConfig{
		Image:           a.cfg.SandboxImage,
		Timeout:         time.Duration(a.cfg.SandboxTimeoutMs) * time.Millisecond,
		MaxOutputBytes:  a.cfg.SandboxMaxOutput,
		MemoryLimitMB:   a.cfg.SandboxMemoryMB,
		CPUPercent:      a.cfg.SandboxCPUPercent,
		MaxPIDs:         a.cfg.SandboxMaxPIDs,
		NetworkDisabled: a.cfg.SandboxNetDisabled,
		ReadOnlyRootfs:  a.cfg.SandboxReadOnly,
	}
	secCfg := sandbox.SecurityConfig{
		MaxCommandLength: a.cfg.SecMaxCmdLength,
		AllowlistMode:    a.cfg.SecAllowlistMode,
		Allowlist:        a.cfg.SecAllowlist,
	}

	sb := sandboximpl.NewSandbox(a.cfg.SandboxBackend, sbCfg, secCfg)

	// 注入审计回调：将每条命令执行结果发送到 Kafka
	sb.SetAuditFn(func(r sandbox.ExecResult) {
		event, _ := json.Marshal(map[string]interface{}{
			"command":     r.Command,
			"level":       string(r.Validation.Level),
			"exit_code":   r.ExitCode,
			"duration_ms": r.Duration.Milliseconds(),
			"backend":     r.Backend,
			"killed":      r.Killed,
			"truncated":   r.Truncated,
			"reason":      r.Validation.Reason,
			"violations":  r.Validation.Violations,
		})
		a.repos.events.Publish("sandbox.exec", string(event))
	})

	a.sandbox = sb
	logger.L().Info("sandbox ready (artifact production backend)", "backend", sb.Backend())
}

// Sandbox 暴露沙箱实例，供 HTTP handler 或前端查询状态
func (a *UnifiedAgent) Sandbox() *sandbox.Sandbox { return a.sandbox }
