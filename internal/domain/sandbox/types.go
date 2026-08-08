// Package sandbox 提供安全的终端命令执行能力：
//   - Validator：静态安全校验（Block/Warn/Safe 三级）
//   - Executor：在隔离环境中执行命令（Docker / Local / Mock 多后端）
//   - Audit：每条命令的执行结果记录
package sandbox

import "time"

// RiskLevel 是命令安全校验的风险级别
type RiskLevel string

const (
	RiskSafe  RiskLevel = "safe"  // 安全，可直接执行
	RiskWarn  RiskLevel = "warn"  // 需用户二次确认
	RiskBlock RiskLevel = "block" // 拒绝执行
)

// ValidationResult 是单次安全校验的结果
type ValidationResult struct {
	Level      RiskLevel `json:"level"`
	Violations []string  `json:"violations,omitempty"` // 触发的规则列表
	Reason     string    `json:"reason,omitempty"`     // 拒绝原因（Block 级别）
}

// ExecRequest 描述一次命令执行请求
type ExecRequest struct {
	Command string        // 待执行的 Shell 命令
	Timeout time.Duration // 超时时间，0 表示使用 Sandbox 默认值
	Confirm bool          // 对 Warn 级别命令的二次确认标记
	// WorkspaceHostDir 非空时把该宿主机目录绑定挂载到容器 /workspace（可写）
	// 并把工作目录设为 /workspace；用于 loop 产物物化（生成文件落宿主机桌面）。
	// Local 后端则直接把该目录作为命令执行的工作目录。
	WorkspaceHostDir string
}

// ExecResult 是一次命令执行的完整结果
type ExecResult struct {
	Command    string           `json:"command"`
	Validation ValidationResult `json:"validation"`
	Stdout     string           `json:"stdout"`
	Stderr     string           `json:"stderr"`
	ExitCode   int              `json:"exit_code"`
	Duration   time.Duration    `json:"duration"`
	Killed     bool             `json:"killed"`              // 是否因超时被强制终止
	Backend    string           `json:"backend"`             // "docker" | "local" | "mock"
	Truncated  bool             `json:"truncated,omitempty"` // 输出是否被截断
}

// SandboxConfig 控制单次执行的资源约束（与 config.APIConfig 解耦）
type SandboxConfig struct {
	Image           string        // Docker 镜像
	Timeout         time.Duration // 默认超时
	MaxOutputBytes  int           // stdout/stderr 单边最大字节数
	MemoryLimitMB   int           // 内存限制（MB）
	CPUPercent      int           // CPU 配额百分比（50 = 半核）
	MaxPIDs         int           // 最大进程数（防 fork 炸弹）
	NetworkDisabled bool          // 是否禁用网络
	ReadOnlyRootfs  bool          // 是否将根文件系统设为只读
}

// SecurityConfig 控制 Validator 的策略
type SecurityConfig struct {
	MaxCommandLength int      // 命令最大长度
	AllowlistMode    bool     // 是否启用白名单（true 时只有 Allowlist 中的命令可通过）
	Allowlist        []string // 白名单命令列表（取命令首词比较）
}
