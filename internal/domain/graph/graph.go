// graph.go — 任务图（DAG）领域模型。
//
// 将 ReAct 从串行链路升级为可并行、可竞速的图结构调度。
// 核心概念：Node（执行单元）+ DependsOn（依赖边）+ RaceGroup（竞速组）。
// TaskGraph 通过拓扑排序按层调度，同层节点可并行执行。
package graph

import (
	"fmt"
	"strings"
)

// ─────────────────────────────── 类型常量 ──────────────────────────────────

type NodeID string

type NodeType string // "tool" | "think" | "aggregate"

const (
	NodeTypeTool      NodeType = "tool"
	NodeTypeSubAgent  NodeType = "sub_agent"
	NodeTypeThink     NodeType = "think"
	NodeTypeAggregate NodeType = "aggregate"
)

type NodeStatus string // "pending" | "running" | "done" | "failed" | "skipped" | "cancelled"

const (
	StatusPending   NodeStatus = "pending"
	StatusRunning   NodeStatus = "running"
	StatusDone      NodeStatus = "done"
	StatusFailed    NodeStatus = "failed"
	StatusSkipped   NodeStatus = "skipped"
	StatusCancelled NodeStatus = "cancelled"
)

// ─────────────────────────────── Node ──────────────────────────────────────

// Node 是任务图中的执行单元，对应一次工具调用、推理或数据聚合。
type Node struct {
	ID         NodeID            `json:"id"`
	Type       NodeType          `json:"type"`
	Name       string            `json:"name"`                 // Planner 给出的 reason
	ToolName   string            `json:"tool_name,omitempty"`  // 工具节点必填
	AgentName  string            `json:"agent_name,omitempty"` // 子 Agent 节点必填
	Goal       string            `json:"goal,omitempty"`       // 子 Agent 任务目标
	Params     map[string]string `json:"params,omitempty"`
	DependsOn  []NodeID          `json:"depends_on"`           // 入边：依赖哪些节点
	RaceGroup  string            `json:"race_group,omitempty"` // 空=独立；同 group=竞速
	Status     NodeStatus        `json:"status"`
	Result     string            `json:"result,omitempty"`
	Error      string            `json:"error,omitempty"`
	RetryCount int               `json:"retry_count"`
}

// ─────────────────────────────── TaskGraph ──────────────────────────────────

// TaskGraph 是有向无环图，通过拓扑排序决定节点执行顺序。
type TaskGraph struct {
	Nodes    map[NodeID]*Node    `json:"nodes"`
	AdjList  map[NodeID][]NodeID `json:"adj_list"`  // node → 下游节点
	InDegree map[NodeID]int      `json:"in_degree"` // 入度表
	levels   [][]NodeID          // 缓存的拓扑层级
}

// NewTaskGraph 从节点列表构建图，自动计算邻接表和入度。
func NewTaskGraph(nodes []*Node) *TaskGraph {
	tg := &TaskGraph{
		Nodes:    make(map[NodeID]*Node, len(nodes)),
		AdjList:  make(map[NodeID][]NodeID),
		InDegree: make(map[NodeID]int),
	}

	// 注册所有节点
	for _, n := range nodes {
		n.Status = StatusPending
		tg.Nodes[n.ID] = n
		tg.AdjList[n.ID] = nil
		tg.InDegree[n.ID] = 0
	}

	// 建立邻接表 + 入度
	for _, n := range nodes {
		for _, dep := range n.DependsOn {
			if _, ok := tg.Nodes[dep]; ok {
				tg.AdjList[dep] = append(tg.AdjList[dep], n.ID)
				tg.InDegree[n.ID]++
			}
		}
	}

	return tg
}

// TopologicalLevels 按「层」拓扑排序，同层节点无依赖可并行。
// 使用 Kahn 算法，若检测到环则返回错误。
func (tg *TaskGraph) TopologicalLevels() ([][]NodeID, error) {
	if tg.levels != nil {
		return tg.levels, nil
	}

	// 拷贝入度（不修改原图）
	inDeg := make(map[NodeID]int, len(tg.InDegree))
	for id, d := range tg.InDegree {
		inDeg[id] = d
	}

	var levels [][]NodeID
	processed := 0

	for {
		// 收集当前入度为 0 的节点
		var ready []NodeID
		for id, d := range inDeg {
			if d == 0 {
				ready = append(ready, id)
			}
		}
		if len(ready) == 0 {
			break
		}

		levels = append(levels, ready)
		processed += len(ready)

		// 从图中移除 ready 节点，更新下游入度
		for _, id := range ready {
			inDeg[id] = -1 // 标记已处理
			for _, downstream := range tg.AdjList[id] {
				inDeg[downstream]--
			}
		}
	}

	if processed != len(tg.Nodes) {
		return nil, fmt.Errorf("task graph has cycle: processed %d/%d nodes", processed, len(tg.Nodes))
	}

	tg.levels = levels
	return levels, nil
}

// ReadyNodes 返回当前入度为 0 的可执行节点。
func (tg *TaskGraph) ReadyNodes() []NodeID {
	var ready []NodeID
	for id, d := range tg.InDegree {
		if d == 0 && tg.Nodes[id].Status == StatusPending {
			ready = append(ready, id)
		}
	}
	return ready
}

// MarkDone 标记节点完成，更新下游入度，返回新就绪的节点。
func (tg *TaskGraph) MarkDone(id NodeID) []NodeID {
	tg.InDegree[id] = -1 // 标记已完成
	var newlyReady []NodeID
	for _, downstream := range tg.AdjList[id] {
		tg.InDegree[downstream]--
		if tg.InDegree[downstream] == 0 && tg.Nodes[downstream].Status == StatusPending {
			newlyReady = append(newlyReady, downstream)
		}
	}
	return newlyReady
}

// RaceGroups 按 race_group 对节点分组。返回 map[raceGroup][]NodeID。
func (tg *TaskGraph) RaceGroups() map[string][]NodeID {
	groups := make(map[string][]NodeID)
	for id, n := range tg.Nodes {
		if n.RaceGroup != "" {
			groups[n.RaceGroup] = append(groups[n.RaceGroup], id)
		}
	}
	return groups
}

// Validate 检测图的合法性：环检测 + 悬空依赖。
func (tg *TaskGraph) Validate() error {
	// 悬空依赖检查
	for _, n := range tg.Nodes {
		for _, dep := range n.DependsOn {
			if _, ok := tg.Nodes[dep]; !ok {
				return fmt.Errorf("node %s depends on nonexistent node %s", n.ID, dep)
			}
		}
	}
	// 环检测（通过拓扑排序）
	_, err := tg.TopologicalLevels()
	return err
}

// SetNodeStatus 安全更新节点状态。
func (tg *TaskGraph) SetNodeStatus(id NodeID, status NodeStatus) {
	if n, ok := tg.Nodes[id]; ok {
		n.Status = status
	}
}

// SetNodeResult 安全更新节点结果。
func (tg *TaskGraph) SetNodeResult(id NodeID, result string) {
	if n, ok := tg.Nodes[id]; ok {
		n.Result = result
	}
}

// SetNodeError 安全更新节点错误。
func (tg *TaskGraph) SetNodeError(id NodeID, errMsg string) {
	if n, ok := tg.Nodes[id]; ok {
		n.Error = errMsg
	}
}

// SetNodeRetryCount 安全更新重试计数。
func (tg *TaskGraph) SetNodeRetryCount(id NodeID, count int) {
	if n, ok := tg.Nodes[id]; ok {
		n.RetryCount = count
	}
}

// SuccessfulResults 返回所有成功节点的结果列表（用于 Generator LLM）。
func (tg *TaskGraph) SuccessfulResults() []string {
	var results []string
	for _, n := range tg.Nodes {
		if n.Status == StatusDone && n.Result != "" {
			name := n.ToolName
			if n.Type == NodeTypeSubAgent {
				name = n.AgentName
			}
			results = append(results, fmt.Sprintf("[%s] %s", name, n.Result))
		}
	}
	return results
}

// AddNodes 动态追加节点到已有图（Replanner 用）。
//
// 追加的节点：
//   - ID 冲突则跳过（避免覆盖已完成的节点结果）
//   - depends_on 指向不存在的节点会被静默丢弃（保证追加节点可执行）
//   - 缓存的 levels 会被清空，下次 TopologicalLevels 重新分层
//
// 返回实际追加的节点 ID 列表。调用方应在 Validate 通过后再让 Runtime 继续执行。
func (tg *TaskGraph) AddNodes(nodes []*Node) []NodeID {
	var added []NodeID
	for _, n := range nodes {
		if _, exists := tg.Nodes[n.ID]; exists {
			continue
		}
		n.Status = StatusPending
		// 过滤悬空依赖：只保留指向图中已存在节点的依赖
		var validDeps []NodeID
		for _, dep := range n.DependsOn {
			if _, ok := tg.Nodes[dep]; ok {
				validDeps = append(validDeps, dep)
			}
		}
		n.DependsOn = validDeps

		tg.Nodes[n.ID] = n
		tg.AdjList[n.ID] = nil
		tg.InDegree[n.ID] = 0
		for _, dep := range validDeps {
			// 若依赖节点已完成（InDegree=-1），新节点入度不再增加，可立即就绪
			if tg.InDegree[dep] >= 0 {
				tg.InDegree[n.ID]++
			}
			tg.AdjList[dep] = append(tg.AdjList[dep], n.ID)
		}
		added = append(added, n.ID)
	}
	// 拓扑层缓存失效
	tg.levels = nil
	return added
}

// Summary 返回图的可读摘要。
func (tg *TaskGraph) Summary() string {
	levels, err := tg.TopologicalLevels()
	if err != nil {
		return fmt.Sprintf("graph invalid: %v", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "graph: %d nodes, %d levels\n", len(tg.Nodes), len(levels))
	for i, level := range levels {
		var names []string
		for _, id := range level {
			n := tg.Nodes[id]
			name := n.ToolName
			if n.Type == NodeTypeSubAgent {
				name = n.AgentName
			}
			names = append(names, fmt.Sprintf("%s(%s)", n.ID, name))
		}
		fmt.Fprintf(&b, "  L%d: %s\n", i, strings.Join(names, ", "))
	}
	return b.String()
}
