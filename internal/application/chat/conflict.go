// conflict.go — 矛盾治理 V1：写入新记忆前后判定与已有记忆是否冲突，
// 命中后把旧条目标 Superseded（不删除，便于审计回滚）。
//
// 设计要点：
//   - 仅对 identity / preference / fact 三类启用——episodic（一次性事件）天然共存；
//     policy 类涉及用户给 agent 的硬约束，需要走专门的二次确认路径（暂未做）。
//   - 候选筛选在 domain 层（ConflictCandidates，纯计算），LLM-judge 在 application 层
//     （Chat 调用，不能在 LTM 锁内）。
//   - LLM 不可用 / 候选为空时退化为 no-op，不影响现有 Store 行为。
//   - 判定保守：默认认为不矛盾，只有 LLM 明确说"对立"才标记 superseded。
//     宁可漏判也不误伤——误伤会导致正确的事实被压制。
package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agi-assistant/internal/domain/memory/longterm"
	"agi-assistant/internal/infrastructure/llm"
	"agi-assistant/internal/pkg/logger"
)

// 冲突检测仅对这些 category 启用。其它类型默认共存。
var conflictDetectableCategories = map[string]bool{
	"identity":   true,
	"preference": true,
	"fact":       true,
}

// 候选筛选区间：cosine 在 [minSim, maxSim) 之间——与 dedup 阈值（≥maxSim）拼接，
// 形成"重复 / 冲突 / 无关"的三段切分。
const (
	conflictMinSim = 0.75
	conflictMaxSim = 0.95
)

// detectAndResolveConflict 在新记忆 (content, emb, category) 入库前后调用：
//
//	写入前：拿到候选；调 LLM-judge；保留判定结果
//	写入后：用 newID 调 MarkSuperseded（domain 层 + PG repo 双写）
//
// 为简化首版，把"判定"和"标记"拼成一次调用，但在 newID==0 时只判定不标记
// （供调用方在 Store 失败 / dedup 命中时跳过），newID>0 时执行标记。
//
// userID 是多租户隔离主键——只在该用户自己的同主题记忆里找冲突，
// 不会跨用户取代。空字符串直接 skip。
//
// 返回真正被标记的旧条目 ID 列表，便于日志记录。
func (a *UnifiedAgent) detectAndResolveConflict(
	ctx context.Context,
	userID string,
	newContent string,
	newEmb []float64,
	category string,
	newID int,
) []int {
	if userID == "" || !conflictDetectableCategories[category] {
		return nil
	}
	if len(newEmb) == 0 || !a.cfg.IsRealLLM() {
		return nil
	}

	candidates := a.mem.ltm.ConflictCandidates(userID, newEmb, category, conflictMinSim, conflictMaxSim)
	if len(candidates) == 0 {
		return nil
	}

	// 控制 LLM 成本：最多送 3 条候选给 judge——通常同主题的活跃记忆不会超过 3 条
	if len(candidates) > 3 {
		candidates = candidates[:3]
	}

	verdicts := a.llmJudgeConflict(ctx, newContent, candidates)
	var oldIDs []int
	for _, v := range verdicts {
		if v.IsContradicting {
			oldIDs = append(oldIDs, v.OldID)
		}
	}
	if len(oldIDs) == 0 {
		return nil
	}

	// newID == 0 时（极少见，调用方应保证已 Store）只在 domain 层标记，
	// PG 层等下次写入再补——避免数据不一致就直接放弃
	if newID <= 0 {
		logger.C(ctx).Warn("conflict: newID missing, marking domain layer only", "old_ids", oldIDs)
		marked := a.mem.ltm.MarkSuperseded(oldIDs, 0)
		a.repos.ltm.MarkSuperseded(marked, 0)
		return marked
	}

	marked := a.mem.ltm.MarkSuperseded(oldIDs, newID)
	a.repos.ltm.MarkSuperseded(marked, newID)
	logger.C(ctx).Info("conflict: new memory supersedes old",
		"new_id", newID, "old_ids", marked, "category", category)
	return marked
}

// conflictVerdict 是 LLM 对单个 (新, 旧) 对的判定结果
type conflictVerdict struct {
	OldID           int    `json:"old_id"`
	IsContradicting bool   `json:"is_contradicting"`
	Reason          string `json:"reason"`
}

// llmJudgeConflict 给 LLM 一次性看新记忆 + 多个候选，让它逐条返回判定。
// 批量调用比逐对调用便宜，且让 LLM 看到全局上下文（候选间的关系也能识别）。
//
// 解析失败 / LLM 不可用时返回空切片——保守策略：宁可漏判，不误伤。
func (a *UnifiedAgent) llmJudgeConflict(
	ctx context.Context,
	newContent string,
	candidates []longterm.ConflictCandidate,
) []conflictVerdict {
	if len(candidates) == 0 {
		return nil
	}

	var lines []string
	for _, c := range candidates {
		lines = append(lines, fmt.Sprintf(`{"id":%d,"content":%q}`, c.Item.ID, c.Item.Content))
	}
	candidateBlock := "[\n  " + strings.Join(lines, ",\n  ") + "\n]"

	prompt := `判断"新记忆"是否与每条"旧记忆"语义对立（contradicting）。

判定标准：
- "我喜欢猫" 与 "我不喜欢猫" → 对立
- "用户在北京" 与 "用户搬到了上海" → 对立（新事实取代旧事实）
- "用户喜欢咖啡" 与 "用户喜欢茶" → 不对立（可以同时喜欢两者）
- "用户工作时不喝酒" 与 "用户周末喝酒" → 不对立（场景不同）
- 不确定 / 信息不足 → 不对立（保守）

新记忆：` + newContent + `

旧记忆候选列表：
` + candidateBlock + `

请逐条输出判定，严格 JSON 数组格式：
[{"old_id":<id>,"is_contradicting":true|false,"reason":"<简短理由>"}]
只输出 JSON，不要任何其他内容。`

	raw := a.llm.ChatContext(ctx, "", []llm.Message{{Role: "user", Content: prompt}})
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var out []conflictVerdict
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		logger.C(ctx).Warn("conflict: LLM verdict parse failed", "err", err, "raw", raw)
		return nil
	}
	return out
}
