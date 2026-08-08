// accessor.go — UnifiedAgent 的字段访问器、工具注册、快照保存、参数补全。
//
// 这些方法本身没有业务编排逻辑，是 agent struct 对外暴露状态 / 内部辅助操作的薄包装。
package chat

import (
	"agi-assistant/internal/domain/memory/longterm"
	"agi-assistant/internal/domain/memory/preference"
	"agi-assistant/internal/domain/memory/shortterm"
	"agi-assistant/internal/domain/rag"
	"agi-assistant/internal/domain/tool"
	"agi-assistant/internal/pkg/logger"
	"agi-assistant/internal/usercontext"
	"context"
	"encoding/json"
	"fmt"
	"time"
)

func (a *UnifiedAgent) RegisterTool(t tool.Tool) {
	a.tools.register(t)
}

// enabledSkillTools 返回当前请求用户「已安装且开启」的 skill 工具集。
// skillProvider 未注入或未登录时返回 nil——由调用方合并时做空守卫。
func (a *UnifiedAgent) enabledSkillTools(ctx context.Context) map[string]tool.Tool {
	if a.skillProvider == nil {
		return nil
	}
	userID := usercontext.UserIDFromContext(ctx)
	if userID == "" {
		return nil
	}
	return a.skillProvider(userID)
}

// RAG 暴露 RAG 引擎，供 HTTP handler 直接调用 Ingest
func (a *UnifiedAgent) RAG() *rag.Engine { return a.rag }

// Tools 暴露工具集（持锁拷贝），供 HTTP handler 列出工具信息。
// 调用方拿到的是快照，可无锁安全使用，且修改不影响 agent 内部 map。
func (a *UnifiedAgent) Tools() map[string]tool.Tool {
	return a.tools.snapshot()
}

// toolsSnapshot 内部辅助：路由层 / Decide / ReAct 用一致工具集快照。
// 所有读取走 a.tools.snapshot()，避免持锁遍历过程中被并发 register 干扰。
func (a *UnifiedAgent) toolsSnapshot() map[string]tool.Tool {
	return a.tools.snapshot()
}

// ShortTerm 暴露指定用户的短期记忆，供 HTTP handler 查询。
// 多租户：未登录返回 nil；新用户首次访问时懒创建空桶。
func (a *UnifiedAgent) ShortTerm(userID string) *shortterm.ShortTerm { return a.mem.STM(userID) }

// LongTerm 暴露长期记忆，供 HTTP handler 查询
func (a *UnifiedAgent) LongTerm() *longterm.LongTerm { return a.mem.ltm }

// Preferences 暴露指定用户的偏好桶，供 HTTP handler 查询。
func (a *UnifiedAgent) Preferences(userID string) *preference.Preference { return a.mem.Pref(userID) }

// Snapshots 返回历史快照列表（持锁拷贝）
func (a *UnifiedAgent) Snapshots() []Snapshot {
	return a.runtime.snapshotList()
}

// QuarantinedMemories 返回所有被隔离的 LTM 条目（审计端点用）。
// 隔离不召回 + 不删除——既阻止 prompt 污染，又保留证据用于事后取证。
func (a *UnifiedAgent) QuarantinedMemories() []longterm.Item {
	return a.mem.ltm.QuarantinedItems()
}

// QuarantineMemory 把指定 ID 的 LTM 条目标记为隔离，并同步写 PG。
// 内存层标记成功后再落盘，避免数据库已写但内存未生效的窗口。
func (a *UnifiedAgent) QuarantineMemory(id int, reason string) bool {
	if !a.mem.ltm.Quarantine(id, reason) {
		return false
	}
	a.repos.ltm.SetQuarantine(id, true, reason)
	return true
}

// UnquarantineMemory 解除隔离并同步写 PG。
func (a *UnifiedAgent) UnquarantineMemory(id int) bool {
	if !a.mem.ltm.Unquarantine(id) {
		return false
	}
	a.repos.ltm.SetQuarantine(id, false, "")
	return true
}

// SupersededMemories 列出所有 Superseded=true 的条目（审计用）。
// 仅返回内存中存在的——重启后已通过 restoreFromDB 灌回。
func (a *UnifiedAgent) SupersededMemories() []longterm.Item {
	return a.mem.ltm.SupersededItems()
}
func (a *UnifiedAgent) saveSnapshot(task *TaskState) {
	if task == nil {
		return
	}
	var stateCopy TaskState
	data, _ := json.Marshal(task)
	if err := json.Unmarshal(data, &stateCopy); err != nil {
		// 不应该发生（自序列化），但避免吃掉错误
		logger.L().Warn("saveSnapshot deserialize failed", "err", err)
		return
	}
	snap := Snapshot{State: stateCopy, Timestamp: time.Now().Format("15:04:05")}
	a.runtime.appendSnapshot(snap)
	a.repos.snap.Save(task.TaskID, data)
}

// ─────────────────────────────── Stage 5：Memory（基础层，注入所有模式）────────
//
// 旧的 buildMemorySystemPrefix / buildMemorySystemPrefixWithCtx 已删除，
// 由 buildContextPrefix → promptctx.ContextAssembler 取代（Schema-driven 装配）。

// fillParamsFromPreference 用用户偏好自动补全工具调用参数中缺失的值。
// userID 为空时不补全（未登录请求拿不到任何用户偏好桶）。
func (a *UnifiedAgent) fillParamsFromPreference(userID string, tc *tool.CallResult) {
	if tc == nil || userID == "" {
		return
	}
	prefs := a.mem.prefSnapshot(userID) // 一次性快照，下方可无锁访问
	if len(prefs) == 0 {
		return
	}
	// 偏好 key → 工具参数名的映射
	prefToParam := map[string][]string{
		"城市": {"city", "location", "location_name"},
		"时区": {"timezone", "tz", "time_zone"},
		"姓名": {"name", "username", "user_name"},
		"语言": {"language", "lang"},
		"国家": {"country", "nation"},
	}
	for prefKey, paramNames := range prefToParam {
		prefVal, ok := prefs[prefKey]
		if !ok || prefVal == "" {
			continue
		}
		for _, paramName := range paramNames {
			if v, exists := tc.Params[paramName]; !exists || v == nil || fmt.Sprint(v) == "" {
				tc.Params[paramName] = prefVal
			}
		}
	}
}
