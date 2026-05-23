// Package memory 实现三层记忆系统：
//   - ShortTerm  短期记忆：近 N 轮对话滑动窗口
//   - LongTerm   长期记忆：支持语义向量（embedding）或 TF 词袋降级
//   - Preference 用户偏好：从对话中自动提取并持久化的键值对
//
// 长期记忆支持自动合并（Consolidation）：
//   - 去重（Dedup）：存入时自动检测高相似度条目，跳过重复
//   - 合并（Merge）：相似度超阈值的条目自动合并为一条
//   - 衰减（Decay）：重要性随时间指数衰减
//   - 过期（Expire）：低重要性 + 超过 TTL 的条目自动淘汰
package memory

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// ─────────────────────────────── 短期记忆 ────────────────────────────────

// ConversationMessage 是单条对话记录
type ConversationMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
}

// ShortTerm 维护最近 MaxTurns 轮的对话上下文
type ShortTerm struct {
	Messages []ConversationMessage `json:"messages"`
	MaxTurns int                   `json:"max_turns"`
}

// NewShortTerm 创建短期记忆，maxTurns 为保留的最大对话轮数
func NewShortTerm(maxTurns int) *ShortTerm {
	return &ShortTerm{MaxTurns: maxTurns}
}

// Add 追加一条消息，超出窗口时自动丢弃最早记录
func (m *ShortTerm) Add(role, content string) {
	m.Messages = append(m.Messages, ConversationMessage{
		Role:      role,
		Content:   content,
		Timestamp: time.Now().Format("15:04:05"),
	})
	max := m.MaxTurns * 2 // 每轮 = user + assistant 两条
	if len(m.Messages) > max {
		m.Messages = m.Messages[len(m.Messages)-max:]
	}
}

// ─────────────────────────────── 长期记忆 ────────────────────────────────

// Item 是长期记忆的存储单元
type Item struct {
	ID           int       `json:"id"`
	Content      string    `json:"content"`
	Importance   float64   `json:"importance"` // 0~1，越高越重要
	Embedding    []float64 `json:"embedding,omitempty"`
	Score        float64   `json:"score,omitempty"` // 召回时的综合得分（不持久化）
	CreatedAt    time.Time `json:"created_at"`
	LastAccessed time.Time `json:"last_accessed"`
}

// ConsolidationConfig 记忆合并配置
type ConsolidationConfig struct {
	SimilarityThreshold float64 // 合并相似度阈值 (0~1)，超过此值触发合并
	DedupThreshold      float64 // 去重相似度阈值 (0~1)，超过此值视为重复
	TTLDays             int     // 过期天数 (0=永不过期)
	DecayRate           float64 // 每日衰减系数 (0~1, 如 0.995 表示每天保留 99.5%)
	MinImportance       float64 // 低于此重要性且超 TTL 的条目会被淘汰
	TriggerInterval     int     // 每存入 N 条新记忆后触发合并
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
	Deduped      int    // 去重删除的条目数
	Merged       int    // 合并的条目数
	Expired      int    // 过期删除的条目数
	DeleteFromDB []int  // 需要从 PG 删除的 ID 列表
	UpdateInDB   []Item // 需要在 PG 更新的条目列表
}

// LongTerm 支持语义向量召回（embedding 优先）或 TF 词袋降级
type LongTerm struct {
	Items            []Item
	vocabID          map[string]int
	vocab            []string
	nextID           int
	storeCount       int
	consolidationCfg *ConsolidationConfig
}

// NewLongTerm 创建长期记忆
func NewLongTerm() *LongTerm {
	return &LongTerm{vocabID: make(map[string]int)}
}

// SetConsolidationConfig 设置合并配置
func (m *LongTerm) SetConsolidationConfig(cfg *ConsolidationConfig) {
	m.consolidationCfg = cfg
}

func (m *LongTerm) buildVocab(text string) {
	for _, t := range tokenize(text) {
		if _, ok := m.vocabID[t]; !ok {
			m.vocabID[t] = len(m.vocab)
			m.vocab = append(m.vocab, t)
		}
	}
}

func (m *LongTerm) textToVector(text string) []float64 {
	vec := make([]float64, len(m.vocabID))
	for _, t := range tokenize(text) {
		if idx, ok := m.vocabID[t]; ok {
			vec[idx]++
		}
	}
	return vec
}

// Store 将内容存入长期记忆（embedding 可选，传 nil 则使用 TF 降级）
// 返回 true 表示新增成功，false 表示因去重而跳过
func (m *LongTerm) Store(content string, importance float64, embedding []float64) bool {
	// 去重检测：与已有条目相似度过高时跳过，但更新已有条目的访问时间和重要性
	if m.consolidationCfg != nil && len(m.Items) > 0 && len(embedding) > 0 {
		for i := range m.Items {
			if len(m.Items[i].Embedding) == len(embedding) {
				sim := cosine(embedding, m.Items[i].Embedding)
				if sim >= m.consolidationCfg.DedupThreshold {
					if importance > m.Items[i].Importance {
						m.Items[i].Importance = importance
					}
					m.Items[i].LastAccessed = time.Now()
					return false
				}
			}
		}
	}

	for _, item := range m.Items {
		m.buildVocab(item.Content)
	}
	m.buildVocab(content)
	now := time.Now()
	m.Items = append(m.Items, Item{
		ID:           m.nextID,
		Content:      content,
		Importance:   importance,
		Embedding:    embedding,
		CreatedAt:    now,
		LastAccessed: now,
	})
	m.nextID++
	m.storeCount++
	return true
}

// StoreItem 直接插入已有 Item（用于从 DB 恢复数据）
func (m *LongTerm) StoreItem(item Item) {
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

// SyncLastItemPGID 将最后一条记忆的 ID 同步为 PG 自增 ID
// 用于解决内存 ID 与 PG ID 不一致的问题
func (m *LongTerm) SyncLastItemPGID(pgID int) {
	if len(m.Items) > 0 && pgID > 0 {
		m.Items[len(m.Items)-1].ID = pgID
		if pgID >= m.nextID {
			m.nextID = pgID + 1
		}
	}
}

// NeedConsolidation 检查是否需要触发记忆合并
func (m *LongTerm) NeedConsolidation() bool {
	return m.consolidationCfg != nil &&
		m.consolidationCfg.TriggerInterval > 0 &&
		m.storeCount >= m.consolidationCfg.TriggerInterval
}

// Recall 从长期记忆中召回与 query 最相关的 topK 条
// 优先使用 embedding 余弦相似度，若无 embedding 则退回 TF
// 只返回综合得分超过 threshold 的条目，避免注入噪声
func (m *LongTerm) Recall(query string, topK int, queryEmbedding []float64) []Item {
	if len(m.Items) == 0 {
		return nil
	}
	const threshold = 0.4 // 综合得分阈值：sim*0.7 + importance*0.3
	type scored struct {
		item Item
		s    float64
	}
	var items []scored
	for i := range m.Items {
		var sim float64
		if len(queryEmbedding) > 0 && len(m.Items[i].Embedding) == len(queryEmbedding) {
			sim = cosine(queryEmbedding, m.Items[i].Embedding)
		} else {
			// TF 降级
			m.buildVocab(query)
			qv := m.textToVector(query)
			iv := m.textToVector(m.Items[i].Content)
			if len(qv) < len(iv) {
				qv = append(qv, make([]float64, len(iv)-len(qv))...)
			} else if len(iv) < len(qv) {
				iv = append(iv, make([]float64, len(qv)-len(iv))...)
			}
			sim = cosine(qv, iv)
		}
		s := sim*0.7 + m.Items[i].Importance*0.3
		if s >= threshold {
			m.Items[i].LastAccessed = time.Now()
			items = append(items, scored{item: m.Items[i], s: s})
		}
	}
	if len(items) == 0 {
		return nil
	}
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].s > items[i].s {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	if topK > len(items) {
		topK = len(items)
	}
	result := make([]Item, topK)
	for i := range result {
		result[i] = items[i].item
		result[i].Score = items[i].s
	}
	return result
}

// Consolidate 执行记忆合并：衰减 → 去重+合并 → 过期淘汰
// 返回合并结果，调用方需根据结果同步 PG
func (m *LongTerm) Consolidate() ConsolidationResult {
	result := ConsolidationResult{}
	if m.consolidationCfg == nil || len(m.Items) <= 1 {
		return result
	}
	m.storeCount = 0
	removed := make(map[int]bool)

	// Phase 1: 重要性衰减 — 重要性随时间指数递减
	for i := range m.Items {
		days := time.Since(m.Items[i].CreatedAt).Hours() / 24
		m.Items[i].Importance *= math.Pow(m.consolidationCfg.DecayRate, days)
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
		return cosine(a.Embedding, b.Embedding)
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
	return cosine(av, bv)
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

// ─────────────────────────────── 用户偏好 ────────────────────────────────

// Preference 以键值对形式存储用户偏好信息
type Preference struct {
	Data map[string]string `json:"data"`
}

// NewPreference 创建用户偏好存储
func NewPreference() *Preference {
	return &Preference{Data: make(map[string]string)}
}

// Save 保存单条偏好
func (p *Preference) Save(key, value string) {
	if key != "" && value != "" {
		p.Data[key] = value
	}
}

// SaveBatch 批量保存偏好（从 LLM 提取结果）
func (p *Preference) SaveBatch(kvs map[string]string) {
	for k, v := range kvs {
		if k != "" && v != "" {
			p.Data[k] = v
		}
	}
}

// ExtractAndSave 从对话文本中用规则提取偏好（兜底，LLM 提取优先）
func (p *Preference) ExtractAndSave(msg string) (key, value string, ok bool) {
	if strings.Contains(msg, "我喜欢") {
		parts := strings.SplitN(msg, "喜欢", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
			key, value = "喜好", strings.TrimSpace(parts[1])
			p.Data[key] = value
			return key, value, true
		}
	}
	if strings.Contains(msg, "我爱") {
		parts := strings.SplitN(msg, "爱", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
			key, value = "喜好", strings.TrimSpace(parts[1])
			p.Data[key] = value
			return key, value, true
		}
	}
	if strings.Contains(msg, "我叫") {
		parts := strings.SplitN(msg, "叫", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
			key, value = "姓名", strings.TrimSpace(parts[1])
			p.Data[key] = value
			return key, value, true
		}
	}
	return "", "", false
}

// BuildContext 将偏好数据格式化为给 LLM 的上下文字符串
func (p *Preference) BuildContext() string {
	if len(p.Data) == 0 {
		return ""
	}
	var items []string
	for k, v := range p.Data {
		items = append(items, fmt.Sprintf("%s: %s", k, v))
	}
	return "【用户偏好】\n" + strings.Join(items, "\n")
}

// ─────────────────────────────── 内部工具函数 ────────────────────────────

// tokenize 将文本切成词元（中文逐字，英文按单词）
func tokenize(text string) []string {
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

// cosine 计算两个向量的余弦相似度
func cosine(a, b []float64) float64 {
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
