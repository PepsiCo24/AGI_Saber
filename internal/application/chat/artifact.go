// artifact.go — 正常 loop 的「沙箱产物物化」。
//
// 设计目标（对齐需求）：正常 loop（非知识增强）每次执行前准备一个绑定挂载到宿主机
// 桌面的沙箱工作目录；loop 用 web_search / skill / 子 Agent 收集内容后，由 LLM 判断
// 是否需要交付文件（报告 / 作业等），需要则把内容写进工作目录——该目录即沙箱 /workspace，
// 文件通过绑定挂载出现在宿主机桌面。
//
// 产出路径（可靠性优先，规避 shell 转义 / 命令长度上限）：
//  1. Go 侧把 LLM 生成的正文写入工作目录下的暂存文件；
//  2. 若 docker 沙箱可用，则在容器内执行 `cp 暂存 目标`（在 /workspace 内完成，属 Safe 级命令），
//     实现"在沙箱里产出、挂载到桌面"；
//  3. 沙箱不可用 / mock / 执行后目标缺失时，Go 侧直接落盘兜底。
package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agi-assistant/internal/domain/sandbox"
	"agi-assistant/internal/infrastructure/llm"
	"agi-assistant/internal/pkg/logger"
)

// prepareArtifactWorkspace 在产物根目录下按 {userID}/{taskID} 创建工作目录。
// 返回空字符串表示不可用（未配置根目录或创建失败），调用方据此跳过产物物化。
func (a *UnifiedAgent) prepareArtifactWorkspace(userID, taskID string) string {
	base := a.cfg.SandboxArtifactHostDir
	if base == "" {
		return ""
	}
	if userID == "" {
		userID = "anonymous"
	}
	ws := filepath.Join(base, sanitizePathSeg(userID), sanitizePathSeg(taskID))
	if err := os.MkdirAll(ws, 0o755); err != nil {
		logger.L().Warn("artifact workspace mkdir failed", "dir", ws, "err", err)
		return ""
	}
	return ws
}

// produceArtifact 在最终答案生成后，决定是否把答案落成交付文件。
// content 通常就是 Generator 生成的最终答案（报告正文），避免二次生成与巨型 JSON 解析。
// 返回一个可渲染的 ReActStep（供 runReAct 追加）；无产物时返回 nil。
func (a *UnifiedAgent) produceArtifact(
	ctx context.Context,
	query string,
	content string,
	hostWS string,
	onEvent func(StreamEvent),
) *ReActStep {
	if hostWS == "" || strings.TrimSpace(content) == "" {
		return nil
	}

	needs, filename := a.decideArtifact(ctx, query)
	if !needs {
		return nil
	}
	filename = sanitizeFilename(filename)
	absPath := filepath.Join(hostWS, filename)

	// 产物步骤作为一个可渲染节点推送
	emit(onEvent, "node_start", map[string]string{"id": "artifact", "tool": "produce_artifact"})
	emit(onEvent, "step", ReActStep{Type: StepThought, Content: "生成交付文件"})
	emit(onEvent, "step", ReActStep{
		Type: StepAction, Content: "在沙箱生成文件 " + filename, Tool: "produce_artifact",
	})

	produced, backend := a.materialize(ctx, hostWS, filename, content)

	var obs string
	switch {
	case produced && backend != "":
		obs = fmt.Sprintf("已在沙箱(%s)内生成并挂载到宿主机：%s", backend, absPath)
	case produced:
		obs = "已生成文件：" + absPath
	default:
		obs = "产出文件失败：" + absPath
	}

	step := ReActStep{Type: StepObservation, Content: obs, Tool: "produce_artifact"}
	emit(onEvent, "step", step)
	emit(onEvent, "node_done", map[string]string{
		"id": "artifact", "tool": "produce_artifact", "status": "done", "path": absPath,
	})
	return &step
}

// materialize 把正文写到工作目录的目标文件。返回 (是否成功, 实际执行后端)。
// 优先在沙箱容器内 cp（在 /workspace 内完成 → 挂载到宿主机桌面）；否则 Go 落盘兜底。
func (a *UnifiedAgent) materialize(ctx context.Context, hostWS, filename, content string) (bool, string) {
	stage := ".agi_stage_" + fmt.Sprint(time.Now().UnixNano())
	stagePath := filepath.Join(hostWS, stage)
	finalPath := filepath.Join(hostWS, filename)

	// 1) Go 写暂存文件（避免 shell 转义与命令长度上限）
	if err := os.WriteFile(stagePath, []byte(content), 0o644); err != nil {
		logger.C(ctx).Warn("artifact stage write failed", "err", err)
		return false, ""
	}
	defer os.Remove(stagePath)

	// 2) docker 沙箱可用 → 在容器内 cp（Safe 级命令，走 /workspace 挂载）
	backend := ""
	if a.sandbox != nil {
		backend = a.sandbox.Backend()
		if backend == "docker" {
			cmd := fmt.Sprintf("cp %s %s", shellQuoteArg(stage), shellQuoteArg(filename))
			res := a.sandbox.Exec(ctx, sandbox.ExecRequest{
				Command: cmd, WorkspaceHostDir: hostWS, Timeout: 20 * time.Second,
			})
			if res.ExitCode == 0 {
				if _, err := os.Stat(finalPath); err == nil {
					return true, backend
				}
			}
			logger.C(ctx).Warn("sandbox cp did not produce file, falling back to local write",
				"exit", res.ExitCode, "stderr", res.Stderr)
		}
	}

	// 3) 兜底：Go 直接落盘到挂载目录（等价于写入桌面）
	if err := os.WriteFile(finalPath, []byte(content), 0o644); err != nil {
		logger.C(ctx).Warn("artifact final write failed", "err", err)
		return false, ""
	}
	return true, "" // 空 backend 表示本地落盘
}

// decideArtifact 判断是否需要交付文件并给出文件名。
//
// 只让 LLM 输出「极小 JSON」（needs_file + filename），避免把大段正文塞进 JSON
// 导致解析失败（LLM 常把正文里的换行输出成真实换行，破坏 JSON）。
// LLM 不可用或解析失败时回落到关键词启发式。
func (a *UnifiedAgent) decideArtifact(ctx context.Context, query string) (bool, string) {
	// 关键词启发式（同时作为 LLM 解析失败的兜底）
	heuristic := func() (bool, string) {
		q := query
		for _, kw := range []string{"报告", "作业", "文档", "方案", "总结", "计划书", "文案", "简历", "论文", "写一份", "写一篇", "生成一份", "生成一个文件", "导出"} {
			if strings.Contains(q, kw) {
				return true, safeTitle("", query) + ".md"
			}
		}
		return false, ""
	}

	if !a.cfg.IsRealLLM() {
		return heuristic()
	}

	prompt := fmt.Sprintf(`判断用户是否需要一个可交付的成文文件（报告/作业/文档/方案等）。
只输出极简 JSON，不要正文、不要解释：
{"needs_file":true,"filename":"报告.md"}
或
{"needs_file":false}

用户请求：%s`, query)

	raw := a.llm.ChatContextFast(ctx, "你严格只输出一行极简 JSON。", []llm.Message{{Role: "user", Content: prompt}})
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	// 只截取第一个 {...}，容忍尾随说明
	if i := strings.Index(raw, "{"); i >= 0 {
		if j := strings.LastIndex(raw, "}"); j >= i {
			raw = raw[i : j+1]
		}
	}

	var dec struct {
		NeedsFile bool   `json:"needs_file"`
		Filename  string `json:"filename"`
	}
	if err := json.Unmarshal([]byte(raw), &dec); err != nil {
		logger.C(ctx).Warn("artifact decide parse failed, using heuristic", "raw", truncateStr(raw, 120))
		return heuristic()
	}
	if dec.NeedsFile && strings.TrimSpace(dec.Filename) == "" {
		dec.Filename = safeTitle("", query) + ".md"
	}
	return dec.NeedsFile, dec.Filename
}

// sanitizeFilename 只取基名并过滤路径分隔符，防目录穿越；空则给默认名。
func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	name = filepath.Base(name)
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.TrimLeft(name, ".")
	if name == "" {
		name = fmt.Sprintf("artifact_%d.md", time.Now().Unix())
	}
	return name
}

// sanitizePathSeg 清洗目录段（userID / taskID），避免穿越。
func sanitizePathSeg(seg string) string {
	seg = strings.TrimSpace(seg)
	seg = strings.ReplaceAll(seg, "/", "_")
	seg = strings.ReplaceAll(seg, "\\", "_")
	seg = strings.ReplaceAll(seg, "..", "_")
	if seg == "" {
		seg = "default"
	}
	return seg
}

// shellQuoteArg 用单引号包裹参数，转义内部单引号（cp 的文件名参数用）。
func shellQuoteArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
