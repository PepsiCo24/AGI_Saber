// replanner.go — Plan-and-ReAct 的 Replanner。
//
// 与 llmPlanGraph 一次性成图不同，Replanner 在图执行到中途/失败时被唤起：
//   - layerReplan: 一层执行完，把该层的观察和当前图状态给 LLM，问它「够不够、要不要追加」
//   - failureReplan: 单个节点失败后，问 LLM 是否需要用替代方案（如换搜索源、换参数）
//
// 输出仍是 planNode 数组，直接复用现有 toGraphNode → GraphRuntime 追加执行。
// 每次 replan 消耗一次 GraphMaxReplan 额度，防止 LLM 死循环追加。
package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agi-assistant/internal/domain/graph"
	"agi-assistant/internal/domain/tool"
	"agi-assistant/internal/infrastructure/llm"
	"agi-assistant/internal/pkg/logger"
)

// replanContext 打包 replan 所需的图状态快照，方便复用同一 prompt 模板
type replanContext struct {
	Query    string
	Reason   string // "layer_done" | "node_failed"
	Failed   *graph.Node
	Snapshot []nodeSnapshot // 当前图中所有节点的状态和观察
}

type nodeSnapshot struct {
	ID       string `json:"id"`
	Executor string `json:"executor"`
	Goal     string `json:"goal,omitempty"`
	Status   string `json:"status"`
	Result   string `json:"result,omitempty"`
	Error    string `json:"error,omitempty"`
}

// buildSnapshot 从 TaskGraph 抽取当前所有节点状态
func buildSnapshot(tg *graph.TaskGraph) []nodeSnapshot {
	snaps := make([]nodeSnapshot, 0, len(tg.Nodes))
	for id, n := range tg.Nodes {
		executor := n.ToolName
		if n.Type == graph.NodeTypeSubAgent {
			executor = n.AgentName
		}
		result := truncateStr(n.Result, 200)
		snaps = append(snaps, nodeSnapshot{
			ID: string(id), Executor: executor, Goal: n.Goal,
			Status: string(n.Status), Result: result, Error: n.Error,
		})
	}
	return snaps
}

// llmReplan 让 Planner LLM 基于当前图状态决定是否追加节点。
//
// 返回：
//   - 追加的 planNode 列表（可能为空 → 表示 LLM 认为不需要新增）
//   - LLM 不可用或解析失败时返回 nil, nil（外层视为「无需 replan」不影响主流程）
func (a *UnifiedAgent) llmReplan(
	ctx context.Context,
	rc replanContext,
	ts map[string]tool.Tool,
	memPrefix string,
	allowSubAgents bool,
) []*graph.Node {
	if !a.cfg.IsRealLLM() {
		return nil
	}

	snapJSON, _ := json.Marshal(rc.Snapshot)

	// 工具/子 Agent 描述与 llmPlanGraph 保持一致
	var toolLines []string
	for name, t := range ts {
		toolLines = append(toolLines, fmt.Sprintf("- %s: %s", name, t.Description))
	}
	// 子 Agent 段落：仅在 allowSubAgents 时暴露
	agentSection := ""
	if allowSubAgents {
		var agentLines []string
		for name, sa := range a.subagents.snapshot() {
			agentLines = append(agentLines, fmt.Sprintf("- %s: %s", name, sa.Description()))
		}
		agentSection = "\n\n可用子 Agent：\n" + strings.Join(agentLines, "\n")
	}

	failedHint := ""
	if rc.Failed != nil {
		fname := rc.Failed.ToolName
		if rc.Failed.Type == graph.NodeTypeSubAgent {
			fname = rc.Failed.AgentName
		}
		failedHint = fmt.Sprintf("失败节点：id=%s executor=%s error=%s\n请优先给出替代方案（换工具/换参数）。\n",
			rc.Failed.ID, fname, truncateStr(rc.Failed.Error, 200))
	}

	prompt := fmt.Sprintf(`你是一个任务重规划器。当前正在执行的任务图部分节点已产生结果，请判断是否需要追加新节点。

用户问题：%s
触发原因：%s
%s
当前图状态（JSON）：
%s

可用工具：
%s%s

判断规则：
- 如果现有观察已足够回答用户问题，返回 []（不追加）
- 如果需要基于已有观察做进一步查询/加工/汇总，追加节点
- 追加节点的 depends_on 可以指向图中任何已存在的节点 id
- 不要重复已有节点做过的事
- 每次最多追加 3 个节点

以 JSON 数组格式输出（结构与 planner 相同）：
[{"id":"r1","type":"tool","tool":"search_web","params":{...},"reason":"...","depends_on":["n2"],"race_group":""}]
只输出 JSON。`, rc.Query, rc.Reason, failedHint, string(snapJSON),
		strings.Join(toolLines, "\n"), agentSection)

	base := "你是一个精准的任务重规划器，只在现有观察不足时才追加节点，避免无意义的调用。"
	if memPrefix != "" {
		base = memPrefix + "\n\n" + base
	}

	raw := a.llm.ChatContextFast(ctx, base, []llm.Message{{Role: "user", Content: prompt}})
	if ctx.Err() != nil {
		return nil
	}

	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var nodes []planNode
	if err := json.Unmarshal([]byte(raw), &nodes); err != nil {
		logger.C(ctx).Warn("replanner parse failed", "err", err, "raw", truncateStr(raw, 200))
		return nil
	}

	// 复用 llmPlanGraph 的过滤逻辑：只保留合法工具/子 Agent，补齐缺省字段
	existing := map[string]bool{}
	for id := range buildSnapshotMap(rc.Snapshot) {
		existing[id] = true
	}
	var valid []*graph.Node
	for i, n := range nodes {
		if n.Type == "" {
			if n.Agent != "" {
				n.Type = string(graph.NodeTypeSubAgent)
			} else {
				n.Type = string(graph.NodeTypeTool)
			}
		}
		if n.Type == string(graph.NodeTypeSubAgent) {
			if !allowSubAgents {
				continue
			}
			if _, ok := a.subagents.get(n.Agent); !ok {
				continue
			}
		} else if _, ok := ts[n.Tool]; !ok {
			continue
		}
		// 保证追加节点 ID 不与已有节点冲突：无 id 或冲突时用 r{n} 前缀
		if n.ID == "" || existing[n.ID] {
			n.ID = fmt.Sprintf("r%d-%d", len(valid)+1, i)
		}
		if n.DependsOn == nil {
			n.DependsOn = []string{}
		}
		if n.Params == nil {
			n.Params = map[string]string{}
		}
		if n.Goal == "" {
			n.Goal = n.Reason
		}
		valid = append(valid, n.toGraphNode())
		existing[n.ID] = true
	}
	return valid
}

func buildSnapshotMap(snaps []nodeSnapshot) map[string]nodeSnapshot {
	m := make(map[string]nodeSnapshot, len(snaps))
	for _, s := range snaps {
		m[s.ID] = s
	}
	return m
}
