// Package skill 定义「Skill 广场」的领域模型。
//
// Skill 是对 domain/tool.Tool 的上层封装：广场里的每个条目（Manifest）在被
// 用户安装后成为 Skill（带 user 维度 + 开关）。真正被 Agent 调用时，由
// application/skill.Service 把 Skill 转成 tool.Tool 并入 Planner 可见的工具集。
//
// 该包是纯 domain：不含 HTTP / DB / LLM 依赖，只描述数据形态与命名规则。
package skill

import (
	"regexp"
	"strings"

	"agi-assistant/internal/domain/tool"
)

// Source 标识 skill 的来源
type Source string

const (
	SourceBuiltin Source = "builtin" // 官方内置办公 skill
	SourceGitHub  Source = "github"  // 从 GitHub 广场爬取
	SourceMCP     Source = "mcp"     // 外部 MCP HTTP 端点
)

// Invocation 标识 skill 的调用方式
type Invocation string

const (
	InvokePrompt Invocation = "prompt" // 本地 LLM 按 PromptTemplate 执行
	InvokeMCP    Invocation = "mcp"    // 调用外部 HTTP 端点（复用 NewMCPTool）
)

// Manifest 是广场展示项（未安装态）。
type Manifest struct {
	ID             string       `json:"id"`          // builtin:meeting_minutes / github:owner/repo
	Name           string       `json:"name"`        // 展示名
	Description    string       `json:"description"` // 描述
	Category       string       `json:"category"`    // office / productivity / ...
	Source         Source       `json:"source"`      // builtin / github / mcp
	SourceURL      string       `json:"source_url,omitempty"`
	Stars          int          `json:"stars,omitempty"`
	Invocation     Invocation   `json:"invocation"`
	Endpoint       string       `json:"endpoint,omitempty"`        // 仅 InvokeMCP
	PromptTemplate string       `json:"prompt_template,omitempty"` // 含 {{input}} 占位
	Parameters     []tool.Param `json:"parameters,omitempty"`
}

// Skill 是已安装态（含用户维度与开关）。
type Skill struct {
	Manifest
	UserID  string `json:"user_id"`
	Enabled bool   `json:"enabled"`
}

// toolNameSanitizer 把 ID 中的非法字符替换为下划线（工具名只保留字母/数字/下划线）。
var toolNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

// ToolName 把 skill 映射为一个稳定、合法且带命名空间前缀的工具名，
// 避免与内置工具（get_time / search_web / rag_search 等）重名冲突。
//
// 例：
//
//	builtin:meeting_minutes -> skill_meeting_minutes
//	github:owner/repo       -> skill_owner_repo
func (m Manifest) ToolName() string {
	id := m.ID
	if i := strings.IndexByte(id, ':'); i >= 0 {
		id = id[i+1:] // 去掉 source 前缀
	}
	id = toolNameSanitizer.ReplaceAllString(id, "_")
	id = strings.Trim(id, "_")
	if id == "" {
		id = "unnamed"
	}
	return "skill_" + strings.ToLower(id)
}
