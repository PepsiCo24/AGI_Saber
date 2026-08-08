package chat

import (
	"sync"
	"sync/atomic"
	"testing"

	"agi-assistant/config"
	"agi-assistant/internal/domain/memory/shortterm"
)

// newTestStack 构造一个无 hydrator 的 memoryStack——
// 大多数单测不需要预热，桶首次创建为空。
func newTestStack() *memoryStack {
	return newMemoryStack(&config.APIConfig{
		MemoryConfig: config.MemoryConfig{
			ShortTermMaxTurns: 5,
		},
	}, nil, nil)
}

// newTestStackWithHydrator 构造带可计数 hydrator 的 memoryStack——
// 用来测懒加载 + 单飞 + 内容正确性。
func newTestStackWithHydrator(stm STMHydrator, pref PrefHydrator) *memoryStack {
	return newMemoryStack(&config.APIConfig{
		MemoryConfig: config.MemoryConfig{
			ShortTermMaxTurns: 5,
		},
	}, stm, pref)
}

// TestSTM_PerUserBucket 验证不同用户的 STM 桶完全独立。
func TestSTM_PerUserBucket(t *testing.T) {
	m := newTestStack()

	alice := m.STM("alice")
	bob := m.STM("bob")
	if alice == nil || bob == nil {
		t.Fatal("STM 桶应被懒创建")
	}
	if alice == bob {
		t.Fatal("不同用户的 STM 桶不应是同一个指针——会跨用户串号")
	}

	alice.Add("user", "alice 的消息")
	bob.Add("user", "bob 的消息")

	if alice.Count() != 1 || bob.Count() != 1 {
		t.Errorf("各桶应各自有 1 条, alice=%d bob=%d", alice.Count(), bob.Count())
	}
	for _, m := range alice.Snapshot() {
		if m.Content == "bob 的消息" {
			t.Error("alice 桶里不应出现 bob 的消息")
		}
	}
}

// TestSTM_LazyCreate 验证再次调用同一 userID 拿到同一桶。
func TestSTM_LazyCreate(t *testing.T) {
	m := newTestStack()
	first := m.STM("alice")
	first.Add("user", "msg1")
	second := m.STM("alice")

	if first != second {
		t.Fatal("同一 userID 多次访问应返回同一桶")
	}
	if second.Count() != 1 {
		t.Errorf("第二次访问应保留状态, 得到 %d 条", second.Count())
	}
}

// TestSTM_AnonymousReturnsNil 验证 userID 空返回 nil。
func TestSTM_AnonymousReturnsNil(t *testing.T) {
	m := newTestStack()
	if got := m.STM(""); got != nil {
		t.Error("userID=\"\" 时 STM 应返回 nil")
	}
}

// TestSTM_ConcurrentCreate 验证并发首次访问同一 userID 不会重复创建桶。
func TestSTM_ConcurrentCreate(t *testing.T) {
	m := newTestStack()
	const N = 50
	results := make(chan interface{}, N)
	for i := 0; i < N; i++ {
		go func() { results <- m.STM("racer") }()
	}
	first := <-results
	for i := 1; i < N; i++ {
		got := <-results
		if got != first {
			t.Fatalf("并发首次访问应返回同一桶, 第 %d 个不同", i)
		}
	}
}

// TestSTM_HydrateOnFirstAccess 验证首次访问时 hydrator 被调用并把历史灌入桶。
func TestSTM_HydrateOnFirstAccess(t *testing.T) {
	calls := int32(0)
	hydrator := func(userID string) []shortterm.ConversationMessage {
		atomic.AddInt32(&calls, 1)
		if userID == "alice" {
			return []shortterm.ConversationMessage{
				{Role: "user", Content: "你好"},
				{Role: "assistant", Content: "你也好"},
			}
		}
		return nil
	}
	m := newTestStackWithHydrator(hydrator, nil)

	alice := m.STM("alice")
	if alice.Count() != 2 {
		t.Errorf("alice 桶应预热 2 条, 得到 %d", alice.Count())
	}
	if calls != 1 {
		t.Errorf("hydrator 应被调用 1 次, 得到 %d", calls)
	}

	// 第二次访问不应再调 hydrator
	_ = m.STM("alice")
	if calls != 1 {
		t.Errorf("二次访问 hydrator 应保持 1 次, 得到 %d", calls)
	}
}

// TestSTM_HydrateRespectsWindow 验证 hydrator 给的条数超过窗口时被自动截断。
func TestSTM_HydrateRespectsWindow(t *testing.T) {
	// MaxTurns=5 → 窗口 10 条；hydrator 给 20 条，应只保留最新 10 条
	hydrator := func(userID string) []shortterm.ConversationMessage {
		out := make([]shortterm.ConversationMessage, 20)
		for i := 0; i < 20; i++ {
			out[i] = shortterm.ConversationMessage{Role: "user", Content: string(rune('a' + i))}
		}
		return out
	}
	m := newTestStackWithHydrator(hydrator, nil)

	alice := m.STM("alice")
	if alice.Count() != 10 {
		t.Errorf("应被窗口截到 10 条, 得到 %d", alice.Count())
	}
	// 应保留最新 10 条（索引 10..19，内容 'k'..'t'）
	first := alice.Snapshot()[0]
	if first.Content != "k" {
		t.Errorf("截断后首条应是 'k', 得到 %q", first.Content)
	}
}

// TestSTM_HydratorFailureOK 验证 hydrator 返回 nil 时桶仍被创建为空。
func TestSTM_HydratorFailureOK(t *testing.T) {
	hydrator := func(userID string) []shortterm.ConversationMessage { return nil }
	m := newTestStackWithHydrator(hydrator, nil)

	alice := m.STM("alice")
	if alice == nil {
		t.Fatal("hydrator 返 nil 时桶仍应被创建（空桶）")
	}
	if alice.Count() != 0 {
		t.Errorf("hydrator 返 nil 时桶应为空, 得到 %d", alice.Count())
	}
	// 后续 Add 应正常工作
	alice.Add("user", "first message")
	if alice.Count() != 1 {
		t.Errorf("Add 后应有 1 条, 得到 %d", alice.Count())
	}
}

// TestSTM_HydrateOnceUnderConcurrency 验证 N 个 goroutine 并发首次访问只触发 1 次 hydrate。
//
// 这是 cache-aside + write-through 模式的关键不变量：高并发下不能 N 次打 PG，
// 否则首次活跃高峰期会冲爆数据库。
func TestSTM_HydrateOnceUnderConcurrency(t *testing.T) {
	calls := int32(0)
	hydrator := func(userID string) []shortterm.ConversationMessage {
		atomic.AddInt32(&calls, 1)
		return []shortterm.ConversationMessage{{Role: "user", Content: "hi"}}
	}
	m := newTestStackWithHydrator(hydrator, nil)

	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_ = m.STM("racer")
		}()
	}
	wg.Wait()

	if calls != 1 {
		t.Errorf("并发首访 hydrator 应只调 1 次, 得到 %d", calls)
	}
}

// TestPref_PerUserBucket 验证 Preference 也按 userID 分桶。
func TestPref_PerUserBucket(t *testing.T) {
	m := newTestStack()

	alice := m.Pref("alice")
	bob := m.Pref("bob")
	if alice == nil || bob == nil {
		t.Fatal("Preference 桶应被懒创建")
	}
	if alice == bob {
		t.Fatal("不同用户的 Preference 桶不应共用")
	}

	alice.Save("姓名", "Alice")
	bob.Save("姓名", "Bob")

	if v, _ := alice.Get("姓名"); v != "Alice" {
		t.Errorf("alice.姓名 期望 Alice, 得到 %q", v)
	}
	if v, _ := bob.Get("姓名"); v != "Bob" {
		t.Errorf("bob.姓名 期望 Bob, 得到 %q", v)
	}
}

// TestPref_AnonymousReturnsNil 验证空 userID 返回 nil + snapshot 返空 map。
func TestPref_AnonymousReturnsNil(t *testing.T) {
	m := newTestStack()
	if got := m.Pref(""); got != nil {
		t.Error("userID=\"\" 时 Pref 应返回 nil")
	}
	if got := m.prefSnapshot(""); len(got) != 0 {
		t.Errorf("prefSnapshot(\"\") 应返回空 map, 得到 %v", got)
	}
}

// TestPref_HydrateOnFirstAccess 验证 Preference 桶懒加载 PG 偏好。
func TestPref_HydrateOnFirstAccess(t *testing.T) {
	hydrator := func(userID string) map[string]string {
		if userID == "alice" {
			return map[string]string{"姓名": "Alice", "城市": "北京"}
		}
		return nil
	}
	m := newTestStackWithHydrator(nil, hydrator)

	alice := m.Pref("alice")
	if v, _ := alice.Get("姓名"); v != "Alice" {
		t.Errorf("应预热 alice.姓名=Alice, 得到 %q", v)
	}
	if v, _ := alice.Get("城市"); v != "北京" {
		t.Errorf("应预热 alice.城市=北京, 得到 %q", v)
	}
}

// TestStmCount_Anonymous 验证空 userID 调 stmCount 返 0 而非 panic。
func TestStmCount_Anonymous(t *testing.T) {
	m := newTestStack()
	if got := m.stmCount(""); got != 0 {
		t.Errorf("stmCount(\"\") 期望 0, 得到 %d", got)
	}
}
