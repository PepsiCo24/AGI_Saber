package promptctx

import (
	"context"
	"fmt"
	"sort"

	"agi-assistant/internal/domain/memory/longterm"
	"agi-assistant/internal/domain/memory/preference"
)

// PreferenceProvider 抽象"按 userID 取偏好桶"的能力——
// 让 ProfileSource 不再持有进程级 *preference.Preference 单例，
// 改成每次 Fetch 时按 q.UserID 拿桶（多租户隔离）。
//
// 实现侧（application/chat/memoryStack）保证：
//   - userID 空 → 返 nil
//   - userID 首次访问 → 懒创建空桶 → 返回有效指针
type PreferenceProvider func(userID string) *preference.Preference

// ProfileSource 装填 Long-term Profile 槽位
// 数据来源：
//   - prefProvider(userID)：稳定身份/偏好键值对（高优先级）
//   - LTM 中 user_id=q.UserID 且 category=identity|preference 的条目
type ProfileSource struct {
	prefProvider PreferenceProvider
	ltm          *longterm.LongTerm
}

// NewProfileSource 创建 Profile source。
// prefProvider 实现按 userID 隔离取偏好桶；不能为 nil（启动期 misconfig 应早死）。
func NewProfileSource(prefProvider PreferenceProvider, ltm *longterm.LongTerm) *ProfileSource {
	return &ProfileSource{prefProvider: prefProvider, ltm: ltm}
}

// ID 返回 source 标识
func (s *ProfileSource) ID() string { return "profile" }

// Supports 仅声明支持 Profile 槽位
func (s *ProfileSource) Supports(kind SlotKind) bool { return kind == SlotProfile }

// Fetch 输出按字母序稳定的偏好键值对 + LTM 身份/偏好类条目
func (s *ProfileSource) Fetch(ctx context.Context, slot Slot, q Query) ([]ContextItem, error) {
	// 多租户：未登录请求不输出任何 profile 数据（避免污染 prompt）
	if q.UserID == "" {
		return nil, nil
	}

	var items []ContextItem

	// 偏好部分：按用户取桶
	if s.prefProvider != nil {
		if pref := s.prefProvider(q.UserID); pref != nil {
			// 拿一次性快照，避免遍历期间被并发写入打断
			data := pref.Snapshot()
			if len(data) > 0 {
				keys := make([]string, 0, len(data))
				for k := range data {
					keys = append(keys, k)
				}
				sort.Strings(keys) // 稳定顺序，避免每轮 prompt 抖动
				for _, k := range keys {
					items = append(items, ContextItem{
						Text:   fmt.Sprintf("%s: %s", k, data[k]),
						Score:  1.0, // 偏好是确定性事实
						Source: s.ID(),
					})
				}
			}
		}
	}

	// LTM 身份/偏好类条目：q.UserID 透传给 FilterByCategory，强制隔离
	if s.ltm != nil && len(slot.Filter.Categories) > 0 {
		limit := slot.Filter.TopK
		if limit <= 0 {
			limit = 10
		}
		for _, item := range s.ltm.FilterByCategory(q.UserID, slot.Filter.Categories, limit) {
			items = append(items, ContextItem{
				Text:   item.Content,
				Score:  item.Importance,
				Source: s.ID(),
				Meta:   map[string]string{"category": item.Category},
			})
		}
	}

	return items, nil
}
