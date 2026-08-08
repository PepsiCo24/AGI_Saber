package sandbox

import (
	"regexp"
	"strings"
)

// ─────────────────────────────── 规则表 ──────────────────────────────────────

// blockRule 定义一条 Block 级规则
type blockRule struct {
	pattern *regexp.Regexp
	reason  string
}

// warnRule 定义一条 Warn 级规则
type warnRule struct {
	pattern   *regexp.Regexp
	violation string
}

var blockRules = []blockRule{
	// 文件系统破坏
	{regexp.MustCompile(`rm\s+(-[rfRF]+\s+)?/`), "禁止删除根路径"},
	{regexp.MustCompile(`rm\s+-[rfRF]*r[fF]*\s`), "禁止 rm -rf"},
	{regexp.MustCompile(`\bdd\s+if=`), "禁止 dd 设备写入"},
	{regexp.MustCompile(`\bmkfs\b`), "禁止格式化文件系统"},
	{regexp.MustCompile(`>\s*/dev/(sd|hd|nvme|vd|xvd)`), "禁止写入块设备"},
	{regexp.MustCompile(`:\s*\(\s*\)\s*\{.*:\s*\|`), "禁止 Fork 炸弹"},

	// 权限提升
	{regexp.MustCompile(`\bsudo\b`), "禁止 sudo"},
	{regexp.MustCompile(`\bsu\s`), "禁止 su"},
	{regexp.MustCompile(`\bchmod\s+[0-7]*7[0-7][0-7]\b`), "禁止 chmod 777"},
	{regexp.MustCompile(`\bchown\s+root\b`), "禁止变更为 root 属主"},

	// 系统控制
	{regexp.MustCompile(`\b(shutdown|reboot|halt|poweroff|init 0)\b`), "禁止系统关机/重启"},
	{regexp.MustCompile(`\bsystemctl\s+(stop|disable|mask)\b`), "禁止停止系统服务"},
	{regexp.MustCompile(`\biptables\b`), "禁止修改防火墙规则"},

	// Shell 注入风险（命令替换、进程替换）
	{regexp.MustCompile(`\$\(`), "禁止命令替换 $()"},
	{regexp.MustCompile("`"), "禁止反引号命令替换"},
	{regexp.MustCompile(`\beval\b`), "禁止 eval"},

	// 敏感文件访问
	{regexp.MustCompile(`/etc/(passwd|shadow|sudoers|ssh)`), "禁止访问系统凭证文件"},
	{regexp.MustCompile(`~/?\.(ssh|aws|docker|kube)/`), "禁止访问凭证目录"},

	// 路径遍历
	{regexp.MustCompile(`\.\./\.\./`), "禁止多级路径遍历"},

	// 网络（沙箱已断网，拦截外联意图）
	{regexp.MustCompile(`\b(curl|wget|nc|netcat|ncat)\s.*http`), "禁止网络外联（沙箱无网）"},
	{regexp.MustCompile(`\bssh\b`), "禁止 SSH 连接"},

	// 进程/资源滥用
	{regexp.MustCompile(`\bkillall\b`), "禁止 killall"},
	{regexp.MustCompile(`\bnohup\b`), "禁止 nohup 后台驻留"},
}

var warnRules = []warnRule{
	{regexp.MustCompile(`\brm\s`), "删除文件操作"},
	{regexp.MustCompile(`>\s*\w`), "输出重定向（可能覆盖文件）"},
	{regexp.MustCompile(`\bkill\s`), "进程终止操作"},
	{regexp.MustCompile(`\bpip\s+install\b`), "安装 Python 包"},
	{regexp.MustCompile(`\bnpm\s+install\b`), "安装 Node 包"},
	{regexp.MustCompile(`\bapt(-get)?\s+install\b`), "安装系统包"},
	{regexp.MustCompile(`\bapk\s+add\b`), "安装 Alpine 包"},
	{regexp.MustCompile(`;\s*\S`), "命令链（分号分隔）"},
	{regexp.MustCompile(`\|`), "管道符"},
	{regexp.MustCompile(`&&`), "条件命令链 &&"},
	{regexp.MustCompile(`\|\|`), "条件命令链 ||"},
}

// ─────────────────────────────── Validator ───────────────────────────────────

// Validator 对命令做静态安全校验，输出 Safe / Warn / Block 三种级别
type Validator struct {
	cfg SecurityConfig
}

// NewValidator 创建 Validator
func NewValidator(cfg SecurityConfig) *Validator {
	return &Validator{cfg: cfg}
}

// Validate 对命令字符串执行全套安全规则检查
func (v *Validator) Validate(command string) ValidationResult {
	// 1. 长度检查
	if len(command) > v.cfg.MaxCommandLength {
		return ValidationResult{
			Level:  RiskBlock,
			Reason: "命令超过最大长度限制",
		}
	}

	// 2. 空命令
	if strings.TrimSpace(command) == "" {
		return ValidationResult{Level: RiskBlock, Reason: "命令不能为空"}
	}

	// 3. 白名单模式（只允许明确列出的命令首词）
	if v.cfg.AllowlistMode && len(v.cfg.Allowlist) > 0 {
		firstWord := strings.Fields(command)[0]
		allowed := false
		for _, a := range v.cfg.Allowlist {
			if strings.EqualFold(firstWord, a) {
				allowed = true
				break
			}
		}
		if !allowed {
			return ValidationResult{
				Level:  RiskBlock,
				Reason: "白名单模式：命令 \"" + firstWord + "\" 未在允许列表中",
			}
		}
	}

	// 4. Block 规则：任意一条命中即拒绝
	for _, rule := range blockRules {
		if rule.pattern.MatchString(command) {
			return ValidationResult{
				Level:  RiskBlock,
				Reason: rule.reason,
			}
		}
	}

	// 5. Warn 规则：收集所有命中项
	var violations []string
	for _, rule := range warnRules {
		if rule.pattern.MatchString(command) {
			violations = append(violations, rule.violation)
		}
	}
	if len(violations) > 0 {
		return ValidationResult{Level: RiskWarn, Violations: violations}
	}

	return ValidationResult{Level: RiskSafe}
}

// ─────────────────────────────── 政策快照 ─────────────────────────────────

// Policy 是单条静态安全政策的描述（用于 runtime 装配 Constraints 槽位）
type Policy struct {
	Level   RiskLevel `json:"level"`
	Pattern string    `json:"pattern"` // 原始正则字面量（仅用于展示）
	Reason  string    `json:"reason"`  // 中文说明
}

// PolicySnapshot 返回当前所有静态安全政策（Block + Warn）的只读快照，
// 供 Schema-driven 装配机制在 Constraints 槽位中向 LLM 描述硬性约束。
// 规则定义在本文件 blockRules / warnRules 中。
func PolicySnapshot() []Policy {
	out := make([]Policy, 0, len(blockRules)+len(warnRules))
	for _, r := range blockRules {
		out = append(out, Policy{Level: RiskBlock, Pattern: r.pattern.String(), Reason: r.reason})
	}
	for _, r := range warnRules {
		out = append(out, Policy{Level: RiskWarn, Pattern: r.pattern.String(), Reason: r.violation})
	}
	return out
}
