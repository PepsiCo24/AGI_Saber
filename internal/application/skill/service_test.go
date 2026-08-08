package skill

import (
	"context"
	"strings"
	"testing"

	"agi-assistant/config"
	skilldomain "agi-assistant/internal/domain/skill"
	"agi-assistant/internal/domain/tool"
	"agi-assistant/internal/infrastructure/llm"
)

type memoryRepo struct {
	items []skilldomain.Skill
}

func (r *memoryRepo) ListByUser(userID string) ([]skilldomain.Skill, error) {
	var out []skilldomain.Skill
	for _, sk := range r.items {
		if sk.UserID == userID {
			out = append(out, sk)
		}
	}
	return out, nil
}

func (r *memoryRepo) ListEnabled(userID string) ([]skilldomain.Skill, error) {
	var out []skilldomain.Skill
	for _, sk := range r.items {
		if sk.UserID == userID && sk.Enabled {
			out = append(out, sk)
		}
	}
	return out, nil
}

func (r *memoryRepo) Install(userID string, m skilldomain.Manifest) error {
	r.items = append(r.items, skilldomain.Skill{Manifest: m, UserID: userID, Enabled: false})
	return nil
}

func (r *memoryRepo) SetEnabled(userID, skillID string, enabled bool) (bool, error) {
	for i := range r.items {
		if r.items[i].UserID == userID && r.items[i].ID == skillID {
			r.items[i].Enabled = enabled
			return true, nil
		}
	}
	return false, nil
}

func (r *memoryRepo) Uninstall(userID, skillID string) (bool, error) {
	for i := range r.items {
		if r.items[i].UserID == userID && r.items[i].ID == skillID {
			r.items = append(r.items[:i], r.items[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func TestEnabledToolsExecutesGitHubPromptSkill(t *testing.T) {
	repo := &memoryRepo{items: []skilldomain.Skill{{
		Manifest: skilldomain.Manifest{
			ID:             "github:clhikari/astrbot_plugin_office_assistant",
			Name:           "clhikari/astrbot_plugin_office_assistant",
			Description:    "Office assistant test skill",
			Category:       "office",
			Source:         skilldomain.SourceGitHub,
			Invocation:     skilldomain.InvokePrompt,
			PromptTemplate: "GitHub skill background. Input: {{input}}",
			Parameters:     []tool.Param{{Name: "input", Type: "string", Description: "task", Required: true}},
		},
		UserID:  "u1",
		Enabled: true,
	}}}
	svc := NewService(repo, nil, llm.New(&config.APIConfig{}), false)

	tools := svc.EnabledTools("u1")
	ghTool, ok := tools["skill_clhikari_astrbot_plugin_office_assistant"]
	if !ok {
		t.Fatalf("expected github skill tool, got keys %#v", tools)
	}

	out, err := ghTool.ExecuteCtx(context.Background(), map[string]interface{}{"input": "把报告润色成正式文档"})
	if err != nil {
		t.Fatalf("ExecuteCtx returned error: %v", err)
	}
	if !strings.Contains(out, "把报告润色成正式文档") {
		t.Fatalf("expected mock output to include input, got %q", out)
	}
}

func TestPromptSkillCancellationReturnsError(t *testing.T) {
	repo := &memoryRepo{items: []skilldomain.Skill{{
		Manifest: skilldomain.Manifest{
			ID:             "builtin:doc_polish",
			Name:           "doc polish",
			Description:    "polish",
			Source:         skilldomain.SourceBuiltin,
			Invocation:     skilldomain.InvokePrompt,
			PromptTemplate: "{{input}}",
			Parameters:     []tool.Param{{Name: "input", Type: "string", Description: "task", Required: true}},
		},
		UserID:  "u1",
		Enabled: true,
	}}}
	svc := NewService(repo, nil, llm.New(&config.APIConfig{
		LLMConfig: config.LLMConfig{
			LLMAPIKey: "real-key-for-cancel-path",
			LLMAPIUrl: "http://127.0.0.1:1/v1/chat/completions",
			LLMModel:  "test-model",
		},
	}), false)
	tools := svc.EnabledTools("u1")
	polishTool := tools["skill_doc_polish"]

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := polishTool.ExecuteCtx(ctx, map[string]interface{}{"input": "abc"})
	if err == nil {
		t.Fatalf("expected cancellation error, got nil with output %q", out)
	}
}
