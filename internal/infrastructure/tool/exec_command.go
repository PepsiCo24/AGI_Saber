package toolimpl

import (
	"context"
	"fmt"
	"strings"
	"time"

	"agi-assistant/internal/domain/sandbox"
	"agi-assistant/internal/domain/tool"
)

// ExecCommandTool 创建一个调用 Sandbox 执行终端命令的工具
//
// 工具流程：
//  1. 接收 command 参数（字符串）和可选的 confirm 参数（布尔）
//  2. 委托给 Sandbox，Sandbox 内部串行执行校验 → 执行 → 审计
//  3. 将 ExecResult 格式化为人类可读的 Markdown 返回给 Agent
func ExecCommandTool(sb *sandbox.Sandbox) tool.Tool {
	return tool.Tool{
		Name: "exec_command",
		Description: "在隔离沙箱中执行终端命令。支持 ls/cat/echo/python3/node 等常见操作；" +
			"危险命令（rm -rf、sudo、网络外联等）会被自动拒绝；" +
			"涉及删除/安装/管道等中等风险命令需通过 confirm=true 二次确认。",
		Parameters: []tool.Param{
			{Name: "command", Type: "string", Description: "要执行的 Shell 命令（单条，禁止命令链）", Required: true},
			{Name: "confirm", Type: "boolean", Description: "对 warn 级命令的二次确认；默认 false", Required: false},
		},
		Execute: func(params map[string]interface{}) (string, error) {
			cmdStr, _ := params["command"].(string)
			if strings.TrimSpace(cmdStr) == "" {
				return "", fmt.Errorf("参数 command 不能为空")
			}

			// confirm 可能来自 bool 或 string
			var confirm bool
			switch v := params["confirm"].(type) {
			case bool:
				confirm = v
			case string:
				confirm = strings.EqualFold(v, "true") || v == "1"
			}

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			result := sb.Exec(ctx, sandbox.ExecRequest{Command: cmdStr, Confirm: confirm})
			return formatExecResult(result), nil
		},
	}
}

// formatExecResult 将 ExecResult 渲染为对 LLM 友好的字符串
func formatExecResult(r sandbox.ExecResult) string {
	var sb strings.Builder

	// 安全级别提示
	switch r.Validation.Level {
	case sandbox.RiskBlock:
		sb.WriteString(fmt.Sprintf("🛑 **命令被拒绝**\n原因：%s\n", r.Validation.Reason))
		return sb.String()
	case sandbox.RiskWarn:
		if r.ExitCode == -2 {
			sb.WriteString(fmt.Sprintf("⚠️ **命令需要确认**\n触发规则：%s\n"+
				"如确认无误，请在调用参数中加入 `confirm=true` 后重新执行。\n",
				strings.Join(r.Validation.Violations, "、")))
			return sb.String()
		}
		sb.WriteString(fmt.Sprintf("⚠️ 警告级命令已执行（触发规则：%s）\n",
			strings.Join(r.Validation.Violations, "、")))
	}

	// 执行结果
	sb.WriteString(fmt.Sprintf("**沙箱后端**: %s | **退出码**: %d | **耗时**: %v\n",
		r.Backend, r.ExitCode, r.Duration.Round(time.Millisecond)))

	if r.Killed {
		sb.WriteString("⏱ 因超时被强制终止\n")
	}
	if r.Truncated {
		sb.WriteString("✂️ 输出过长已被截断\n")
	}

	if r.Stdout != "" {
		sb.WriteString("\n**stdout**\n```\n")
		sb.WriteString(r.Stdout)
		if !strings.HasSuffix(r.Stdout, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("```\n")
	}
	if r.Stderr != "" {
		sb.WriteString("\n**stderr**\n```\n")
		sb.WriteString(r.Stderr)
		if !strings.HasSuffix(r.Stderr, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("```\n")
	}
	if r.Stdout == "" && r.Stderr == "" {
		sb.WriteString("（无输出）\n")
	}
	return sb.String()
}
