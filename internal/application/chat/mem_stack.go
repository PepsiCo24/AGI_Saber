// mem_stack.go — 三层记忆 + 偏好的聚合容器（多租户分桶 + 懒加载预热版）。
//
// 设计：
//   - stm / pref 按 userID 分桶——隔离跨用户串号
//   - 桶在用户**首次活动**时懒创建，并从 PG 预热历史（cache-aside 模式）
//   - 写入路径走 write-through：内存桶 + PG 双写（写入侧由调用方负责）
//   - hydrator 通过闭包注入——domain/application 层不直接 import infra
//
// 为什么不全量启动 restore：
//   - 一万个用户启动期串行 SELECT 太慢
//   - 大部分用户当天不活跃，预加载内存浪费
//   - 懒加载只为活跃用户付代价
//
// 为什么不每次走 PG 直查：
//   - 一次 chat 多次访问 STM（prepare 写、ctx_builder 读、finalize 写、status 计数）
//   - PG 往返累计上来不便宜（虽然单次 ms 级），且加重 PG 负担
//   - 内存桶承担"请求级别短期缓存"角色
//
// graphMem 是延后注入的：initKnowledgeGraph 在 New 末尾才创建，期间 graphMem 为 nil。
// 调用方需要 nil 检查（与重构前一致）。
package chat

import (
	"sync"

	"agi-assistant/config"
	graphmem "agi-assistant/internal/domain/memory/graph"
	"agi-assistant/internal/domain/memory/longterm"
	"agi-assistant/internal/domain/memory/preference"
	"agi-assistant/internal/domain/memory/shortterm"
)

// STMHydrator 接收 userID，返回该用户最近 N 条历史消息（按时间正序）。
// 应实现：从 chat_history 表读最近 ShortTermMaxTurns*2 条，按时间正序返回。
// 失败 / 数据缺失时返回 nil 即可——首次桶仍会创建为空，后续 Add 正常工作。
type STMHydrator func(userID string) []shortterm.ConversationMessage

// PrefHydrator 接收 userID，返回该用户的偏好键值对快照。
// 失败 / 数据缺失时返回 nil 即可。
type PrefHydrator func(userID string) map[string]string

// memoryStack 聚合三层记忆 + 用户偏好。
//
// stm/pref 字段保留为 nil 占位——所有访问必须走 STM(userID) / Pref(userID)
// 工厂方法。这强制所有调用点显式带上 userID，避免回到"全局单例串号"。
type memoryStack struct {
	stmMaxTurns int

	stmMu       sync.RWMutex
	stmByUser   map[string]*shortterm.ShortTerm
	stmHydrator STMHydrator

	prefMu       sync.RWMutex
	prefByUser   map[string]*preference.Preference
	prefHydrator PrefHydrator

	// ltm / graphMem 仍单例：condense 出的 Items 列表已经按 UserID 字段
	// 隔离，召回时 RecallByFilter 强制过滤
	ltm      *longterm.LongTerm
	graphMem *graphmem.GraphMemory // 启动期为 nil，initKnowledgeGraph 中赋值
}

// newMemoryStack 创建并配置记忆栈。
//
// hydrator 参数是可选的——nil 时桶首次创建为空（适合单测 / CLI 等无 PG 场景）。
// 生产路径上 main 会传入实际的 chathistory.Load / pref.Load 闭包。
func newMemoryStack(cfg *config.APIConfig, stmHydrator STMHydrator, prefHydrator PrefHydrator) *memoryStack {
	ltm := longterm.New()
	ltm.SetConsolidationConfig(&longterm.ConsolidationConfig{
		SimilarityThreshold: cfg.MemoryConsolidationSimilarity,
		DedupThreshold:      cfg.MemoryConsolidationDedup,
		TTLDays:             cfg.MemoryConsolidationTTLDays,
		DecayRate:           cfg.MemoryConsolidationDecayRate,
		MinImportance:       cfg.MemoryConsolidationMinImport,
		TriggerInterval:     cfg.MemoryConsolidationTrigger,
	})
	return &memoryStack{
		stmMaxTurns:  cfg.ShortTermMaxTurns,
		stmByUser:    make(map[string]*shortterm.ShortTerm),
		stmHydrator:  stmHydrator,
		prefByUser:   make(map[string]*preference.Preference),
		prefHydrator: prefHydrator,
		ltm:          ltm,
	}
}

// attachGraph 在 KGStore 就绪后注入图增强记忆层
func (m *memoryStack) attachGraph(g *graphmem.GraphMemory) {
	m.graphMem = g
}

// ─────────────────────────── STM 分桶 + 懒加载 ───────────────────────────

// STM 取 userID 对应的短期记忆桶。
//   - 首次访问：双 check 升级到写锁创建桶 → 调 hydrator 从 PG 预热 → 灌入桶
//   - 后续访问：走读锁快路径直接命中
//
// 同一用户并发首次访问只会触发一次 hydrate（双 check 保护）。
// hydrator 失败返回 nil 时桶仍被创建为空，调用方继续 Add 正常工作。
//
// userID 为空时返回 nil——调用方应自行 nil 守卫，避免向"未登录请求"写入桶。
func (m *memoryStack) STM(userID string) *shortterm.ShortTerm {
	if userID == "" {
		return nil
	}
	m.stmMu.RLock()
	if s, ok := m.stmByUser[userID]; ok {
		m.stmMu.RUnlock()
		return s
	}
	m.stmMu.RUnlock()

	m.stmMu.Lock()
	defer m.stmMu.Unlock()
	if s, ok := m.stmByUser[userID]; ok {
		return s // 双 check 防并发首次访问重复创建 + 重复 hydrate
	}

	s := shortterm.New(m.stmMaxTurns)
	// 注意：hydrate 在持写锁内完成——确保"桶建好"和"历史灌入"对其他读者是原子的；
	// 否则可能出现"看到空桶但 hydrate 还在跑"的窗口
	if m.stmHydrator != nil {
		if hist := m.stmHydrator(userID); len(hist) > 0 {
			s.Hydrate(toSTMMessages(hist))
		}
	}
	m.stmByUser[userID] = s
	return s
}

// toSTMMessages 是 hydrator 输出与 ShortTerm.Hydrate 入参之间的标识转换 hook。
// 当前 hydrator 直接返回 ConversationMessage，这里就是个透传——
// 留这层是为将来 hydrator 升级（比如返回 chathistory.Entry 时方便适配）。
func toSTMMessages(in []shortterm.ConversationMessage) []shortterm.ConversationMessage {
	return in
}

// stmCount 返回某用户 STM 当前消息数；nil 桶按 0 处理便于上层无脑调用。
func (m *memoryStack) stmCount(userID string) int {
	s := m.STM(userID)
	if s == nil {
		return 0
	}
	return s.Count()
}

// ─────────────────────────── Preference 分桶 + 懒加载 ───────────────────────────

// Pref 取 userID 对应的偏好桶；与 STM 同样的双 check + 首次 hydrate 模式。
func (m *memoryStack) Pref(userID string) *preference.Preference {
	if userID == "" {
		return nil
	}
	m.prefMu.RLock()
	if p, ok := m.prefByUser[userID]; ok {
		m.prefMu.RUnlock()
		return p
	}
	m.prefMu.RUnlock()

	m.prefMu.Lock()
	defer m.prefMu.Unlock()
	if p, ok := m.prefByUser[userID]; ok {
		return p
	}

	p := preference.New()
	if m.prefHydrator != nil {
		if kvs := m.prefHydrator(userID); len(kvs) > 0 {
			p.SaveBatch(kvs)
		}
	}
	m.prefByUser[userID] = p
	return p
}

// prefSnapshot 返回某用户偏好的浅拷贝；空桶 / 未登录返回空 map（不返 nil 便于 JSON 序列化）。
func (m *memoryStack) prefSnapshot(userID string) map[string]string {
	p := m.Pref(userID)
	if p == nil {
		return map[string]string{}
	}
	return p.Snapshot()
}
