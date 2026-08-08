package longterm

import (
	"testing"
)

// TestQuarantine_BlocksRecall 验证被隔离条目默认不出现在 RecallByFilter 结果中。
func TestQuarantine_BlocksRecall(t *testing.T) {
	m := New()
	m.SetConsolidationConfig(DefaultConsolidationConfig())

	// 两条嵌入相似但不完全相同，避免触发 dedup（>=0.95）
	m.StoreClassified("u1", "用户城市: 北京", 0.9, []float64{1, 0, 0}, "preference", nil, "")
	m.StoreClassified("u1", "用户城市: 上海", 0.9, []float64{0, 1, 0}, "preference", nil, "")
	if m.Count() != 2 {
		t.Fatalf("应该有 2 条 (dedup 不应触发不同内容): %d", m.Count())
	}

	// 用一个跟两条都有正夹角的查询向量，并把阈值调到 0
	queryEmb := []float64{1, 1, 0}
	beforeIDs := idsOf(m.RecallByFilter("城市", queryEmb, RecallFilter{UserID: "u1", TopK: 10, MinScore: 0}))
	if len(beforeIDs) != 2 {
		t.Fatalf("隔离前应召回 2 条, 得到 %d (ids=%v)", len(beforeIDs), beforeIDs)
	}

	// 隔离第一条
	if !m.Quarantine(beforeIDs[0], "test") {
		t.Fatal("Quarantine 应成功")
	}

	afterIDs := idsOf(m.RecallByFilter("城市", queryEmb, RecallFilter{UserID: "u1", TopK: 10, MinScore: 0}))
	if len(afterIDs) != 1 {
		t.Fatalf("隔离后应只召回 1 条, 得到 %d (ids=%v)", len(afterIDs), afterIDs)
	}
	if afterIDs[0] == beforeIDs[0] {
		t.Errorf("被隔离的 id=%d 不应出现在结果中", beforeIDs[0])
	}
}

// TestQuarantine_IncludeFlag 验证 IncludeQuarantined=true 能召回隔离条目（审计用）。
func TestQuarantine_IncludeFlag(t *testing.T) {
	m := New()
	m.SetConsolidationConfig(DefaultConsolidationConfig())
	emb := []float64{1, 0, 0}
	m.StoreClassified("u1", "敏感信息", 0.9, emb, "fact", nil, "")
	id := m.LastID()
	m.Quarantine(id, "pii")

	// 默认不召回
	if got := m.RecallByFilter("敏感", emb, RecallFilter{UserID: "u1", TopK: 5}); len(got) != 0 {
		t.Errorf("默认不应召回, 得到 %d 条", len(got))
	}
	// IncludeQuarantined=true 时召回
	got := m.RecallByFilter("敏感", emb, RecallFilter{UserID: "u1", TopK: 5, IncludeQuarantined: true, MinScore: 0})
	if len(got) != 1 {
		t.Fatalf("Include 后应召回 1 条, 得到 %d", len(got))
	}
	if !got[0].Quarantined || got[0].QuarantineReason != "pii" {
		t.Errorf("被召回条目的隔离信息应保留：%+v", got[0])
	}
}

// TestQuarantine_FilterByCategory 验证 FilterByCategory 也过滤隔离条目（profile slot 路径）。
func TestQuarantine_FilterByCategory(t *testing.T) {
	m := New()
	m.StoreClassified("u1", "身份: 工程师", 0.9, nil, "identity", nil, "profile")
	m.StoreClassified("u1", "身份: 设计师", 0.9, nil, "identity", nil, "profile")
	if got := m.FilterByCategory("u1", []string{"identity"}, 10); len(got) != 2 {
		t.Fatalf("应有 2 条 identity, 得到 %d", len(got))
	}
	m.Quarantine(m.LastID(), "test")
	if got := m.FilterByCategory("u1", []string{"identity"}, 10); len(got) != 1 {
		t.Errorf("隔离后应剩 1 条, 得到 %d", len(got))
	}
}

// TestQuarantine_QuarantinedItems 验证审计端点能拿到所有隔离条目。
func TestQuarantine_QuarantinedItems(t *testing.T) {
	m := New()
	m.StoreClassified("u1", "a", 0.5, nil, "general", nil, "")
	m.StoreClassified("u1", "b", 0.5, nil, "general", nil, "")
	m.StoreClassified("u1", "c", 0.5, nil, "general", nil, "")

	if got := m.QuarantinedItems(); len(got) != 0 {
		t.Errorf("初始应无隔离条目, 得到 %d", len(got))
	}

	snap := m.Snapshot()
	m.Quarantine(snap[0].ID, "r1")
	m.Quarantine(snap[2].ID, "r2")

	got := m.QuarantinedItems()
	if len(got) != 2 {
		t.Fatalf("应有 2 条隔离, 得到 %d", len(got))
	}
}

// TestUnquarantine 验证解除隔离后条目恢复召回。
func TestUnquarantine(t *testing.T) {
	m := New()
	m.SetConsolidationConfig(DefaultConsolidationConfig())
	emb := []float64{1, 0}
	m.StoreClassified("u1", "test", 0.9, emb, "general", nil, "")
	id := m.LastID()
	m.Quarantine(id, "x")
	if !m.Unquarantine(id) {
		t.Fatal("Unquarantine 应成功")
	}
	got := m.RecallByFilter("test", emb, RecallFilter{UserID: "u1", TopK: 5, MinScore: 0})
	if len(got) != 1 {
		t.Errorf("解除隔离后应可召回, 得到 %d", len(got))
	}
}

func idsOf(items []Item) []int {
	ids := make([]int, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	return ids
}
