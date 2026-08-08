package longterm

import (
	"testing"
)

// TestMultiUser_RecallIsolated 验证不同 userID 的记忆互不可见。
//
// 这是 V1 多租户改造的核心验收用例：
//   - alice 写一条记忆，bob 召回拿不到
//   - 召回时 UserID 必填——空字符串拿不到任何条目
func TestMultiUser_RecallIsolated(t *testing.T) {
	m := New()
	m.SetConsolidationConfig(DefaultConsolidationConfig())

	// alice 写一条 preference
	m.StoreClassified("alice", "用户喜欢猫", 0.9, []float64{1, 0, 0}, "preference", nil, "")
	// bob 写一条不同的 preference
	m.StoreClassified("bob", "用户喜欢狗", 0.9, []float64{0, 1, 0}, "preference", nil, "")

	// 用一个对两条都有相似度的 query 试 alice 视角
	q := []float64{0.5, 0.5, 0}
	aliceHits := m.RecallByFilter("宠物", q, RecallFilter{UserID: "alice", TopK: 10, MinScore: 0})
	if len(aliceHits) != 1 || aliceHits[0].Content != "用户喜欢猫" {
		t.Fatalf("alice 应只看到自己的猫记忆, 得到 %+v", aliceHits)
	}
	bobHits := m.RecallByFilter("宠物", q, RecallFilter{UserID: "bob", TopK: 10, MinScore: 0})
	if len(bobHits) != 1 || bobHits[0].Content != "用户喜欢狗" {
		t.Fatalf("bob 应只看到自己的狗记忆, 得到 %+v", bobHits)
	}

	// 未登录调用（UserID 空）应得 0 条
	noUserHits := m.RecallByFilter("宠物", q, RecallFilter{TopK: 10, MinScore: 0})
	if len(noUserHits) != 0 {
		t.Errorf("UserID 空时不应返回任何条目, 得到 %d 条", len(noUserHits))
	}

	// 显式打开 IncludeAllUsers（仅 admin 审计端点）能看到全部
	allHits := m.RecallByFilter("宠物", q, RecallFilter{IncludeAllUsers: true, TopK: 10, MinScore: 0})
	if len(allHits) != 2 {
		t.Errorf("admin 视角应能看到全部 2 条, 得到 %d", len(allHits))
	}
}

// TestMultiUser_FilterByCategoryIsolated 验证 profile slot 路径也按 user 隔离。
func TestMultiUser_FilterByCategoryIsolated(t *testing.T) {
	m := New()
	m.StoreClassified("alice", "alice 是工程师", 0.9, nil, "identity", nil, "profile")
	m.StoreClassified("bob", "bob 是设计师", 0.9, nil, "identity", nil, "profile")

	aliceProfile := m.FilterByCategory("alice", []string{"identity"}, 10)
	if len(aliceProfile) != 1 || aliceProfile[0].Content != "alice 是工程师" {
		t.Errorf("alice profile 串了用户: %+v", aliceProfile)
	}

	// 空 userID 不返回任何条目（与 RecallByFilter 行为一致）
	if got := m.FilterByCategory("", []string{"identity"}, 10); len(got) != 0 {
		t.Errorf("UserID 空时 FilterByCategory 不应返回, 得到 %d", len(got))
	}
}

// TestMultiUser_DedupScoped 验证去重只在同一用户内生效——
// 否则 alice 写过 "我叫 Bob" 后，bob 真的写自己的名字会被 dedup 跳过。
func TestMultiUser_DedupScoped(t *testing.T) {
	m := New()
	m.SetConsolidationConfig(DefaultConsolidationConfig())

	// 同一内容、相同 emb，跨用户写入两次——必须各自存储
	emb := []float64{1, 0, 0}
	addedA := m.StoreClassified("alice", "用户叫 X", 0.9, emb, "identity", nil, "")
	addedB := m.StoreClassified("bob", "用户叫 X", 0.9, emb, "identity", nil, "")

	if !addedA || !addedB {
		t.Fatalf("跨用户相同内容应各自存储, 得到 alice=%v bob=%v", addedA, addedB)
	}
	if m.Count() != 2 {
		t.Errorf("应有 2 条独立条目, 得到 %d", m.Count())
	}
}

// TestMultiUser_ConflictCandidatesScoped 验证矛盾检测候选不会跨用户。
func TestMultiUser_ConflictCandidatesScoped(t *testing.T) {
	m := New()
	m.SetConsolidationConfig(DefaultConsolidationConfig())

	// alice 在北京
	m.StoreClassified("alice", "用户在北京", 0.9, []float64{1, 0}, "fact", nil, "")
	// bob 写一条与 alice 高度相似但属于他自己的事实
	m.StoreClassified("bob", "用户在上海", 0.9, []float64{0.95, 0.05}, "fact", nil, "")

	// alice 想写"用户在广州"——不应触发与 bob 上海的冲突
	cands := m.ConflictCandidates("alice", []float64{0.9, 0.1}, "fact", 0.5, 0.99)
	for _, c := range cands {
		if c.Item.UserID != "alice" {
			t.Errorf("跨用户候选不应出现: %+v", c.Item)
		}
	}
}
