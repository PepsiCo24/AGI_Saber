// Package longterm 长期记忆：支持语义向量召回（embedding 优先）或 TF 词袋降级。
//
// 核心特性：
//   - StoreClassified  写入前自动 dedup（cosine ≥ DedupThreshold）
//   - RecallByFilter   按 Schema-driven 过滤条件做语义召回
//   - Consolidate      周期性合并：衰减 → 去重/合并 → 过期淘汰
//
// Item 含 Category / Tags / SlotHint 三个字段，与 promptctx 的 SlotFilter 对齐——
// runtime 装配时按这些字段筛选适合各槽位的记忆，避免 Top-K 召回污染 prompt。
//
// 并发安全：内置 sync.RWMutex 串行化所有读写。Snapshot() 返回只读副本。
package longterm

import (
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"agi-assistant/internal/domain/memory/consistency"
)

// Item 是长期记忆的存储单元
type Item struct {
	ID           int       `json:"id"`
	Content      string    `json:"content"`
	Importance   float64   `json:"importance"` // 0~1，越高越重要
	Embedding    []float64 `json:"embedding,omitempty"`
	Score        float64   `json:"score,omitempty"` // 召回时的综合得分（不持久化）
	CreatedAt    time.Time `json:"created_at"`
	LastAccessed time.Time `json:"last_accessed"`
	// 多租户：每条记忆所属用户 ID（来自 JWT.subject）。RecallByFilter 强制按此过滤，
	// 不传 UserID 的召回会得到空结果——避免 V1 升级期间出现"忘传 ID 就泄漏所有用户"的灾难。
	// 老数据（迁移前 'legacy'）仍能被 user_id="legacy" 显式查询到，便于人工审计。
	UserID string `json:"user_id,omitempty"`
	// Schema-driven 装配字段（promptctx 包按这些字段过滤）
	Category string   `json:"category,omitempty"`  // 主类别：identity / preference / fact / episodic / tool_failure / policy / general
	Tags     []string `json:"tags,omitempty"`      // 自由标签
	SlotHint string   `json:"slot_hint,omitempty"` // 建议归属的 SlotKind 字符串
	// 安全字段：被 poison detector 或人工标记隔离的条目，
	// 物理上保留在内存/PG 中（便于审计），但 RecallByFilter 默认过滤不召回。
	// 不读不删的"软删除"——攻击发生后还能查到证据，并通过 Unquarantine 恢复。
	Quarantined      bool   `json:"quarantined,omitempty"`
	QuarantineReason string `json:"quarantine_reason,omitempty"`
	// 矛盾治理字段：当用户陈述与已有 identity/preference/fact 类记忆冲突时，
	// 旧条目标 Superseded=true（不删除，便于审计与回滚），新条目通过
	// Supersedes 数组记录"我替代了哪些旧条目"。RecallByFilter 默认过滤 Superseded。
	Superseded   bool      `json:"superseded,omitempty"`
	SupersededAt time.Time `json:"superseded_at,omitempty"`
	Supersedes   []int     `json:"supersedes,omitempty"`
	// Projection metadata comes from PostgreSQL, the sole source of truth.
	// Legacy restored rows with Version=0 are normalized to version 1.
	Version     int64      `json:"version,omitempty"`
	ContentHash string     `json:"content_hash,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at,omitempty"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
}

// RecallFilter 控制 RecallByFilter 的语义召回约束（与 promptctx.SlotFilter 同构）
type RecallFilter struct {
	// UserID 强制——空字符串将不召回任何条目（防忘传导致跨用户泄漏）。
	// 通过此设计把"多租户隔离"从 application 层下沉到 domain 层强制实施。
	UserID      string
	Categories  []string
	RequireTags []string
	MinScore    float64
	TopK        int
	MaxAgeHours int
	// IncludeQuarantined 默认 false：被隔离条目不会被召回（即不会注入 prompt）。
	// 仅审计 / 调试用途下允许置为 true（如 /api/memory/audit 端点）。
	IncludeQuarantined bool
	// IncludeSuperseded 默认 false：被新条目取代的历史记忆不参与召回，
	// 避免"用户已搬到上海"的事实被旧的"在北京"覆盖。审计端点可显式打开。
	IncludeSuperseded bool
	// IncludeAllUsers 仅 admin 审计端点用——置 true 可越过 UserID 过滤看全量数据。
	// 默认 false 时 UserID="" 等价于"看不到任何条目"。
	IncludeAllUsers bool
}

// ConsolidationConfig 记忆合并配置
type ConsolidationConfig struct {
	SimilarityThreshold float64 // 合并相似度阈值 (0~1)，超过此值触发合并
	DedupThreshold      float64 // 去重相似度阈值 (0~1)，超过此值视为重复
	TTLDays             int     // 过期天数 (0=永不过期)
	DecayRate           float64 // 每日衰减系数 (0~1, 如 0.995 表示每天保留 99.5%)
	MinImportance       float64 // 低于此重要性且超 TTL 的条目会被淘汰
	TriggerInterval     int     // 每存入 N 条新记忆后触发合并
	// ProtectFn 是删除前的保护钩子。给定 candidates（即将物理删除的 ID 集合），
	// 返回其中需要保留的 ID 子集。GraphMemory 注入入度高的节点保护规则，
	// 让保护决策与物理删除发生在同一临界区，避免内存/PG 不一致。
	ProtectFn func(candidates []int) (protected []int)
}

// DefaultConsolidationConfig 返回默认合并配置
func DefaultConsolidationConfig() *ConsolidationConfig {
	return &ConsolidationConfig{
		SimilarityThreshold: 0.80,
		DedupThreshold:      0.95,
		TTLDays:             30,
		DecayRate:           0.995,
		MinImportance:       0.3,
		TriggerInterval:     5,
	}
}

// ConsolidationResult 记忆合并结果
type ConsolidationResult struct {
	Deduped      int           // 去重删除的条目数
	Merged       int           // 合并的条目数
	Expired      int           // 过期删除的条目数
	DeleteFromDB []int         // 需要从 PG 删除的 ID 列表
	UpdateInDB   []Item        // 需要在 PG 更新的条目列表（合并产物）
	DecayUpdates []DecayUpdate // 仅 importance 变化的批量更新（Phase 1 衰减）
}

// DecayUpdate 衰减阶段单条 importance 变更
type DecayUpdate struct {
	ID         int
	Importance float64
}

// LongTerm 支持语义向量召回（embedding 优先）或 TF 词袋降级
type LongTerm struct {
	mu               sync.RWMutex
	Items            []Item
	vocabID          map[string]int
	vocab            []string
	nextID           int
	storeCount       int
	consolidationCfg *ConsolidationConfig
}

var (
	ErrStaleCommittedVersion = errors.New("longterm: stale committed version")
	ErrCommittedHashConflict = errors.New("longterm: equal version has different content hash")
)

// New 创建长期记忆
func New() *LongTerm {
	return &LongTerm{vocabID: make(map[string]int)}
}

// SetConsolidationConfig 设置合并配置
func (m *LongTerm) SetConsolidationConfig(cfg *ConsolidationConfig) {
	m.mu.Lock()
	m.consolidationCfg = cfg
	m.mu.Unlock()
}

// ConsolidationCfg 返回当前合并配置（指针，调用方修改字段后需再调 SetConsolidationConfig
// 才能保证 happens-before；GraphMemory 仅在初始化时挂 ProtectFn 后调用一次）
func (m *LongTerm) ConsolidationCfg() *ConsolidationConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.consolidationCfg
}

// Snapshot 返回 Items 的只读副本，调用方可安全遍历不被并发写入打断
func (m *LongTerm) Snapshot() []Item {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := make([]Item, len(m.Items))
	copy(cp, m.Items)
	return cp
}

// Count 返回当前条目数（持锁读）
func (m *LongTerm) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.Items)
}

// LastID 返回最后一条记忆的 ID；空时返回 -1
func (m *LongTerm) LastID() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.Items) == 0 {
		return -1
	}
	return m.Items[len(m.Items)-1].ID
}

// LastItem 返回最后一条记忆的副本和 ok 标记
func (m *LongTerm) LastItem() (Item, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.Items) == 0 {
		return Item{}, false
	}
	return m.Items[len(m.Items)-1], true
}

// FindByID 按 ID 查找记忆，返回副本和 ok 标记（持读锁）
func (m *LongTerm) FindByID(id int) (Item, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, it := range m.Items {
		if it.ID == id {
			return it, true
		}
	}
	return Item{}, false
}

// ApplyCommitted applies a PostgreSQL-committed change set atomically to the
// active cache. It never accepts an older version or conflicting equal version.
func (m *LongTerm) ApplyCommitted(changes consistency.CommittedChangeSet) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	index := make(map[int]int, len(m.Items))
	for i := range m.Items {
		index[m.Items[i].ID] = i
	}
	validate := func(record consistency.MemoryRecord) error {
		i, ok := index[int(record.ID)]
		if !ok {
			return nil
		}
		current := m.Items[i]
		currentVersion := current.Version
		if currentVersion <= 0 {
			currentVersion = 1
		}
		if record.Version < currentVersion {
			return ErrStaleCommittedVersion
		}
		if record.Version == currentVersion &&
			current.ContentHash != "" && record.ContentHash != "" &&
			current.ContentHash != record.ContentHash {
			return ErrCommittedHashConflict
		}
		return nil
	}
	for _, record := range changes.Upserts {
		if err := validate(record); err != nil {
			return err
		}
	}
	for _, record := range changes.Deletes {
		if err := validate(record); err != nil {
			return err
		}
	}

	for _, record := range changes.Upserts {
		if record.DeletedAt != nil {
			continue
		}
		item := recordToItem(record)
		if i, ok := index[item.ID]; ok {
			m.Items[i] = item
		} else {
			index[item.ID] = len(m.Items)
			m.Items = append(m.Items, item)
			m.storeCount++
		}
	}
	if len(changes.Deletes) > 0 {
		deleted := make(map[int]bool, len(changes.Deletes))
		for _, record := range changes.Deletes {
			deleted[int(record.ID)] = true
		}
		active := m.Items[:0]
		for _, item := range m.Items {
			if !deleted[item.ID] {
				active = append(active, item)
			}
		}
		m.Items = active
	}
	m.rebuildVocab()
	m.recomputeNextID()
	return nil
}

// ReplaceCommitted discards the local cache and rebuilds it from authoritative
// PostgreSQL records. Tombstones are intentionally excluded from active recall.
func (m *LongTerm) ReplaceCommitted(records []consistency.MemoryRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Items = m.Items[:0]
	for _, record := range records {
		if record.DeletedAt != nil {
			continue
		}
		m.Items = append(m.Items, recordToItem(record))
	}
	sort.Slice(m.Items, func(i, j int) bool { return m.Items[i].ID < m.Items[j].ID })
	m.rebuildVocab()
	m.recomputeNextID()
	m.storeCount = 0
}

func (m *LongTerm) recomputeNextID() {
	m.nextID = 0
	for _, item := range m.Items {
		if item.ID >= m.nextID {
			m.nextID = item.ID + 1
		}
	}
}

func recordToItem(record consistency.MemoryRecord) Item {
	version := record.Version
	if version <= 0 {
		version = 1
	}
	var supersededAt time.Time
	if record.SupersededAt != nil {
		supersededAt = *record.SupersededAt
	}
	supersedes := make([]int, len(record.Supersedes))
	for i, id := range record.Supersedes {
		supersedes[i] = int(id)
	}
	return Item{
		ID:               int(record.ID),
		UserID:           record.UserID,
		Content:          record.Content,
		Importance:       record.Importance,
		Embedding:        append([]float64(nil), record.Embedding...),
		CreatedAt:        record.CreatedAt,
		UpdatedAt:        record.UpdatedAt,
		LastAccessed:     record.LastAccessed,
		Category:         record.Category,
		Tags:             append([]string(nil), record.Tags...),
		SlotHint:         record.SlotHint,
		Quarantined:      record.Quarantined,
		QuarantineReason: record.QuarantineReason,
		Superseded:       record.Superseded,
		SupersededAt:     supersededAt,
		Supersedes:       supersedes,
		Version:          version,
		ContentHash:      record.ContentHash,
		DeletedAt:        record.DeletedAt,
	}
}

func itemToRecord(item Item) consistency.MemoryRecord {
	supersedes := make([]int64, len(item.Supersedes))
	for i, id := range item.Supersedes {
		supersedes[i] = int64(id)
	}
	var supersededAt *time.Time
	if !item.SupersededAt.IsZero() {
		value := item.SupersededAt
		supersededAt = &value
	}
	version := item.Version
	if version <= 0 {
		version = 1
	}
	return consistency.MemoryRecord{
		ID:               int64(item.ID),
		UserID:           item.UserID,
		Content:          item.Content,
		Importance:       item.Importance,
		Embedding:        append([]float64(nil), item.Embedding...),
		Category:         item.Category,
		Tags:             append([]string(nil), item.Tags...),
		SlotHint:         item.SlotHint,
		Version:          version,
		ContentHash:      item.ContentHash,
		CreatedAt:        item.CreatedAt,
		UpdatedAt:        item.UpdatedAt,
		LastAccessed:     item.LastAccessed,
		DeletedAt:        item.DeletedAt,
		Quarantined:      item.Quarantined,
		QuarantineReason: item.QuarantineReason,
		Superseded:       item.Superseded,
		SupersededAt:     supersededAt,
		Supersedes:       supersedes,
	}
}

func (m *LongTerm) buildVocab(text string) {
	for _, t := range Tokenize(text) {
		if _, ok := m.vocabID[t]; !ok {
			m.vocabID[t] = len(m.vocab)
			m.vocab = append(m.vocab, t)
		}
	}
}

func (m *LongTerm) textToVector(text string) []float64 {
	vec := make([]float64, len(m.vocabID))
	for _, t := range Tokenize(text) {
		if idx, ok := m.vocabID[t]; ok {
			vec[idx]++
		}
	}
	return vec
}

// Store 将内容存入长期记忆（embedding 可选，传 nil 则使用 TF 降级）
// 返回 true 表示新增成功，false 表示因去重而跳过。
//
// userID 是多租户隔离主键——空字符串退化为 "legacy"（迁移期兼容），
// 强制让所有写入都打上归属。
func (m *LongTerm) Store(userID, content string, importance float64, embedding []float64) bool {
	return m.StoreClassified(userID, content, importance, embedding, "general", nil, "")
}

// StoreClassified 与 Store 行为相同，但额外记录 category/tags/slot_hint
// 用于 Schema-driven 槽位装配过滤。userID 同上。
func (m *LongTerm) StoreClassified(userID, content string, importance float64, embedding []float64,
	category string, tags []string, slotHint string) bool {
	if userID == "" {
		userID = "legacy"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// 去重检测：仅与同 userID 的现有条目对比——不同用户即使内容一样也独立存储
	if m.consolidationCfg != nil && len(m.Items) > 0 && len(embedding) > 0 {
		for i := range m.Items {
			if m.Items[i].UserID != userID {
				continue
			}
			if len(m.Items[i].Embedding) == len(embedding) {
				sim := Cosine(embedding, m.Items[i].Embedding)
				if sim >= m.consolidationCfg.DedupThreshold {
					if importance > m.Items[i].Importance {
						m.Items[i].Importance = importance
					}
					m.Items[i].LastAccessed = time.Now()
					// 命中已有条目：若新分类更具体则覆盖（general 视为最弱）
					if category != "" && (m.Items[i].Category == "" || m.Items[i].Category == "general") {
						m.Items[i].Category = category
					}
					if slotHint != "" && m.Items[i].SlotHint == "" {
						m.Items[i].SlotHint = slotHint
					}
					if len(tags) > 0 {
						m.Items[i].Tags = mergeTags(m.Items[i].Tags, tags)
					}
					return false
				}
			}
		}
	}

	m.buildVocab(content)
	now := time.Now()
	if category == "" {
		category = "general"
	}
	m.Items = append(m.Items, Item{
		ID:           m.nextID,
		UserID:       userID,
		Content:      content,
		Importance:   importance,
		Embedding:    embedding,
		CreatedAt:    now,
		LastAccessed: now,
		Category:     category,
		Tags:         tags,
		SlotHint:     slotHint,
	})
	m.nextID++
	m.storeCount++
	return true
}

// mergeTags 合并两个标签列表去重，保持顺序
func mergeTags(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, t := range append(a, b...) {
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// StoreItem 直接插入已有 Item（用于从 DB 恢复数据）
func (m *LongTerm) StoreItem(item Item) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buildVocab(item.Content)
	if item.ID >= m.nextID {
		m.nextID = item.ID + 1
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	if item.LastAccessed.IsZero() {
		item.LastAccessed = item.CreatedAt
	}
	m.Items = append(m.Items, item)
}

// NeedConsolidation 检查是否需要触发记忆合并
func (m *LongTerm) NeedConsolidation() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.consolidationCfg != nil &&
		m.consolidationCfg.TriggerInterval > 0 &&
		m.storeCount >= m.consolidationCfg.TriggerInterval
}

// Recall 从长期记忆中召回与 query 最相关的 topK 条
// 优先使用 embedding 余弦相似度，若无 embedding 则退回 TF
// 只返回综合得分超过 threshold 的条目，避免注入噪声。
// userID 强制——空字符串将不返回任何条目。
func (m *LongTerm) Recall(userID, query string, topK int, queryEmbedding []float64) []Item {
	return m.RecallByFilter(query, queryEmbedding, RecallFilter{UserID: userID, TopK: topK, MinScore: 0.4})
}

// RecallByFilter 按 Schema-driven 过滤条件做语义召回
//   - filter.Categories 非空时，只返回 Category 命中其一的条目
//   - filter.RequireTags 非空时，条目必须包含全部标签
//   - filter.MaxAgeHours > 0 时，超龄条目被过滤
//   - filter.MinScore 控制综合分阈值（默认 0.4）
//   - filter.TopK 控制返回数量（0 表示不截断）
func (m *LongTerm) RecallByFilter(query string, queryEmbedding []float64, filter RecallFilter) []Item {
	// 写路径：召回过程中会更新 LastAccessed + 在 TF 兜底时增量 buildVocab
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.Items) == 0 {
		return nil
	}
	threshold := filter.MinScore
	if threshold <= 0 {
		threshold = 0.4
	}
	now := time.Now()

	// 仅在确实走 TF 兜底时（query 没 embedding 或维度不匹配）才需要扩词表
	useTF := len(queryEmbedding) == 0
	if useTF {
		m.buildVocab(query)
	}

	type scored struct {
		item Item
		s    float64
	}
	var items []scored
	for i := range m.Items {
		// 多租户隔离（默认强制）：UserID 为空且未显式打开 IncludeAllUsers 时，
		// 直接返回空——避免 application 层忘传 UserID 时跨用户泄漏。
		if !filter.IncludeAllUsers {
			if filter.UserID == "" {
				return nil
			}
			if m.Items[i].UserID != filter.UserID {
				continue
			}
		}
		// 隔离过滤（默认开启）：被 poison detector / 人工标记的不召回，
		// 但仍然保留在 m.Items 中便于审计端点查看。
		if m.Items[i].Quarantined && !filter.IncludeQuarantined {
			continue
		}
		// Superseded 过滤（默认开启）：已被新条目取代的不再注入 prompt。
		if m.Items[i].Superseded && !filter.IncludeSuperseded {
			continue
		}
		// 类别过滤
		if len(filter.Categories) > 0 && !containsString(filter.Categories, m.Items[i].Category) {
			continue
		}
		// 标签过滤
		if len(filter.RequireTags) > 0 && !containsAllTags(m.Items[i].Tags, filter.RequireTags) {
			continue
		}
		// 年龄过滤
		if filter.MaxAgeHours > 0 {
			if now.Sub(m.Items[i].CreatedAt).Hours() > float64(filter.MaxAgeHours) {
				continue
			}
		}

		var sim float64
		if !useTF && len(m.Items[i].Embedding) == len(queryEmbedding) {
			sim = Cosine(queryEmbedding, m.Items[i].Embedding)
		} else {
			qv := m.textToVector(query)
			iv := m.textToVector(m.Items[i].Content)
			if len(qv) < len(iv) {
				qv = append(qv, make([]float64, len(iv)-len(qv))...)
			} else if len(iv) < len(qv) {
				iv = append(iv, make([]float64, len(qv)-len(iv))...)
			}
			sim = Cosine(qv, iv)
		}
		s := sim*0.7 + m.Items[i].Importance*0.3
		if s >= threshold {
			m.Items[i].LastAccessed = now
			items = append(items, scored{item: m.Items[i], s: s})
		}
	}
	if len(items) == 0 {
		return nil
	}
	sort.Slice(items, func(i, j int) bool { return items[i].s > items[j].s })
	topK := filter.TopK
	if topK > 0 && topK < len(items) {
		items = items[:topK]
	}
	result := make([]Item, len(items))
	for i := range result {
		result[i] = items[i].item
		result[i].Score = items[i].s
	}
	return result
}

// containsString 简单 slice 包含检查
func containsString(slice []string, target string) bool {
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}

// ContainsString 暴露给外部子包（如 graph）使用
func ContainsString(slice []string, target string) bool { return containsString(slice, target) }

// containsAllTags 检查 item 是否包含所有要求的标签
func containsAllTags(itemTags, required []string) bool {
	for _, r := range required {
		if !containsString(itemTags, r) {
			return false
		}
	}
	return true
}

// FilterByCategory 直接返回属于指定 category 的全部条目（不做语义召回）
// 用于 Profile 等结构化槽位的稳定枚举。
//
// userID 强制——空字符串将不返回任何条目（与 RecallByFilter 的隔离保持一致）。
// 隔离 / superseded 条目自动过滤。
func (m *LongTerm) FilterByCategory(userID string, categories []string, limit int) []Item {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if userID == "" || len(m.Items) == 0 || len(categories) == 0 {
		return nil
	}
	var result []Item
	for i := range m.Items {
		if m.Items[i].UserID != userID {
			continue
		}
		if m.Items[i].Quarantined || m.Items[i].Superseded {
			continue
		}
		if containsString(categories, m.Items[i].Category) {
			result = append(result, m.Items[i])
			if limit > 0 && len(result) >= limit {
				break
			}
		}
	}
	return result
}

// Quarantine 把指定 ID 的条目标记为隔离，附原因。
// 物理上不删除——便于事后审计 + 误判恢复。返回是否找到该条目。
func (m *LongTerm) Quarantine(id int, reason string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.Items {
		if m.Items[i].ID == id {
			m.Items[i].Quarantined = true
			m.Items[i].QuarantineReason = reason
			return true
		}
	}
	return false
}

// Unquarantine 解除隔离。审计端点 / 人工复查后调用。
func (m *LongTerm) Unquarantine(id int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.Items {
		if m.Items[i].ID == id {
			m.Items[i].Quarantined = false
			m.Items[i].QuarantineReason = ""
			return true
		}
	}
	return false
}

// QuarantinedItems 返回所有被隔离的条目（持读锁拷贝）。供审计 / 前端展示使用。
func (m *LongTerm) QuarantinedItems() []Item {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Item
	for _, it := range m.Items {
		if it.Quarantined {
			out = append(out, it)
		}
	}
	return out
}

// ─────────────────────────── 矛盾治理（V1）─────────────────────────────

// ConflictCandidate 是 ConflictCandidates 返回的"疑似冲突"单元。
// Score = cosine(emb, candidate.Embedding)；调用方按需用 LLM-judge 确认。
type ConflictCandidate struct {
	Item  Item
	Score float64
}

// ConflictCandidates 返回与 (emb, category) 在指定相似度区间内的候选条目。
//
// 设计意图：
//   - 与 Store 的 dedup 阈值（≥0.95）不同——这里筛的是"语义同主题但不到完全重复"，
//     正好是"我喜欢猫" vs "我不喜欢猫" 这种潜在矛盾的特征区间；
//   - 仅匹配同 category 的条目——identity 不会与 preference 冲突；
//   - 排除已 Superseded / Quarantined 的条目；
//   - domain 层只做"提名"，不做语义判断（LLM-judge 在 application 层）。
//
// minSim ≤ 0 时使用 0.75 默认下界；maxSim ≤ 0 时使用 dedup 阈值（默认 0.95）。
// userID 强制——空字符串返 nil（不会跨用户找冲突）。
func (m *LongTerm) ConflictCandidates(userID string, emb []float64, category string, minSim, maxSim float64) []ConflictCandidate {
	if userID == "" || len(emb) == 0 || category == "" {
		return nil
	}
	if minSim <= 0 {
		minSim = 0.75
	}
	if maxSim <= 0 {
		if m.consolidationCfg != nil {
			maxSim = m.consolidationCfg.DedupThreshold
		} else {
			maxSim = 0.95
		}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []ConflictCandidate
	for i := range m.Items {
		it := m.Items[i]
		if it.UserID != userID {
			continue
		}
		if it.Superseded || it.Quarantined {
			continue
		}
		if it.Category != category {
			continue
		}
		if len(it.Embedding) != len(emb) {
			continue
		}
		sim := Cosine(emb, it.Embedding)
		if sim >= minSim && sim < maxSim {
			out = append(out, ConflictCandidate{Item: it, Score: sim})
		}
	}
	return out
}

// MarkSuperseded 把一组旧条目标为 Superseded，并在新条目（newID）的
// Supersedes 数组里登记替代关系。原子操作（持写锁），保证审计可追溯。
//
// newID == 0 表示新条目尚未确定 ID（少见，调用方应在 Store 之后再调用本方法）。
// 返回真正被标记为 superseded 的旧条目 ID 列表（已存在的、未被排除的）。
func (m *LongTerm) MarkSuperseded(oldIDs []int, newID int) []int {
	if len(oldIDs) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	oldSet := make(map[int]bool, len(oldIDs))
	for _, id := range oldIDs {
		oldSet[id] = true
	}
	var marked []int
	for i := range m.Items {
		if oldSet[m.Items[i].ID] && !m.Items[i].Superseded {
			m.Items[i].Superseded = true
			m.Items[i].SupersededAt = now
			marked = append(marked, m.Items[i].ID)
		}
	}
	if newID > 0 && len(marked) > 0 {
		for i := range m.Items {
			if m.Items[i].ID == newID {
				m.Items[i].Supersedes = appendUniqueInt(m.Items[i].Supersedes, marked...)
				break
			}
		}
	}
	return marked
}

// SupersededItems 返回所有 Superseded=true 的条目（审计用）
func (m *LongTerm) SupersededItems() []Item {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Item
	for _, it := range m.Items {
		if it.Superseded {
			out = append(out, it)
		}
	}
	return out
}

func appendUniqueInt(s []int, vals ...int) []int {
	seen := make(map[int]bool, len(s))
	for _, x := range s {
		seen[x] = true
	}
	for _, v := range vals {
		if !seen[v] {
			s = append(s, v)
			seen[v] = true
		}
	}
	return s
}

// PlanConsolidation computes the authoritative changes consolidation would
// make without mutating the active cache. Every change carries the version
// observed during planning so PostgreSQL can reject stale plans.
func (m *LongTerm) PlanConsolidation(now time.Time) consistency.ConsolidationPlan {
	m.mu.RLock()
	items := make([]Item, len(m.Items))
	for i := range m.Items {
		items[i] = m.Items[i]
		items[i].Embedding = append([]float64(nil), m.Items[i].Embedding...)
		items[i].Tags = append([]string(nil), m.Items[i].Tags...)
		items[i].Supersedes = append([]int(nil), m.Items[i].Supersedes...)
	}
	var cfg *ConsolidationConfig
	if m.consolidationCfg != nil {
		copyCfg := *m.consolidationCfg
		cfg = &copyCfg
	}
	m.mu.RUnlock()

	if cfg == nil || len(items) <= 1 {
		return consistency.ConsolidationPlan{}
	}
	removed := make(map[int]bool)
	reasons := make(map[int]string)
	updates := make(map[int]consistency.MemoryUpdate)

	const minDecayDelta = 0.01
	for i := range items {
		days := now.Sub(items[i].CreatedAt).Hours() / 24
		if days < 0 {
			days = 0
		}
		oldImportance := items[i].Importance
		items[i].Importance = oldImportance * math.Pow(cfg.DecayRate, days)
		if oldImportance-items[i].Importance >= minDecayDelta {
			record := itemToRecord(items[i])
			updates[items[i].ID] = consistency.MemoryUpdate{
				Record:          record,
				ExpectedVersion: normalizedVersion(items[i].Version),
			}
		}
	}

	for i := 0; i < len(items); i++ {
		if removed[i] {
			continue
		}
		for j := i + 1; j < len(items); j++ {
			if removed[j] || items[i].UserID != items[j].UserID {
				continue
			}
			similarity := itemSimilarityPure(items[i], items[j])
			switch {
			case similarity >= cfg.DedupThreshold:
				removeIndex := j
				if items[j].Importance >= items[i].Importance {
					removeIndex = i
				}
				removed[removeIndex] = true
				reasons[removeIndex] = "deduplicated"
				delete(updates, items[removeIndex].ID)
			case similarity >= cfg.SimilarityThreshold:
				survivorIndex, removedIndex := i, j
				if items[j].Importance > items[i].Importance {
					survivorIndex, removedIndex = j, i
				}
				items[survivorIndex] = mergeItemsPure(items[i], items[j], now)
				removed[removedIndex] = true
				reasons[removedIndex] = "merged"
				delete(updates, items[removedIndex].ID)
				updates[items[survivorIndex].ID] = consistency.MemoryUpdate{
					Record:          itemToRecord(items[survivorIndex]),
					ExpectedVersion: normalizedVersion(items[survivorIndex].Version),
				}
			}
			if removed[i] {
				break
			}
		}
	}

	for i := range items {
		if removed[i] {
			continue
		}
		days := now.Sub(items[i].CreatedAt).Hours() / 24
		if cfg.TTLDays > 0 && days > float64(cfg.TTLDays) &&
			items[i].Importance < cfg.MinImportance {
			removed[i] = true
			reasons[i] = "expired"
			delete(updates, items[i].ID)
		}
	}

	if cfg.ProtectFn != nil {
		var candidates []int
		for i := range items {
			if removed[i] {
				candidates = append(candidates, items[i].ID)
			}
		}
		protected := cfg.ProtectFn(candidates)
		protectedSet := make(map[int]bool, len(protected))
		for _, id := range protected {
			protectedSet[id] = true
		}
		for i := range items {
			if removed[i] && protectedSet[items[i].ID] {
				removed[i] = false
				delete(reasons, i)
			}
		}
	}

	plan := consistency.ConsolidationPlan{}
	for _, update := range updates {
		plan.Updates = append(plan.Updates, update)
	}
	sort.Slice(plan.Updates, func(i, j int) bool {
		return plan.Updates[i].Record.ID < plan.Updates[j].Record.ID
	})
	for i, isRemoved := range removed {
		if !isRemoved {
			continue
		}
		plan.Deletes = append(plan.Deletes, consistency.MemoryDelete{
			ID:              int64(items[i].ID),
			UserID:          items[i].UserID,
			ExpectedVersion: normalizedVersion(items[i].Version),
			Reason:          reasons[i],
		})
	}
	sort.Slice(plan.Deletes, func(i, j int) bool { return plan.Deletes[i].ID < plan.Deletes[j].ID })
	return plan
}

func normalizedVersion(version int64) int64 {
	if version <= 0 {
		return 1
	}
	return version
}

func itemSimilarityPure(a, b Item) float64 {
	if len(a.Embedding) > 0 && len(a.Embedding) == len(b.Embedding) {
		return Cosine(a.Embedding, b.Embedding)
	}
	vocabulary := make(map[string]int)
	for _, token := range append(Tokenize(a.Content), Tokenize(b.Content)...) {
		if _, ok := vocabulary[token]; !ok {
			vocabulary[token] = len(vocabulary)
		}
	}
	left := make([]float64, len(vocabulary))
	right := make([]float64, len(vocabulary))
	for _, token := range Tokenize(a.Content) {
		left[vocabulary[token]]++
	}
	for _, token := range Tokenize(b.Content) {
		right[vocabulary[token]]++
	}
	return Cosine(left, right)
}

func mergeItemsPure(a, b Item, now time.Time) Item {
	base, other := a, b
	if b.Importance > a.Importance {
		base, other = b, a
	}
	merged := base
	merged.Importance = math.Max(base.Importance, other.Importance)
	merged.LastAccessed = now
	if !strings.Contains(base.Content, other.Content) && !strings.Contains(other.Content, base.Content) {
		merged.Content = base.Content + "；" + other.Content
	} else if len(other.Content) > len(base.Content) {
		merged.Content = other.Content
	}
	if len(base.Embedding) > 0 && len(base.Embedding) == len(other.Embedding) {
		total := base.Importance + other.Importance
		if total > 0 {
			merged.Embedding = make([]float64, len(base.Embedding))
			for i := range base.Embedding {
				merged.Embedding[i] = (base.Embedding[i]*base.Importance + other.Embedding[i]*other.Importance) / total
			}
		}
	}
	merged.Tags = mergeTags(base.Tags, other.Tags)
	merged.Supersedes = appendUniqueInt(append([]int(nil), base.Supersedes...), other.ID)
	merged.Supersedes = appendUniqueInt(merged.Supersedes, other.Supersedes...)
	return merged
}

// Consolidate 执行记忆合并：衰减 → 去重+合并 → 过期淘汰
// 返回合并结果，调用方需根据结果同步 PG
func (m *LongTerm) Consolidate() ConsolidationResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := ConsolidationResult{}
	if m.consolidationCfg == nil || len(m.Items) <= 1 {
		return result
	}
	m.storeCount = 0
	removed := make(map[int]bool)

	// Phase 1: 重要性衰减 — 重要性随时间指数递减
	// 仅当变化幅度 > 0.01 时入 DecayUpdates，避免每次 Consolidate 都全表 UPDATE
	const minDecayDelta = 0.01
	for i := range m.Items {
		days := time.Since(m.Items[i].CreatedAt).Hours() / 24
		oldImp := m.Items[i].Importance
		newImp := oldImp * math.Pow(m.consolidationCfg.DecayRate, days)
		m.Items[i].Importance = newImp
		if oldImp-newImp >= minDecayDelta {
			result.DecayUpdates = append(result.DecayUpdates, DecayUpdate{
				ID:         m.Items[i].ID,
				Importance: newImp,
			})
		}
	}

	// Phase 2: 去重 + 合并 — 两两比较相似度
	for i := 0; i < len(m.Items); i++ {
		if removed[i] {
			continue
		}
		for j := i + 1; j < len(m.Items); j++ {
			if removed[j] {
				continue
			}
			sim := m.itemSimilarity(m.Items[i], m.Items[j])

			if sim >= m.consolidationCfg.DedupThreshold {
				// 去重：保留重要性更高的，删除另一个
				if m.Items[j].Importance >= m.Items[i].Importance {
					removed[i] = true
					result.Deduped++
					result.DeleteFromDB = append(result.DeleteFromDB, m.Items[i].ID)
				} else {
					removed[j] = true
					result.Deduped++
					result.DeleteFromDB = append(result.DeleteFromDB, m.Items[j].ID)
				}
			} else if sim >= m.consolidationCfg.SimilarityThreshold {
				// 合并：语义相近但非完全重复，合并为一条
				merged := m.mergeItems(m.Items[i], m.Items[j])
				m.Items[i] = merged
				removed[j] = true
				result.Merged++
				result.DeleteFromDB = append(result.DeleteFromDB, m.Items[j].ID)
				result.UpdateInDB = append(result.UpdateInDB, merged)
			}
		}
	}

	// Phase 3: 过期淘汰 — 低重要性 + 超过 TTL 的条目自动删除
	for i := range m.Items {
		if removed[i] {
			continue
		}
		days := time.Since(m.Items[i].CreatedAt).Hours() / 24
		if m.consolidationCfg.TTLDays > 0 &&
			days > float64(m.consolidationCfg.TTLDays) &&
			m.Items[i].Importance < m.consolidationCfg.MinImportance {
			removed[i] = true
			result.Expired++
			result.DeleteFromDB = append(result.DeleteFromDB, m.Items[i].ID)
		}
	}

	// Phase 4: 保护钩子 — GraphMemory 注入的图中心度保护在此生效。
	// 必须在物理删除之前回滚 removed 标记，否则会出现"内存已删但 PG 还在"的不一致窗口。
	if m.consolidationCfg.ProtectFn != nil && len(result.DeleteFromDB) > 0 {
		protected := m.consolidationCfg.ProtectFn(result.DeleteFromDB)
		if len(protected) > 0 {
			protSet := make(map[int]bool, len(protected))
			for _, id := range protected {
				protSet[id] = true
			}
			// 1) 从 removed 集合中撤回保护节点
			for i := range m.Items {
				if removed[i] && protSet[m.Items[i].ID] {
					removed[i] = false
				}
			}
			// 2) 同步剔除 DeleteFromDB 中的保护节点（PG 不删，与内存对齐）
			filtered := result.DeleteFromDB[:0]
			for _, id := range result.DeleteFromDB {
				if !protSet[id] {
					filtered = append(filtered, id)
				}
			}
			// 重算各计数：被保护的不计入 Deduped/Expired/Merged
			rescued := len(result.DeleteFromDB) - len(filtered)
			result.DeleteFromDB = filtered
			// 保护节点优先回填到去重计数，再回退过期与合并
			for _, n := range []*int{&result.Deduped, &result.Expired, &result.Merged} {
				if rescued <= 0 {
					break
				}
				take := rescued
				if take > *n {
					take = *n
				}
				*n -= take
				rescued -= take
			}
		}
	}

	// 重建列表和词表
	var newItems []Item
	for i, item := range m.Items {
		if !removed[i] {
			newItems = append(newItems, item)
		}
	}
	m.Items = newItems
	m.rebuildVocab()

	return result
}

// itemSimilarity 计算两条记忆之间的相似度
func (m *LongTerm) itemSimilarity(a, b Item) float64 {
	if len(a.Embedding) > 0 && len(b.Embedding) > 0 && len(a.Embedding) == len(b.Embedding) {
		return Cosine(a.Embedding, b.Embedding)
	}
	// TF 词袋降级
	m.buildVocab(a.Content)
	m.buildVocab(b.Content)
	av := m.textToVector(a.Content)
	bv := m.textToVector(b.Content)
	if len(av) < len(bv) {
		av = append(av, make([]float64, len(bv)-len(av))...)
	} else if len(bv) < len(av) {
		bv = append(bv, make([]float64, len(av)-len(bv))...)
	}
	return Cosine(av, bv)
}

// mergeItems 合并两条相似记忆，保留重要性更高的作为主体
func (m *LongTerm) mergeItems(a, b Item) Item {
	// 以重要性更高的条目为主体
	base, other := a, b
	if b.Importance > a.Importance {
		base, other = b, a
	}

	merged := Item{
		ID:           base.ID,
		Importance:   math.Max(base.Importance, other.Importance),
		Embedding:    base.Embedding,
		CreatedAt:    base.CreatedAt,
		LastAccessed: time.Now(),
	}

	// 内容合并：非子串关系时用分号拼接，否则保留较长的
	if !strings.Contains(base.Content, other.Content) && !strings.Contains(other.Content, base.Content) {
		merged.Content = base.Content + "；" + other.Content
	} else if len(other.Content) > len(base.Content) {
		merged.Content = other.Content
	} else {
		merged.Content = base.Content
	}

	// Embedding 按重要性加权平均
	if len(base.Embedding) > 0 && len(other.Embedding) > 0 && len(base.Embedding) == len(other.Embedding) {
		wA, wB := base.Importance, other.Importance
		total := wA + wB
		if total > 0 {
			merged.Embedding = make([]float64, len(base.Embedding))
			for i := range base.Embedding {
				merged.Embedding[i] = (base.Embedding[i]*wA + other.Embedding[i]*wB) / total
			}
		}
	}

	return merged
}

// rebuildVocab 重建全局词表（合并/删除后调用）
func (m *LongTerm) rebuildVocab() {
	m.vocabID = make(map[string]int)
	m.vocab = nil
	for _, item := range m.Items {
		m.buildVocab(item.Content)
	}
}

// ─────────────────────────────── 公共工具 ──────────────────────────────

// Tokenize 将文本切成词元（中文逐字，英文按单词）
// 暴露给外部子包（如 graph）使用
func Tokenize(text string) []string {
	var tokens []string
	word := ""
	for _, r := range text {
		if r >= 0x4E00 && r <= 0x9FFF {
			if word != "" {
				tokens = append(tokens, strings.ToLower(word))
				word = ""
			}
			tokens = append(tokens, string(r))
		} else if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			word += string(r)
		} else {
			if word != "" {
				tokens = append(tokens, strings.ToLower(word))
				word = ""
			}
		}
	}
	if word != "" {
		tokens = append(tokens, strings.ToLower(word))
	}
	return tokens
}

// Cosine 计算两个向量的余弦相似度
// 暴露给外部子包（如 graph）使用
func Cosine(a, b []float64) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
