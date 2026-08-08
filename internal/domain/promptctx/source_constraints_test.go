package promptctx

import (
	"context"
	"strings"
	"testing"

	"agi-assistant/internal/domain/sandbox"
)

func TestConstraintsSource_BlockBeforeWarn(t *testing.T) {
	policies := sandbox.PolicySnapshot()
	src := NewConstraintsSource(policies)

	slot := Slot{Kind: SlotConstraints, Filter: SlotFilter{}}
	items, err := src.Fetch(context.Background(), slot, Query{})
	if err != nil {
		t.Fatalf("Fetch error: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("expected policies, got none")
	}

	// 第一批应该是 Block（score=1.0），之后才是 Warn（score=0.5）
	seenWarn := false
	for _, it := range items {
		if it.Score < 1.0 {
			seenWarn = true
		}
		if seenWarn && it.Score == 1.0 {
			t.Errorf("Block policy found after Warn policy — ordering violated")
		}
	}
}

func TestConstraintsSource_TopKTruncation(t *testing.T) {
	policies := sandbox.PolicySnapshot()
	src := NewConstraintsSource(policies)

	topK := 5
	slot := Slot{Kind: SlotConstraints, Filter: SlotFilter{TopK: topK}}
	items, _ := src.Fetch(context.Background(), slot, Query{})
	if len(items) > topK {
		t.Errorf("expected ≤%d items, got %d", topK, len(items))
	}
}

func TestConstraintsSource_ContainsRmRf(t *testing.T) {
	policies := sandbox.PolicySnapshot()
	src := NewConstraintsSource(policies)

	slot := Slot{Kind: SlotConstraints, Filter: SlotFilter{}}
	items, _ := src.Fetch(context.Background(), slot, Query{})

	found := false
	for _, it := range items {
		if strings.Contains(it.Text, "rm") || strings.Contains(it.Text, "删除") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected rm-related policy in constraints, not found")
	}
}

func TestConstraintsSource_EmptyPolicies(t *testing.T) {
	src := NewConstraintsSource(nil)
	slot := Slot{Kind: SlotConstraints, Filter: SlotFilter{}}
	items, err := src.Fetch(context.Background(), slot, Query{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items for empty policy list, got %d", len(items))
	}
}
