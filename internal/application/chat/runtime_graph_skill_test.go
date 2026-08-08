package chat

import (
	"context"
	"strings"
	"testing"

	"agi-assistant/config"
	"agi-assistant/internal/domain/graph"
	"agi-assistant/internal/domain/tool"
)

type noopSnapshotRepo struct{}

func (noopSnapshotRepo) Save(taskID string, stateJSON []byte) {}

func TestGraphRuntimeReplacesSkillPlaceholderWithUpstreamResult(t *testing.T) {
	tg := graph.NewTaskGraph([]*graph.Node{
		{
			ID:       "n1",
			Type:     graph.NodeTypeTool,
			ToolName: "source",
			Params:   map[string]string{"query": "draft"},
		},
		{
			ID:        "n2",
			Type:      graph.NodeTypeTool,
			ToolName:  "skill_doc_polish",
			Params:    map[string]string{"input": "基于n1搜索结果撰写的初稿"},
			DependsOn: []graph.NodeID{"n1"},
		},
	})

	var skillInput string
	agent := &UnifiedAgent{cfg: &config.APIConfig{
		HarnessConfig: config.HarnessConfig{MaxRetries: 1, StepTimeoutMs: 1000},
	}, runtime: newTaskRuntime(), repos: &repoBundle{snap: noopSnapshotRepo{}}}
	rt := NewGraphRuntime(tg, agent, GraphConfig{MaxParallel: 1}, map[string]tool.Tool{
		"source": {
			Name: "source",
			ExecuteCtx: func(ctx context.Context, params map[string]interface{}) (string, error) {
				return "这是来自 n1 的真实搜索结果", nil
			},
		},
		"skill_doc_polish": {
			Name: "skill_doc_polish",
			Parameters: []tool.Param{
				{Name: "input", Type: "string", Required: true},
			},
			ExecuteCtx: func(ctx context.Context, params map[string]interface{}) (string, error) {
				skillInput, _ = params["input"].(string)
				return "polished", nil
			},
		},
	}, &TaskState{Query: "润色报告"}, nil)

	result := rt.Execute(context.Background())
	if result.Interrupted {
		t.Fatalf("runtime interrupted: %s", result.InterruptedMsg)
	}
	if !strings.Contains(skillInput, "这是来自 n1 的真实搜索结果") {
		t.Fatalf("expected upstream result in skill input, got %q", skillInput)
	}
	if strings.Contains(skillInput, "基于n1搜索结果撰写的初稿") {
		t.Fatalf("placeholder was not replaced: %q", skillInput)
	}
}
