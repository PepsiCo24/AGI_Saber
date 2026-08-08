// router.go — UnifiedAgent 的模式路由判断。
//
// 把 needTool / needRAG / needReAct / needReActFromTools 等基于关键词的
// 启发式判断从 agent.go 抽出，让 process()/processStream() 的主流程更聚焦。
package chat

import (
	"strings"

	"agi-assistant/internal/domain/tool"
)

// needTool 判断 query 是否触发单一工具（时间 / 天气 / 搜索 / 查询）
func (a *UnifiedAgent) needTool(query string) bool {
	q := strings.ToLower(query)
	return strings.Contains(q, "几点") || strings.Contains(q, "时间") ||
		strings.Contains(q, "天气") || strings.Contains(q, "查") ||
		strings.Contains(q, "搜索") || strings.Contains(q, "是什么")
}

// needRAG 知识库已加载且本次不走工具/ReAct 时启用 RAG
func (a *UnifiedAgent) needRAG(query string) bool {
	return a.rag.Loaded && !a.needTool(query) && !a.needReAct(query)
}

// needReAct 当 query 涉及 2+ 个子需求时触发多步推理
func (a *UnifiedAgent) needReAct(query string) bool {
	q := strings.ToLower(query)
	if (strings.Contains(q, "报告") || strings.Contains(q, "文档") || strings.Contains(q, "方案")) &&
		(strings.Contains(q, "生成") || strings.Contains(q, "写") || strings.Contains(q, "总结") || strings.Contains(q, "保存")) {
		return true
	}
	if strings.Contains(q, "调研") || strings.Contains(q, "研究") {
		return true
	}
	count := 0
	if strings.Contains(q, "时间") || strings.Contains(q, "几点") {
		count++
	}
	if strings.Contains(q, "天气") {
		count++
	}
	if strings.Contains(q, "总结") || strings.Contains(q, "汇总") {
		count++
	}
	if strings.Contains(q, "查") || strings.Contains(q, "搜索") {
		count++
	}
	return count >= 2
}

// needReActFromTools 在显式指定了工具集时直接走 ReAct 路径
func (a *UnifiedAgent) needReActFromTools(query string, ts map[string]tool.Tool) bool {
	return len(ts) > 0
}
