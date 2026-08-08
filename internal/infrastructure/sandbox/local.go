package sandboximpl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"agi-assistant/internal/domain/sandbox"
)

// LocalSandbox 在本机直接执行命令（无容器隔离），仅用于 Docker 不可用时的降级场景。
// 出于安全考虑，LocalSandbox 始终对命令做二次 Block 校验，且超时强制终止。
type LocalSandbox struct {
	cfg       sandbox.SandboxConfig
	validator *sandbox.Validator // 内置校验，只允许 Safe 级别
}

// NewLocalSandbox 创建本地执行器
func NewLocalSandbox(cfg sandbox.SandboxConfig) *LocalSandbox {
	// 本地模式：强制非白名单模式，但 block 规则仍然生效
	return &LocalSandbox{
		cfg:       cfg,
		validator: sandbox.NewValidator(sandbox.SecurityConfig{MaxCommandLength: cfg.MaxOutputBytes}),
	}
}

// Backend 返回后端名称
func (l *LocalSandbox) Backend() string { return "local" }

// Available 本地模式始终可用
func (l *LocalSandbox) Available() bool { return true }

// Exec 在本地环境直接执行命令（有超时限制）
func (l *LocalSandbox) Exec(ctx context.Context, req sandbox.ExecRequest) sandbox.ExecResult {
	start := time.Now()
	result := sandbox.ExecResult{Command: req.Command, Backend: "local"}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = l.cfg.Timeout
	}
	if timeout <= 0 {
		timeout = 15 * time.Second // 本地模式给更保守的超时
	}

	// 本地模式不允许 Warn 级别命令（无容器隔离，风险更高）
	v := l.validator.Validate(req.Command)
	if v.Level != sandbox.RiskSafe {
		result.ExitCode = -1
		result.Stderr = fmt.Sprintf("[本地模式拒绝] 只允许 safe 级别命令，当前: %s %v", v.Level, v.Violations)
		return result
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "sh", "-c", req.Command)
	if req.WorkspaceHostDir != "" {
		cmd.Dir = req.WorkspaceHostDir // 本地模式：直接在产物工作目录执行
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = limitWriter(&stdout, l.cfg.MaxOutputBytes)
	cmd.Stderr = limitWriter(&stderr, l.cfg.MaxOutputBytes)

	err := cmd.Run()
	result.Duration = time.Since(start)
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()

	if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
		result.Killed = true
		result.ExitCode = -4
		result.Stderr += fmt.Sprintf("\n[超时] 执行超过 %v 被终止", timeout)
		return result
	}

	var exitErr *exec.ExitError
	if err != nil {
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = -5
			result.Stderr += "\n" + err.Error()
		}
	}
	return result
}

// MockSandbox 返回固定结果，用于测试或沙箱完全不可用时占位
type MockSandbox struct{}

// NewMockSandbox 创建 Mock 执行器
func NewMockSandbox() *MockSandbox { return &MockSandbox{} }

// Backend 返回后端名称
func (m *MockSandbox) Backend() string { return "mock" }

// Available mock 始终可用
func (m *MockSandbox) Available() bool { return true }

// Exec 返回模拟输出
func (m *MockSandbox) Exec(_ context.Context, req sandbox.ExecRequest) sandbox.ExecResult {
	return sandbox.ExecResult{
		Command:  req.Command,
		Stdout:   fmt.Sprintf("[mock] 命令 %q 在模拟沙箱中执行（Docker 不可用）", req.Command),
		ExitCode: 0,
		Backend:  "mock",
		Duration: time.Millisecond,
	}
}
