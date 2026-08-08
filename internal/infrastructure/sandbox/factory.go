// Package sandboximpl 提供 sandbox.Executor 的具体实现：Docker / Local / Mock。
//
// domain/sandbox 只持有抽象（Validator + Executor 接口 + Sandbox 编排）。
// 具体执行器涉及 docker CLI / os/exec 等基础设施依赖，按 Clean Architecture 下沉到这里。
package sandboximpl

import (
	"agi-assistant/internal/domain/sandbox"
	"agi-assistant/internal/pkg/logger"
)

// NewSandbox 工厂函数：按 backend 字符串组装 domain.Sandbox。
//
// backend 取值：
//   - "docker"  使用 DockerSandbox；docker daemon 不可用时降级为 mock
//   - "local"   使用 LocalSandbox（无容器隔离，只允许 Safe 级命令）
//   - "mock"    使用 MockSandbox（用于测试 / 完全不可用时占位）
//   - 其他      日志告警后用 mock 兜底
func NewSandbox(backend string, sandboxCfg sandbox.SandboxConfig, secCfg sandbox.SecurityConfig) *sandbox.Sandbox {
	validator := sandbox.NewValidator(secCfg)

	var exec sandbox.Executor
	switch backend {
	case "docker":
		ds := NewDockerSandbox(sandboxCfg)
		if ds.Available() {
			exec = ds
		} else {
			logger.L().Warn("docker unavailable, sandbox falling back to mock")
			exec = NewMockSandbox()
		}
	case "local":
		exec = NewLocalSandbox(sandboxCfg)
	case "mock":
		exec = NewMockSandbox()
	default:
		logger.L().Warn("unknown sandbox backend, using mock", "backend", backend)
		exec = NewMockSandbox()
	}

	return sandbox.New(validator, exec)
}
