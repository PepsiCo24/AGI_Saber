// runtime.go — GraphRuntime：任务图的并行调度 + 竞速执行引擎。
//
// 核心调度策略：
//   - 拓扑排序按层调度，同层节点可并行执行
//   - 同 RaceGroup 的节点竞速执行（First-success-wins）
//   - 信号量控制最大并行度
//   - 支持 context 取消 + 中断恢复
package chat

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"agi-assistant/internal/domain/graph"
	"agi-assistant/internal/domain/promptctx"
	"agi-assistant/internal/domain/tool"
	"agi-assistant/internal/pkg/logger"
)

// ─────────────────────────────── GraphConfig ──────────────────────────────────

// GraphConfig 控制图运行时行为
type GraphConfig struct {
	MaxParallel   int  `yaml:"max_parallel"`    // 最大并行数，默认 2
	RaceTimeoutMs int  `yaml:"race_timeout_ms"` // 竞速组超时（毫秒），默认 30000
	EnableRacing  bool `yaml:"enable_racing"`   // 是否启用竞速，默认 true
	// Plan-and-ReAct
	ReplanEnabled  bool // 每层执行后触发 LLM replan
	MaxReplan      int  // 单次任务 replan 上限
	ReplanOnFailed bool // 节点失败时触发局部 replan
}

// DefaultGraphConfig 返回默认配置
func DefaultGraphConfig() GraphConfig {
	return GraphConfig{MaxParallel: 2, RaceTimeoutMs: 30000, EnableRacing: true, MaxReplan: 2}
}

// subAgentStepTimeout 是子 Agent 节点的执行超时下限。
// 子 Agent 内部会串行做多次 LLM 调用，工具级（秒级）step_timeout 不适用，
// 这里给一个宽松上限，避免被误判超时。
const subAgentStepTimeout = 5 * time.Minute

// ─────────────────────────────── GraphResult ──────────────────────────────────

// GraphResult 是图执行完毕后的汇总结果
type GraphResult struct {
	Observations   []string // 所有成功节点的观察
	NodeResults    map[graph.NodeID]NodeResult
	Interrupted    bool
	InterruptedAt  graph.NodeID // 被中断时正在执行的节点
	InterruptedMsg string
}

// NodeResult 单节点的执行结果
type NodeResult struct {
	Status graph.NodeStatus
	Result string
	Error  string
}

// ─────────────────────────────── raceGroup ──────────────────────────────────

// raceGroup 是同一层中属于同 RaceGroup 的节点集合
type raceGroup struct {
	RaceGroup string
	NodeIDs   []graph.NodeID
}

// ─────────────────────────────── GraphRuntime ──────────────────────────────────

// GraphRuntime 负责按拓扑层级并行调度执行任务图
type GraphRuntime struct {
	graph   *graph.TaskGraph
	agent   *UnifiedAgent
	cfg     GraphConfig
	tools   map[string]tool.Tool // 允许调用的工具集
	sem     chan struct{}        // 并发信号量
	mu      sync.Mutex
	results map[graph.NodeID]string
	errors  map[graph.NodeID]string
	task    *TaskState        // 共享 TaskState，用于快照 / 中断恢复
	onEvent func(StreamEvent) // SSE 事件回调（nil = 静默模式）

	// Plan-and-ReAct 需要的重规划上下文
	query      string
	memPrefix  string
	replanUsed int // 已消耗的 replan 次数

	// allowSubAgents 控制 replan 是否可引入子 Agent（普通 loop=false）
	allowSubAgents bool
}

// NewGraphRuntime 创建图运行时
func NewGraphRuntime(tg *graph.TaskGraph, agent *UnifiedAgent, cfg GraphConfig, tools map[string]tool.Tool, task *TaskState, onEvent func(StreamEvent)) *GraphRuntime {
	if cfg.MaxParallel <= 0 {
		cfg.MaxParallel = 2
	}
	return &GraphRuntime{
		graph:   tg,
		agent:   agent,
		cfg:     cfg,
		tools:   tools,
		sem:     make(chan struct{}, cfg.MaxParallel),
		results: make(map[graph.NodeID]string),
		errors:  make(map[graph.NodeID]string),
		task:    task,
		onEvent: onEvent,
	}
}

// SetReplanContext 注入 replan 时需要的用户 query 和记忆前缀。
// 由 runReAct 在创建 runtime 后调用；不设置则 replan 拿不到 memPrefix，仍能工作。
func (rt *GraphRuntime) SetReplanContext(query, memPrefix string) {
	rt.query = query
	rt.memPrefix = memPrefix
}

// Execute 执行整个任务图。
//
// 迭代式调度：每一轮取当前入度为 0 的节点作为一层并行执行；执行完 →
// 若开启 Replan 且额度未耗尽 → 让 LLM 决定是否追加节点 → 下一轮再取 ReadyNodes。
// 直到没有 pending 节点为止（正常完成）或 ctx 取消（中断）。
//
// 与旧实现"预先算 levels 一次遍历"的区别：允许运行期动态注入节点。
func (rt *GraphRuntime) Execute(ctx context.Context) *GraphResult {
	// 首次校验（悬空依赖 + 环）
	if err := rt.graph.Validate(); err != nil {
		logger.C(ctx).Warn("graph runtime: validation failed", "err", err)
		return &GraphResult{InterruptedMsg: fmt.Sprintf("图校验失败: %v", err)}
	}

	// 推送初始图快照
	if rt.onEvent != nil {
		levels, _ := rt.graph.TopologicalLevels()
		rt.onEvent(NewStreamEvent("graph_ready", map[string]interface{}{
			"levels": levels,
			"nodes":  rt.graph.Nodes,
		}))
	}

	layerIdx := 0
	for {
		if ctx.Err() != nil {
			return rt.buildInterruptedResult(ctx, fmt.Sprintf("在第 %d 层执行前被中断", layerIdx))
		}

		// 取当前所有 pending 且入度为 0 的节点作为本层
		level := rt.graph.ReadyNodes()
		if len(level) == 0 {
			break // 没有可执行节点 → 图跑完
		}

		// 稳定顺序（NodeID 字典序），避免 map 遍历带来的竞速组分组抖动
		sort.Slice(level, func(i, j int) bool { return level[i] < level[j] })

		// 按 race_group 分组：同组竞速，其余独立并行
		groups := rt.groupByRace(level)
		var wg sync.WaitGroup
		for _, g := range groups {
			if g.RaceGroup != "" && rt.cfg.EnableRacing {
				wg.Add(1)
				go rt.raceGroup(ctx, g, &wg)
			} else {
				for _, nodeID := range g.NodeIDs {
					wg.Add(1)
					go rt.executeNode(ctx, nodeID, &wg)
				}
			}
		}
		wg.Wait()

		// 把已完成节点从入度表中"移除"，让下游进入 ready 队列
		for _, id := range level {
			n := rt.graph.Nodes[id]
			// 只有终态节点才推进下游（Done / Skipped / Failed / Cancelled 都算终态）
			if n.Status == graph.StatusPending || n.Status == graph.StatusRunning {
				continue
			}
			rt.graph.MarkDone(id)
		}

		if ctx.Err() != nil {
			return rt.buildInterruptedResult(ctx, fmt.Sprintf("在第 %d 层执行后被中断", layerIdx))
		}

		// 持久化快照
		rt.agent.saveSnapshot(rt.task)

		// ── Plan-and-ReAct: 层间 replan ─────────────────────────────
		if rt.cfg.ReplanEnabled && rt.replanUsed < rt.cfg.MaxReplan {
			added := rt.tryReplan(ctx, "layer_done", nil)
			if len(added) > 0 {
				rt.emitReplanEvent("layer_done", added)
			}
		}

		layerIdx++
	}

	return rt.buildResult()
}

// tryReplan 让 LLM 基于当前图状态决定是否追加节点。返回追加的节点 id 列表。
// 消耗一次 replanUsed 额度；解析失败或 LLM 决定不追加则返回空。
func (rt *GraphRuntime) tryReplan(ctx context.Context, reason string, failed *graph.Node) []graph.NodeID {
	rt.replanUsed++
	rc := replanContext{
		Query:    rt.query,
		Reason:   reason,
		Failed:   failed,
		Snapshot: buildSnapshot(rt.graph),
	}
	newNodes := rt.agent.llmReplan(ctx, rc, rt.tools, rt.memPrefix, rt.allowSubAgents)
	if len(newNodes) == 0 {
		return nil
	}
	added := rt.graph.AddNodes(newNodes)
	// 追加后立即校验，检出环立刻回滚：把刚追加的节点标 Cancelled 并从图剔除依赖
	if err := rt.graph.Validate(); err != nil {
		logger.C(ctx).Warn("replan produced invalid graph, ignoring append", "err", err)
		for _, id := range added {
			rt.graph.SetNodeStatus(id, graph.StatusCancelled)
		}
		return nil
	}
	return added
}

// emitReplanEvent 推送 SSE replan 事件
func (rt *GraphRuntime) emitReplanEvent(reason string, added []graph.NodeID) {
	if rt.onEvent == nil {
		return
	}
	items := make([]map[string]string, 0, len(added))
	for _, id := range added {
		n := rt.graph.Nodes[id]
		items = append(items, map[string]string{
			"id":       string(id),
			"executor": executorName(n),
			"reason":   n.Name,
		})
	}
	rt.onEvent(NewStreamEvent("replan", map[string]interface{}{
		"reason":     reason,
		"added":      items,
		"used_count": rt.replanUsed,
		"max_count":  rt.cfg.MaxReplan,
	}))
}

// ─────────────────────────── 竞速执行（First-success-wins）──────────────────

func (rt *GraphRuntime) raceGroup(ctx context.Context, g raceGroup, wg *sync.WaitGroup) {
	defer wg.Done()

	type raceResult struct {
		nodeID graph.NodeID
		result string
		err    error
	}

	ch := make(chan raceResult, len(g.NodeIDs))
	// 竞速 ctx：在父 ctx 之上叠加 RaceTimeout，整组超时则全部取消。
	// RaceTimeoutMs <= 0 时退化为单纯的 WithCancel（保留首胜取消语义）。
	var raceCtx context.Context
	var cancel context.CancelFunc
	if rt.cfg.RaceTimeoutMs > 0 {
		raceCtx, cancel = context.WithTimeout(ctx, time.Duration(rt.cfg.RaceTimeoutMs)*time.Millisecond)
	} else {
		raceCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	// 并行启动所有竞速节点
	for _, nodeID := range g.NodeIDs {
		go func(id graph.NodeID) {
			// 获取信号量
			rt.sem <- struct{}{}
			defer func() { <-rt.sem }()

			// 如果已被取消（其他节点先胜出了），直接跳过
			if raceCtx.Err() != nil {
				ch <- raceResult{nodeID: id, err: fmt.Errorf("竞速取消")}
				return
			}

			res, execErr := rt.executeSingleNode(raceCtx, id)
			ch <- raceResult{nodeID: id, result: res, err: execErr}
		}(nodeID)
	}

	// 等待首个成功结果
	winnerFound := false
	winnerID := graph.NodeID("")
	var lastErr error
	for i := 0; i < len(g.NodeIDs); i++ {
		r := <-ch
		if r.err == nil && !winnerFound {
			// 首个成功 → 取消其余 + 标记胜出
			winnerFound = true
			winnerID = r.nodeID
			cancel()
			rt.mu.Lock()
			rt.results[r.nodeID] = r.result
			rt.graph.SetNodeStatus(r.nodeID, graph.StatusDone)
			rt.graph.SetNodeResult(r.nodeID, r.result)
			rt.mu.Unlock()

			// 推送竞速胜出事件
			if rt.onEvent != nil {
				node := rt.graph.Nodes[r.nodeID]
				rt.onEvent(NewStreamEvent("race_won", map[string]string{
					"race_group": g.RaceGroup, "winner": string(r.nodeID),
					"tool": executorName(node),
				}))
				rt.onEvent(NewStreamEvent("step", ReActStep{
					Type: StepObservation, Content: r.result,
					Tool: executorName(node), Params: node.Params,
				}))
			}
		} else if !winnerFound {
			lastErr = r.err
		}
	}

	// 竞速结束后，把非胜出节点全部标 Skipped，让下游可以推进
	if winnerFound {
		for _, id := range g.NodeIDs {
			if id == winnerID {
				continue
			}
			if rt.graph.Nodes[id].Status != graph.StatusDone {
				rt.graph.SetNodeStatus(id, graph.StatusSkipped)
			}
		}
	}

	// 如果没有胜出者，标记所有失败
	if !winnerFound {
		for _, nodeID := range g.NodeIDs {
			rt.graph.SetNodeStatus(nodeID, graph.StatusFailed)
			if lastErr != nil {
				rt.graph.SetNodeError(nodeID, lastErr.Error())
				rt.mu.Lock()
				rt.errors[nodeID] = lastErr.Error()
				rt.mu.Unlock()
			}
		}
	}
}

// ─────────────────────────── 单节点执行 ──────────────────────────────────────

func (rt *GraphRuntime) executeNode(ctx context.Context, nodeID graph.NodeID, wg *sync.WaitGroup) {
	defer wg.Done()

	// 获取信号量
	rt.sem <- struct{}{}
	defer func() { <-rt.sem }()

	result, err := rt.executeSingleNode(ctx, nodeID)
	if err != nil {
		rt.mu.Lock()
		rt.errors[nodeID] = err.Error()
		rt.mu.Unlock()
		return
	}

	rt.mu.Lock()
	rt.results[nodeID] = result
	rt.mu.Unlock()
}

// executeSingleNode 执行单个节点：获取工具 → 重试执行 → 记录结果
func (rt *GraphRuntime) executeSingleNode(ctx context.Context, nodeID graph.NodeID) (string, error) {
	node := rt.graph.Nodes[nodeID]
	executor := executorName(node)

	// 推送节点开始事件
	if rt.onEvent != nil {
		rt.onEvent(NewStreamEvent("node_start", map[string]string{
			"id": string(nodeID), "tool": executor,
		}))
	}

	// Thought 步骤
	thoughtStep := ReActStep{Type: StepThought, Content: node.Name}
	actionStep := ReActStep{Type: StepAction, Content: fmt.Sprintf("调用 %s", executor), Tool: executor, Params: node.Params}
	if rt.onEvent != nil {
		rt.onEvent(NewStreamEvent("step", thoughtStep))
		rt.onEvent(NewStreamEvent("step", actionStep))
	}

	rt.graph.SetNodeStatus(nodeID, graph.StatusRunning)

	params := make(map[string]interface{}, len(node.Params))
	for k, v := range node.Params {
		params[k] = v
	}
	rt.enrichParamsWithUpstream(node, params)

	// run 单次执行：优先用 ExecuteStructured（带错误分类），其次 ExecuteCtx（带 ctx），最后回落 Execute。
	// 返回 (结果字符串, 是否可重试, error)。可重试标志驱动外层重试循环——
	// 4xx / 参数错 / cancelled 不应重试，5xx / 网络抖动 / timeout 才重试。
	run := func(runCtx context.Context) (string, bool, error) {
		if node.Type == graph.NodeTypeSubAgent {
			sa, ok := rt.agent.subagents.get(node.AgentName)
			if !ok {
				return "", false, fmt.Errorf("子 Agent %s 不存在", node.AgentName)
			}
			res, err := sa.Run(runCtx, SubAgentTask{
				ID:       string(nodeID),
				Goal:     node.Goal,
				Query:    rt.task.Query,
				Upstream: rt.upstreamResults(node),
			})
			// 子 Agent 错误默认可重试（保守策略，子 Agent 内部可能是 LLM 抖动）
			return res, err != nil, err
		}
		t, ok := rt.tools[node.ToolName]
		if !ok {
			return "", false, fmt.Errorf("工具 %s 不在允许列表中", node.ToolName)
		}
		// 优先：结构化接口
		if t.ExecuteStructured != nil {
			r := t.ExecuteStructured(runCtx, params)
			if r.Success {
				return r.Payload, false, nil
			}
			retryable := r.Error != nil && r.Error.Retryable
			return r.Payload, retryable, r.Error
		}
		// 其次：带 ctx 的字符串接口
		if t.ExecuteCtx != nil {
			s, err := t.ExecuteCtx(runCtx, params)
			if err == nil && s == "[已中断]" {
				err = runCtx.Err()
				if err == nil {
					err = fmt.Errorf("工具 %s 返回中断状态", node.ToolName)
				}
			}
			return s, err != nil, err
		}
		// 兜底：老 Execute（无 ctx）—— 重试间会被 ctx.Done 截断
		s, err := t.Execute(params)
		if err == nil && s == "[已中断]" {
			err = ctx.Err()
			if err == nil {
				err = fmt.Errorf("工具 %s 返回中断状态", node.ToolName)
			}
		}
		return s, err != nil, err
	}

	var result string
	var execErr error
	maxRetries := rt.agent.cfg.MaxRetries
	retryDelay := time.Duration(rt.agent.cfg.RetryDelayMs) * time.Millisecond
	stepTimeout := time.Duration(rt.agent.cfg.StepTimeoutMs) * time.Millisecond
	// 子 Agent 内部会串行执行多次 LLM 调用（planQueries + 检索 + 综合），
	// Skill（skill_*）是 Prompt 驱动的 LLM 工具，单次调用也要十几秒。
	// 秒级的工具 step_timeout 会把它们直接判超时（观察显示 [已中断]）。
	// 这里给「子 Agent + skill 节点」放宽到宽松上限，避免被误杀。
	isLLMHeavy := node.Type == graph.NodeTypeSubAgent || strings.HasPrefix(node.ToolName, "skill_")
	if isLLMHeavy {
		if stepTimeout > 0 && stepTimeout < subAgentStepTimeout {
			stepTimeout = subAgentStepTimeout
		}
		// LLM 型节点重试代价高（每次都是完整 LLM 调用），只跑一次
		maxRetries = 1
	}

	for attempt := 0; attempt < maxRetries; attempt++ {
		if ctx.Err() != nil {
			rt.graph.SetNodeStatus(nodeID, graph.StatusCancelled)
			return "", fmt.Errorf("被用户中断")
		}

		// 单步 ctx：每次 attempt 都派生一个新的 timeout ctx，
		// 这样上一次的超时不会污染下一次。stepTimeout <= 0 时退化为父 ctx。
		var attemptCtx context.Context
		var attemptCancel context.CancelFunc
		if stepTimeout > 0 {
			attemptCtx, attemptCancel = context.WithTimeout(ctx, stepTimeout)
		} else {
			attemptCtx, attemptCancel = context.WithCancel(ctx)
		}

		var retryable bool
		result, retryable, execErr = run(attemptCtx)
		attemptCancel()

		if execErr == nil {
			break
		}
		rt.graph.SetNodeRetryCount(nodeID, attempt+1)

		// 不可重试错误（4xx / 参数错 / cancelled）→ 立刻退出
		if !retryable {
			break
		}
		if attempt < maxRetries-1 {
			time.Sleep(retryDelay)
		}
	}

	if execErr != nil {
		errMsg := execErr.Error()
		logger.C(ctx).Warn("node exec failed", "id", nodeID, "type", node.Type, "executor", executor,
			"step_timeout_ms", stepTimeout.Milliseconds(), "parent_ctx_err", ctx.Err(), "err", errMsg)
		rt.graph.SetNodeStatus(nodeID, graph.StatusFailed)
		rt.graph.SetNodeError(nodeID, errMsg)
		if rt.onEvent != nil {
			rt.onEvent(NewStreamEvent("step", ReActStep{Type: StepObservation, Content: fmt.Sprintf("执行失败: %s", errMsg)}))
		}
		rt.agent.pctx.pushTaskMem(promptctx.StepObservation{
			StepID: nodeStepID(nodeID), ToolName: executor,
			Error: errMsg, Success: false,
		})
		rt.agent.pctx.recordToolCall(promptctx.ToolCallTrace{
			ToolName: executor, Success: false, Summary: errMsg,
		})

		// Plan-and-ReAct: 节点失败时触发局部 replan（补一个替代节点）
		if rt.cfg.ReplanEnabled && rt.cfg.ReplanOnFailed && rt.replanUsed < rt.cfg.MaxReplan {
			// tryReplan 是并发安全的：AddNodes 在 Execute 主循环单线程调用，
			// 但这里在节点执行 goroutine 内触发，需要加锁保护 graph 的写路径
			rt.mu.Lock()
			added := rt.tryReplan(ctx, "node_failed", node)
			rt.mu.Unlock()
			if len(added) > 0 {
				rt.emitReplanEvent("node_failed", added)
			}
		}
		return "", execErr
	}

	// 成功
	rt.graph.SetNodeStatus(nodeID, graph.StatusDone)
	rt.graph.SetNodeResult(nodeID, result)

	if rt.onEvent != nil {
		rt.onEvent(NewStreamEvent("node_done", map[string]string{
			"id": string(nodeID), "tool": executor, "status": "done",
		}))
		rt.onEvent(NewStreamEvent("step", ReActStep{Type: StepObservation, Content: result}))
	}

	rt.agent.pctx.pushTaskMem(promptctx.StepObservation{
		StepID: nodeStepID(nodeID), ToolName: executor,
		Result: result, Success: true,
	})
	rt.agent.pctx.recordToolCall(promptctx.ToolCallTrace{
		ToolName: executor, Success: true, Summary: result,
	})

	return result, nil
}

// ─────────────────────────── 辅助方法 ──────────────────────────────────────

// nodeStepID 从 NodeID（如 "n1", "n2"）中提取数字 ID，失败则返回 0
func nodeStepID(id graph.NodeID) int {
	s := string(id)
	if len(s) > 1 && s[0] == 'n' {
		if n, err := strconv.Atoi(s[1:]); err == nil {
			return n
		}
	}
	return 0
}

func executorName(n *graph.Node) string {
	if n == nil {
		return ""
	}
	if n.Type == graph.NodeTypeSubAgent {
		return n.AgentName
	}
	return n.ToolName
}

func (rt *GraphRuntime) upstreamResults(node *graph.Node) map[string]string {
	out := make(map[string]string, len(node.DependsOn))
	for _, depID := range node.DependsOn {
		if dep, ok := rt.graph.Nodes[depID]; ok && dep.Result != "" {
			key := string(depID)
			if name := executorName(dep); name != "" {
				key = fmt.Sprintf("%s:%s", depID, name)
			}
			out[key] = dep.Result
		}
	}
	return out
}

// enrichParamsWithUpstream makes dependency outputs available to ordinary tools.
// Planner prompts often phrase a later skill input as "based on n1 result"; this
// replaces that placeholder with the actual upstream content before execution.
func (rt *GraphRuntime) enrichParamsWithUpstream(node *graph.Node, params map[string]interface{}) {
	upstream := rt.upstreamResults(node)
	if len(upstream) == 0 {
		return
	}
	text := formatUpstreamResults(upstream)
	if strings.TrimSpace(text) == "" {
		return
	}

	for k, v := range params {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if shouldReplacePlaceholderInput(s) {
			params[k] = text
		}
	}

	t, ok := rt.tools[node.ToolName]
	if !ok {
		return
	}
	for _, p := range t.Parameters {
		if !p.Required {
			continue
		}
		if _, exists := params[p.Name]; !exists {
			params[p.Name] = text
		}
	}
}

func formatUpstreamResults(upstream map[string]string) string {
	var b strings.Builder
	for _, id := range sortedKeys(upstream) {
		if strings.TrimSpace(upstream[id]) == "" {
			continue
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", id, upstream[id])
	}
	return strings.TrimSpace(b.String())
}

func shouldReplacePlaceholderInput(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return true
	}
	lowered := strings.ToLower(trimmed)
	placeholderHints := []string{
		"基于n", "基于 n", "n1", "n2", "n3", "搜索结果", "上一步", "上一节点",
		"润色后", "最终文本", "初稿", "draft", "previous", "upstream",
	}
	for _, hint := range placeholderHints {
		if strings.Contains(lowered, hint) {
			return true
		}
	}
	return false
}

// groupByRace 将同一层中的节点按 race_group 分组
func (rt *GraphRuntime) groupByRace(level []graph.NodeID) []raceGroup {
	groupMap := make(map[string][]graph.NodeID)
	var noGroup []graph.NodeID

	for _, id := range level {
		node := rt.graph.Nodes[id]
		if node.RaceGroup != "" {
			groupMap[node.RaceGroup] = append(groupMap[node.RaceGroup], id)
		} else {
			noGroup = append(noGroup, id)
		}
	}

	var groups []raceGroup
	for rg, ids := range groupMap {
		groups = append(groups, raceGroup{RaceGroup: rg, NodeIDs: ids})
	}
	// 无竞速组的节点，每个独立为一组
	for _, id := range noGroup {
		groups = append(groups, raceGroup{RaceGroup: "", NodeIDs: []graph.NodeID{id}})
	}

	// 按 race_group 排序保证确定性
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].RaceGroup < groups[j].RaceGroup
	})
	return groups
}

func (rt *GraphRuntime) buildResult() *GraphResult {
	observations := rt.graph.SuccessfulResults()
	nodeResults := make(map[graph.NodeID]NodeResult, len(rt.graph.Nodes))
	for id, n := range rt.graph.Nodes {
		nr := NodeResult{Status: n.Status, Result: n.Result, Error: n.Error}
		nodeResults[id] = nr
	}
	return &GraphResult{Observations: observations, NodeResults: nodeResults}
}

func (rt *GraphRuntime) buildInterruptedResult(ctx context.Context, msg string) *GraphResult {
	// 标记所有 pending/running 节点为 cancelled
	for _, n := range rt.graph.Nodes {
		if n.Status == graph.StatusPending || n.Status == graph.StatusRunning {
			rt.graph.SetNodeStatus(n.ID, graph.StatusCancelled)
		}
	}
	result := rt.buildResult()
	result.Interrupted = true
	result.InterruptedMsg = msg
	return result
}
