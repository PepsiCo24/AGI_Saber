package chat

import (
	"context"
	"testing"

	"agi-assistant/internal/domain/graph"
	"agi-assistant/internal/domain/tool"
)

func TestDocumentTitlePrefersMarkdownH1(t *testing.T) {
	content := "## n2\n\n# PPO算法研究综述：对比TRPO的优势、实现特性及应用领域分析\n\n## 摘要\n..."

	got := documentTitle(content, "保存报告到本地文档库并写入 RAG", "写一份 PPO 报告")
	want := "PPO算法研究综述：对比TRPO的优势、实现特性及应用领域分析"
	if got != want {
		t.Fatalf("documentTitle() = %q, want %q", got, want)
	}
}

func TestDocumentContentPrefersWriterAgent(t *testing.T) {
	task := SubAgentTask{
		Query: "生成报告",
		Upstream: map[string]string{
			"n3:review_agent": "Review: 需要补证据",
			"n2:writer_agent": "# 真正的报告\n\n正文",
		},
	}

	got := documentContent(task)
	want := "# 真正的报告\n\n正文"
	if got != want {
		t.Fatalf("documentContent() = %q, want %q", got, want)
	}
}

func TestDocumentTitlePrefersExplicitRequestedTitle(t *testing.T) {
	content := "# 模型生成的其他标题\n\n正文"

	got := documentTitle(content, "保存报告到本地文档库并写入 RAG", "生成一份标题为《子Agent联调测试》的简短报告")
	want := "子Agent联调测试"
	if got != want {
		t.Fatalf("documentTitle() = %q, want %q", got, want)
	}
}

func TestDocumentContentStripsMarkdownFence(t *testing.T) {
	task := SubAgentTask{
		Query: "生成报告",
		Upstream: map[string]string{
			"n2:writer_agent": "```markdown\n# 子Agent联调测试\n\n正文\n```",
		},
	}

	got := documentContent(task)
	want := "# 子Agent联调测试\n\n正文"
	if got != want {
		t.Fatalf("documentContent() = %q, want %q", got, want)
	}
}

func TestResearchQueryPlansSubAgents(t *testing.T) {
	agent := &UnifiedAgent{}

	nodes := agent.llmPlanGraph(context.Background(), "调研 PPO 的优势，两句话即可", map[string]tool.Tool{}, "", true)
	wantAgents := []string{"research_agent", "writer_agent", "review_agent"}
	if len(nodes) != len(wantAgents) {
		t.Fatalf("len(nodes) = %d, want %d", len(nodes), len(wantAgents))
	}
	for i, want := range wantAgents {
		if nodes[i].Type != graph.NodeTypeSubAgent || nodes[i].AgentName != want {
			t.Fatalf("nodes[%d] = (%s, %s), want sub_agent %s", i, nodes[i].Type, nodes[i].AgentName, want)
		}
	}
}

func TestReportSaveQueryPlansDocAgent(t *testing.T) {
	agent := &UnifiedAgent{}

	nodes := agent.llmPlanGraph(context.Background(), "生成一份 PPO 报告并保存到本地文档库", map[string]tool.Tool{}, "", true)
	wantAgents := []string{"research_agent", "writer_agent", "review_agent", "doc_agent"}
	if len(nodes) != len(wantAgents) {
		t.Fatalf("len(nodes) = %d, want %d", len(nodes), len(wantAgents))
	}
	for i, want := range wantAgents {
		if nodes[i].Type != graph.NodeTypeSubAgent || nodes[i].AgentName != want {
			t.Fatalf("nodes[%d] = (%s, %s), want sub_agent %s", i, nodes[i].Type, nodes[i].AgentName, want)
		}
	}
}
