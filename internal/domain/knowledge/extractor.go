package knowledge

import (
	"encoding/json"
	"strings"

	"agi-assistant/internal/pkg/logger"
)

// Extractor 通过注入的 LLM 回调从文本中抽取实体和关系
type Extractor struct {
	llmFn func(systemPrompt, userMsg string) string
}

// NewExtractor 创建 Extractor，llmFn 为 LLM 调用回调（与 agent 解耦）
func NewExtractor(llmFn func(systemPrompt, userMsg string) string) *Extractor {
	return &Extractor{llmFn: llmFn}
}

const extractSystemPrompt = `你是一个信息抽取专家。从给定文本中抽取命名实体和实体间关系。

实体类型（type 字段只能用以下值）：
- Person（人物）
- Organization（组织/公司/机构）
- Location（地点/地区）
- Concept（概念/技术/思想）
- Event（事件）
- Product（产品/工具）
- Unknown（其他）

关系类型（rel_type 字段只能用以下值）：
- RELATES_TO（相关）
- PART_OF（属于/是...的一部分）
- CAUSES（导致/引发）
- DESCRIBES（描述/介绍）
- MENTIONS（提及）
- WORKS_FOR（工作于）
- LOCATED_IN（位于）

输出格式（只输出 JSON，不加任何说明）：
{
  "entities": [{"name":"实体名","type":"类型"}],
  "relations": [{"from":"实体A","to":"实体B","rel_type":"关系类型"}]
}

如果文本中没有可抽取的实体，输出 {"entities":[],"relations":[]}`

// Extract 从单段文本中抽取实体和关系
// 若 LLM 不可用或解析失败，返回空结果（不影响主流程）
func (e *Extractor) Extract(text string) ExtractResult {
	if e.llmFn == nil || strings.TrimSpace(text) == "" {
		return ExtractResult{}
	}

	raw := e.llmFn(extractSystemPrompt, "文本：\n"+text)
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var result ExtractResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		logger.L().Warn("entity/relation extract parse failed", "err", err, "raw_preview", firstN(raw, 100))
		return ExtractResult{}
	}

	// 清洗：去除空名称，规范 type
	cleaned := ExtractResult{}
	seen := make(map[string]bool)
	for _, ent := range result.Entities {
		ent.Name = strings.TrimSpace(ent.Name)
		if ent.Name == "" || seen[ent.Name] {
			continue
		}
		if !isValidEntityType(ent.Type) {
			ent.Type = EntityUnknown
		}
		cleaned.Entities = append(cleaned.Entities, ent)
		seen[ent.Name] = true
	}
	for _, rel := range result.Relations {
		rel.FromName = strings.TrimSpace(rel.FromName)
		rel.ToName = strings.TrimSpace(rel.ToName)
		if rel.FromName == "" || rel.ToName == "" || rel.RelType == "" {
			continue
		}
		if !isValidRelType(rel.RelType) {
			rel.RelType = "RELATES_TO"
		}
		cleaned.Relations = append(cleaned.Relations, rel)
	}
	return cleaned
}

func isValidEntityType(t EntityType) bool {
	switch t {
	case EntityPerson, EntityOrg, EntityLocation, EntityConcept,
		EntityEvent, EntityProduct, EntityUnknown:
		return true
	}
	return false
}

func isValidRelType(r string) bool {
	switch r {
	case "RELATES_TO", "PART_OF", "CAUSES", "DESCRIBES",
		"MENTIONS", "WORKS_FOR", "LOCATED_IN":
		return true
	}
	return false
}

// firstN 返回 s 的前 n 个 rune，超出加省略号；日志预览用。
func firstN(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
