package promptctx

import (
	"context"
	"testing"

	"agi-assistant/internal/domain/memory/longterm"
)

type fakeRecaller struct {
	called bool
	filter longterm.RecallFilter
	items  []longterm.Item
}

func (f *fakeRecaller) RecallByFilter(query string, queryEmbedding []float64, filter longterm.RecallFilter) []longterm.Item {
	f.called = true
	f.filter = filter
	return f.items
}

func TestRecallSource_FilterPassThrough(t *testing.T) {
	r := &fakeRecaller{
		items: []longterm.Item{
			{Content: "记忆A", Importance: 0.8, Score: 0.7, Category: "episodic"},
			{Content: "记忆B", Importance: 0.9, Score: 0.6, Category: "fact"},
		},
	}
	src := NewRecallSource(r)

	slot := Slot{
		Kind: SlotRecall,
		Filter: SlotFilter{
			Categories:  []string{"episodic", "fact"},
			TopK:        2,
			MinScore:    0.4,
			MaxAgeHours: 48,
		},
	}
	items, err := src.Fetch(context.Background(), slot, Query{Text: "hi", UserID: "u1"})
	if err != nil {
		t.Fatalf("Fetch error: %v", err)
	}

	if !r.called {
		t.Error("recaller not called")
	}
	// 验证 filter 透传（含多租户 UserID）
	if r.filter.UserID != "u1" {
		t.Errorf("UserID not propagated: %q", r.filter.UserID)
	}
	if len(r.filter.Categories) != 2 || r.filter.TopK != 2 || r.filter.MinScore != 0.4 || r.filter.MaxAgeHours != 48 {
		t.Errorf("filter not properly passed: %+v", r.filter)
	}
	if len(items) != 2 {
		t.Errorf("expected 2 items, got %d", len(items))
	}
	if items[0].Source != "recall" {
		t.Errorf("expected source=recall, got %s", items[0].Source)
	}
	if items[0].Meta["category"] != "episodic" {
		t.Errorf("expected meta category=episodic, got %v", items[0].Meta)
	}
}

func TestRecallSource_NilRecaller(t *testing.T) {
	src := NewRecallSource(nil)
	items, err := src.Fetch(context.Background(), Slot{Kind: SlotRecall}, Query{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if items != nil {
		t.Errorf("expected nil items, got %v", items)
	}
}

func TestRecallSource_EmptyHits(t *testing.T) {
	r := &fakeRecaller{items: nil}
	src := NewRecallSource(r)
	items, _ := src.Fetch(context.Background(), Slot{Kind: SlotRecall}, Query{UserID: "u1"})
	if len(items) != 0 {
		t.Errorf("expected empty result, got %d items", len(items))
	}
}

// TestRecallSource_AnonymousNoCall 验证未登录（UserID="") 时不调用底层 recaller，
// 防止 application 层忘传时把别人的记忆灌进 prompt。
func TestRecallSource_AnonymousNoCall(t *testing.T) {
	r := &fakeRecaller{items: []longterm.Item{{Content: "should-not-leak"}}}
	src := NewRecallSource(r)
	items, _ := src.Fetch(context.Background(), Slot{Kind: SlotRecall}, Query{Text: "hi"})
	if r.called {
		t.Error("UserID 为空时 recaller 不应被调用")
	}
	if items != nil {
		t.Errorf("UserID 为空时不应返回任何条目")
	}
}
