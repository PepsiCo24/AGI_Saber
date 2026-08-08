package sandbox

import (
	"context"
	"errors"
	"fmt"
)

// Executor 是沙箱执行器的统一接口。
//
// 具体实现（Docker / Local / Mock）在 infrastructure/sandbox 包中。
// domain 只持有接口，方便单测时替换为内存 Mock 而不引入 docker / os/exec 依赖。
type Executor interface {
	Exec(ctx context.Context, req ExecRequest) ExecResult
	Backend() string
	Available() bool
}

// Sandbox 封装 Validator + Executor + 审计回调。
//
// Exec 主入口的编排顺序：静态校验 → Block 拒绝 / Warn 等待确认 → Executor 执行 → 异步审计
type Sandbox struct {
	validator *Validator
	executor  Executor
	auditFn   func(ExecResult) // 审计回调（异步），由 agent 注入 Kafka 推送
}

// New 用已就绪的 Validator + Executor 组合一个 Sandbox。
//
// 工厂函数（按 backend 字符串选 Executor）放在 infrastructure/sandbox.NewSandbox，
// 因为 Executor 的具体实现属于基础设施层。
func New(validator *Validator, executor Executor) *Sandbox {
	return &Sandbox{validator: validator, executor: executor}
}

// SetAuditFn 注入审计回调（在 Exec 完成后异步触发）
func (s *Sandbox) SetAuditFn(fn func(ExecResult)) {
	s.auditFn = fn
}

// Backend 返回当前底层执行后端名称
func (s *Sandbox) Backend() string {
	return s.executor.Backend()
}

// Validator 暴露 Validator 供工具层做预检
func (s *Sandbox) Validator() *Validator {
	return s.validator
}

// Exec 主入口：先校验，再执行，最后审计
func (s *Sandbox) Exec(ctx context.Context, req ExecRequest) ExecResult {
	// 1. 安全校验
	validation := s.validator.Validate(req.Command)

	result := ExecResult{
		Command:    req.Command,
		Validation: validation,
		Backend:    s.executor.Backend(),
	}

	// 2. Block 级直接拒绝，不进入执行
	if validation.Level == RiskBlock {
		result.ExitCode = -1
		result.Stderr = "[拒绝执行] " + validation.Reason
		s.audit(result)
		return result
	}

	// 3. Warn 级要求 confirm
	if validation.Level == RiskWarn && !req.Confirm {
		result.ExitCode = -2
		result.Stderr = fmt.Sprintf("[需要确认] 该命令触发以下规则：%v；请重新调用并设置 confirm=true", validation.Violations)
		s.audit(result)
		return result
	}

	// 4. 进入沙箱执行
	execResult := s.executor.Exec(ctx, req)
	execResult.Command = req.Command
	execResult.Validation = validation
	execResult.Backend = s.executor.Backend()

	s.audit(execResult)
	return execResult
}

func (s *Sandbox) audit(r ExecResult) {
	if s.auditFn != nil {
		go s.auditFn(r)
	}
}

// ErrSandboxUnavailable 表示底层沙箱后端不可用
var ErrSandboxUnavailable = errors.New("sandbox backend unavailable")
