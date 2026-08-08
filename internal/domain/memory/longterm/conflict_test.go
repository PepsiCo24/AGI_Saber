package longterm

import (
	"testing"
)

// TestConflictCandidates_Range 验证候选筛选区间 [0.75, 0.95)：
// 完全相同（≥0.95）走 dedup，不在候选；不同主题（<0.75）也不在候选。
func TestConflictCandidates_Range(t *testing.T) {
	m := New()
	m.SetConsolidationConfig(DefaultConsolidationConfig())

	// 三条 preference，emb 间夹角足够大，确保 Store 时不会触发 dedup（cosine<0.95）：
	//   A: 偏好猫       emb=[1,0,0]
	//   B: 不喜欢猫     emb=[0.5,0.866,0]   ← 与 A cosine=0.5（不 dedup）
	//   C: 偏好咖啡     emb=[0,0,1]         ← 与 A,B 完全垂直
	embA := []float64{1, 0, 0}
	embB := []float64{0.5, 0.866, 0}
	embC := []float64{0, 0, 1}
	m.StoreClassified("u1", "用户喜欢猫", 0.7, embA, "preference", nil, "")
	m.StoreClassified("u1", "用户不喜欢猫", 0.7, embB, "preference", nil, "")
	m.StoreClassified("u1", "用户喜欢咖啡", 0.7, embC, "preference", nil, "")
	if m.Count() != 3 {
		t.Fatalf("应有 3 条 preference, 得到 %d", m.Count())
	}

	// newEmb=[0.9,0.4,0]：与 A cosine≈0.914，与 B cosine≈0.808，与 C 垂直
	// → A,B 都应进候选；C 不应进
	newEmb := []float64{0.9, 0.4, 0}
	cands := m.ConflictCandidates("u1", newEmb, "preference", 0.75, 0.95)
	if len(cands) == 0 {
		t.Fatalf("应该至少返回 1 个候选，得到 0")
	}
	for _, c := range cands {
		if c.Score < 0.75 || c.Score >= 0.95 {
			t.Errorf("候选分数应在 [0.75, 0.95) 区间，得到 %.3f", c.Score)
		}
		if c.Item.Content == "用户喜欢咖啡" {
			t.Errorf("不相关条目（与 newEmb 垂直）不应进入候选")
		}
	}
}

// TestConflictCandidates_CategoryFilter 验证不同 category 不会互相成为候选。
func TestConflictCandidates_CategoryFilter(t *testing.T) {
	m := New()
	m.SetConsolidationConfig(DefaultConsolidationConfig())
	emb := []float64{1, 0}
	m.StoreClassified("u1", "身份: 工程师", 0.9, emb, "identity", nil, "")
	m.StoreClassified("u1", "偏好: 工程", 0.9, []float64{0.99, 0.01}, "preference", nil, "")

	cands := m.ConflictCandidates("u1", emb, "identity", 0.5, 0.99)
	for _, c := range cands {
		if c.Item.Category != "identity" {
			t.Errorf("跨 category 候选不应出现：%+v", c.Item)
		}
	}
}

// TestConflictCandidates_ExcludeSuperseded 验证已 Superseded 条目不再出现在候选中。
func TestConflictCandidates_ExcludeSuperseded(t *testing.T) {
	m := New()
	m.SetConsolidationConfig(DefaultConsolidationConfig())
	embA := []float64{1, 0}
	m.StoreClassified("u1", "用户在北京", 0.7, embA, "fact", nil, "")
	idA := m.LastID()

	// query=[0.8,0.6], stored=[1,0] → cosine = 0.8（落在 [0.5, 0.99) 区间内）
	queryEmb := []float64{0.8, 0.6}

	// 第一次冲突：找候选 → A 在结果里
	cands := m.ConflictCandidates("u1", queryEmb, "fact", 0.5, 0.99)
	if len(cands) != 1 || cands[0].Item.ID != idA {
		t.Fatalf("应找到 A 作为候选, 得到 %+v", cands)
	}

	// 标 A 为 superseded，再次找候选 → 不应再出现
	m.MarkSuperseded([]int{idA}, 0)
	cands2 := m.ConflictCandidates("u1", queryEmb, "fact", 0.5, 0.99)
	if len(cands2) != 0 {
		t.Errorf("Superseded 条目不应出现在候选中, 得到 %+v", cands2)
	}
}

// TestMarkSuperseded_BlocksRecall 验证标记后默认从 RecallByFilter 召回中过滤。
func TestMarkSuperseded_BlocksRecall(t *testing.T) {
	m := New()
	m.SetConsolidationConfig(DefaultConsolidationConfig())
	m.StoreClassified("u1", "旧事实", 0.7, []float64{1, 0}, "fact", nil, "")
	idOld := m.LastID()
	m.StoreClassified("u1", "新事实", 0.7, []float64{0, 1}, "fact", nil, "")
	idNew := m.LastID()

	// 标旧 superseded，新条目记录替代关系
	marked := m.MarkSuperseded([]int{idOld}, idNew)
	if len(marked) != 1 || marked[0] != idOld {
		t.Fatalf("应标记 oldID, 得到 %v", marked)
	}

	// 默认召回不应包含旧条目
	q := []float64{1, 1}
	got := m.RecallByFilter("", q, RecallFilter{UserID: "u1", TopK: 5, MinScore: 0})
	for _, it := range got {
		if it.ID == idOld {
			t.Errorf("Superseded 条目不应被召回, 得到 ID=%d", it.ID)
		}
	}

	// 审计模式应能看到
	got2 := m.RecallByFilter("", q, RecallFilter{UserID: "u1", TopK: 5, MinScore: 0, IncludeSuperseded: true})
	found := false
	for _, it := range got2 {
		if it.ID == idOld {
			found = true
		}
	}
	if !found {
		t.Errorf("IncludeSuperseded=true 时应能召回旧条目")
	}
}

// TestMarkSuperseded_LinksNewItem 验证新条目的 Supersedes 字段被正确填充。
func TestMarkSuperseded_LinksNewItem(t *testing.T) {
	m := New()
	m.StoreClassified("u1", "旧 1", 0.5, nil, "fact", nil, "")
	id1 := m.LastID()
	m.StoreClassified("u1", "旧 2", 0.5, nil, "fact", nil, "")
	id2 := m.LastID()
	m.StoreClassified("u1", "新", 0.5, nil, "fact", nil, "")
	idNew := m.LastID()

	m.MarkSuperseded([]int{id1, id2}, idNew)

	newItem, ok := m.FindByID(idNew)
	if !ok {
		t.Fatal("新条目应存在")
	}
	if len(newItem.Supersedes) != 2 {
		t.Fatalf("新条目 Supersedes 应有 2 个 ID, 得到 %v", newItem.Supersedes)
	}
}

// TestMarkSuperseded_Idempotent 验证重复标记不会产生 Supersedes 重复条目。
func TestMarkSuperseded_Idempotent(t *testing.T) {
	m := New()
	m.StoreClassified("u1", "旧", 0.5, nil, "fact", nil, "")
	idOld := m.LastID()
	m.StoreClassified("u1", "新", 0.5, nil, "fact", nil, "")
	idNew := m.LastID()

	m.MarkSuperseded([]int{idOld}, idNew)
	// 第二次：旧条目已 Superseded → 应只返回空
	again := m.MarkSuperseded([]int{idOld}, idNew)
	if len(again) != 0 {
		t.Errorf("已标记的 ID 不应再次触发, 得到 %v", again)
	}
	newItem, _ := m.FindByID(idNew)
	if len(newItem.Supersedes) != 1 {
		t.Errorf("Supersedes 不应有重复, 得到 %v", newItem.Supersedes)
	}
}

// TestSupersededItems 验证审计端点能拿到完整列表。
func TestSupersededItems(t *testing.T) {
	m := New()
	m.StoreClassified("u1", "a", 0.5, nil, "fact", nil, "")
	m.StoreClassified("u1", "b", 0.5, nil, "fact", nil, "")
	m.StoreClassified("u1", "c", 0.5, nil, "fact", nil, "")

	if got := m.SupersededItems(); len(got) != 0 {
		t.Errorf("初始应无 superseded, 得到 %d", len(got))
	}
	snap := m.Snapshot()
	m.MarkSuperseded([]int{snap[0].ID, snap[2].ID}, 0)
	if got := m.SupersededItems(); len(got) != 2 {
		t.Errorf("应有 2 条 superseded, 得到 %d", len(got))
	}
}

// TestFilterByCategory_ExcludesSuperseded 验证 profile slot 路径也过滤 superseded。
func TestFilterByCategory_ExcludesSuperseded(t *testing.T) {
	m := New()
	m.StoreClassified("u1", "身份 v1", 0.9, nil, "identity", nil, "profile")
	id1 := m.LastID()
	m.StoreClassified("u1", "身份 v2", 0.9, nil, "identity", nil, "profile")

	if got := m.FilterByCategory("u1", []string{"identity"}, 10); len(got) != 2 {
		t.Fatalf("初始应有 2 条, 得到 %d", len(got))
	}
	m.MarkSuperseded([]int{id1}, 0)
	got := m.FilterByCategory("u1", []string{"identity"}, 10)
	if len(got) != 1 {
		t.Errorf("Superseded 应被过滤, 得到 %d", len(got))
	}
}
