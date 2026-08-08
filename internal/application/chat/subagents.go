package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"agi-assistant/internal/domain/document"
	"agi-assistant/internal/infrastructure/llm"
	"agi-assistant/internal/usercontext"
)

type SubAgentTask struct {
	ID       string            `json:"id"`
	Goal     string            `json:"goal"`
	Query    string            `json:"query"`
	Upstream map[string]string `json:"upstream,omitempty"`
}

type SubAgent interface {
	Name() string
	Description() string
	Run(ctx context.Context, task SubAgentTask) (string, error)
}

type subAgentRegistry struct {
	mu     sync.RWMutex
	agents map[string]SubAgent
}

func newSubAgentRegistry() *subAgentRegistry {
	return &subAgentRegistry{agents: make(map[string]SubAgent)}
}

func (r *subAgentRegistry) register(a SubAgent) {
	r.mu.Lock()
	r.agents[a.Name()] = a
	r.mu.Unlock()
}

func (r *subAgentRegistry) get(name string) (SubAgent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.agents[name]
	return a, ok
}

func (r *subAgentRegistry) snapshot() map[string]SubAgent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]SubAgent, len(r.agents))
	for k, v := range r.agents {
		out[k] = v
	}
	return out
}

func (a *UnifiedAgent) registerBuiltinSubAgents() {
	a.subagents.register(&researchAgent{agent: a})
	a.subagents.register(&writerAgent{agent: a})
	a.subagents.register(&reviewAgent{agent: a})
	a.subagents.register(&docAgent{agent: a})
}

type researchAgent struct{ agent *UnifiedAgent }

func (r *researchAgent) Name() string { return "research_agent" }
func (r *researchAgent) Description() string {
	return "Agentic RAG researcher: 多轮改写、知识库/搜索检索、证据整理。"
}

func (r *researchAgent) Run(ctx context.Context, task SubAgentTask) (string, error) {
	// 子 Agent 复用主请求的 STM 历史给 RAG rewriter 用——userID 从 ctx 取
	userID := usercontext.UserIDFromContext(ctx)
	queries := r.planQueries(ctx, task)
	var observations []string
	var evidence []string
	for _, q := range queries {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if r.agent.rag.Loaded {
			answer, results := r.agent.rag.QueryWithHistory(q, r.agent.recentHistoryForRAG(userID))
			observations = append(observations, fmt.Sprintf("Query: %s\nRAG Answer: %s", q, answer))
			for _, sr := range results {
				content := sr.Chunk.Content
				if runeCount(content) > 180 {
					content = string([]rune(content)[:180]) + "..."
				}
				evidence = append(evidence, fmt.Sprintf("- %s", content))
			}
			continue
		}
		if t, ok := r.agent.toolsSnapshot()["search_web"]; ok {
			out, err := t.Execute(map[string]interface{}{"query": q})
			if err == nil {
				observations = append(observations, fmt.Sprintf("Query: %s\nSearch Result: %s", q, out))
			}
		}
	}
	if len(observations) == 0 {
		observations = append(observations, "未找到可用知识库或搜索结果。")
	}
	userMsg := fmt.Sprintf("研究目标：%s\n原始问题：%s\n\n观察结果：\n%s\n\n证据片段：\n%s",
		task.Goal, task.Query, strings.Join(observations, "\n\n"), strings.Join(evidence, "\n"))
	if !r.agent.cfg.IsRealLLM() {
		return "## Research Findings\n\n" + userMsg, nil
	}
	return r.agent.llm.ChatContextFast(ctx,
		"你是 research_agent。请基于观察结果输出结构化研究摘要，包含 Findings、Evidence、Open Questions。不要编造未出现的信息。",
		[]llm.Message{{Role: "user", Content: userMsg}},
	), nil
}

func (r *researchAgent) planQueries(ctx context.Context, task SubAgentTask) []string {
	base := strings.TrimSpace(task.Goal)
	if base == "" {
		base = task.Query
	}
	if !r.agent.cfg.IsRealLLM() {
		return []string{base}
	}
	raw := r.agent.llm.ChatContextFast(ctx,
		"你是查询规划器。请把研究目标改写成 2-3 条互补检索查询，严格输出 JSON：{\"queries\":[\"...\"]}",
		[]llm.Message{{Role: "user", Content: base}},
	)
	var parsed struct {
		Queries []string `json:"queries"`
	}
	raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(raw, "```json"), "```"), "```"))
	if json.Unmarshal([]byte(raw), &parsed) != nil || len(parsed.Queries) == 0 {
		return []string{base}
	}
	out := []string{base}
	for _, q := range parsed.Queries {
		q = strings.TrimSpace(q)
		if q != "" && len(out) < 3 {
			out = append(out, q)
		}
	}
	return dedupStrings(out)
}

type writerAgent struct{ agent *UnifiedAgent }

func (w *writerAgent) Name() string { return "writer_agent" }
func (w *writerAgent) Description() string {
	return "将上游研究结果整理为 Markdown 报告。"
}
func (w *writerAgent) Run(ctx context.Context, task SubAgentTask) (string, error) {
	input := upstreamText(task)
	if !w.agent.cfg.IsRealLLM() {
		return "# " + safeTitle(task.Goal, task.Query) + "\n\n" + input, nil
	}
	return w.agent.llm.ChatContextFast(ctx,
		"你是 writer_agent。请把输入整理为清晰 Markdown 报告，包含摘要、分析、建议和下一步。",
		[]llm.Message{{Role: "user", Content: fmt.Sprintf("写作目标：%s\n\n材料：\n%s", task.Goal, input)}},
	), nil
}

type reviewAgent struct{ agent *UnifiedAgent }

func (r *reviewAgent) Name() string { return "review_agent" }
func (r *reviewAgent) Description() string {
	return "检查报告结构、事实一致性、证据覆盖和风险。"
}
func (r *reviewAgent) Run(ctx context.Context, task SubAgentTask) (string, error) {
	input := upstreamText(task)
	if !r.agent.cfg.IsRealLLM() {
		return "Review: 内容已整理；建议人工确认关键事实。", nil
	}
	return r.agent.llm.ChatContextFast(ctx,
		"你是 review_agent。请审查输入，输出问题清单、可信度和需要补证据的点。",
		[]llm.Message{{Role: "user", Content: input}},
	), nil
}

type docAgent struct{ agent *UnifiedAgent }

func (d *docAgent) Name() string { return "doc_agent" }
func (d *docAgent) Description() string {
	return "将上游结果保存到本地文档库，并同步写入 RAG。"
}
func (d *docAgent) Run(ctx context.Context, task SubAgentTask) (string, error) {
	content := documentContent(task)
	if strings.TrimSpace(content) == "" {
		content = task.Query
	}
	title := documentTitle(content, task.Goal, task.Query)
	res, err := d.agent.WriteDocument(document.WriteRequest{
		Title:     title,
		DocType:   "report",
		Source:    document.DocumentSourceAgent,
		CreatedBy: d.Name(),
		ContentMD: content,
		Summary:   firstRunes(content, 180),
		Metadata: map[string]interface{}{
			"sub_agent": d.Name(),
			"task_id":   task.ID,
			"review":    firstRunes(upstreamByAgent(task, "review_agent"), 1200),
		},
	}, true)
	if err != nil {
		return "", err
	}
	return jsonString(res), nil
}

func upstreamText(task SubAgentTask) string {
	if len(task.Upstream) == 0 {
		return task.Query
	}
	var b strings.Builder
	for _, id := range sortedKeys(task.Upstream) {
		result := task.Upstream[id]
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", id, result)
	}
	return strings.TrimSpace(b.String())
}

func documentContent(task SubAgentTask) string {
	if text := upstreamByAgent(task, "writer_agent"); strings.TrimSpace(text) != "" {
		return stripMarkdownFence(text)
	}
	for _, id := range sortedKeys(task.Upstream) {
		if strings.TrimSpace(task.Upstream[id]) != "" {
			return stripMarkdownFence(task.Upstream[id])
		}
	}
	return strings.TrimSpace(task.Query)
}

func upstreamByAgent(task SubAgentTask, agentName string) string {
	for _, id := range sortedKeys(task.Upstream) {
		if strings.Contains(id, agentName) {
			return task.Upstream[id]
		}
	}
	return ""
}

func documentTitle(content, goal, query string) string {
	if title := explicitRequestedTitle(query); title != "" {
		return firstRunes(title, 80)
	}
	if title := explicitRequestedTitle(goal); title != "" {
		return firstRunes(title, 80)
	}
	if title := markdownTitle(content); title != "" {
		return firstRunes(title, 80)
	}
	return safeTitle("", fallbackTitleInput(goal, query))
}

func markdownTitle(content string) string {
	content = stripMarkdownFence(content)
	var fallback string
	inFence := false
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		level, title, ok := markdownHeading(line)
		if !ok || title == "" || isGenericDocHeading(title) {
			continue
		}
		if level == 1 {
			return title
		}
		if fallback == "" {
			fallback = title
		}
	}
	return fallback
}

func markdownHeading(line string) (int, string, bool) {
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level >= len(line) || line[level] != ' ' {
		return 0, "", false
	}
	title := strings.TrimSpace(line[level:])
	title = strings.Trim(title, "# \t")
	title = strings.Trim(title, "*_`")
	return level, strings.TrimSpace(title), true
}

func explicitRequestedTitle(s string) string {
	s = strings.TrimSpace(s)
	for _, marker := range []string{"标题为《", "标题是《", "题为《"} {
		start := strings.Index(s, marker)
		if start < 0 {
			continue
		}
		rest := s[start+len(marker):]
		if end := strings.Index(rest, "》"); end > 0 {
			return strings.TrimSpace(rest[:end])
		}
	}
	for _, marker := range []string{"标题为\"", "标题是\"", "题为\""} {
		start := strings.Index(s, marker)
		if start < 0 {
			continue
		}
		rest := s[start+len(marker):]
		if end := strings.Index(rest, "\""); end > 0 {
			return strings.TrimSpace(rest[:end])
		}
	}
	return ""
}

func stripMarkdownFence(s string) string {
	trimmed := strings.TrimSpace(s)
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 2 || !strings.HasPrefix(strings.TrimSpace(lines[0]), "```") {
		return trimmed
	}
	last := strings.TrimSpace(lines[len(lines)-1])
	if !strings.HasPrefix(last, "```") {
		return trimmed
	}
	return strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
}

func isGenericDocHeading(title string) bool {
	t := strings.TrimSpace(strings.ToLower(title))
	if t == "" {
		return true
	}
	if len([]rune(t)) <= 3 && strings.HasPrefix(t, "n") {
		return true
	}
	switch t {
	case "摘要", "分析", "建议", "下一步", "结论", "review", "findings", "evidence", "open questions", "research findings":
		return true
	default:
		return false
	}
}

func fallbackTitleInput(goal, query string) string {
	query = strings.TrimSpace(query)
	if query != "" {
		return query
	}
	return strings.TrimSpace(goal)
}

func safeTitle(goal, query string) string {
	title := strings.TrimSpace(goal)
	if title == "" {
		title = strings.TrimSpace(query)
	}
	title = strings.TrimPrefix(title, "生成")
	title = strings.TrimPrefix(title, "撰写")
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Agent Report"
	}
	return firstRunes(title, 60)
}

func sortedKeys(in map[string]string) []string {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func firstRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "..."
}

func dedupStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		key := strings.ToLower(strings.TrimSpace(s))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, s)
	}
	return out
}
