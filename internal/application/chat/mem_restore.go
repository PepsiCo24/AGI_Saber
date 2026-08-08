// restore.go — 启动期从 PostgreSQL 恢复长期记忆 + RAG chunks，
// 以及把 KGStore 与 GraphMemory 串起来。
//
// V2 多租户重构：preference / chat_history 不再启动期 restore——
//   - preference 跨用户全量加载没意义（每用户只需要自己的）
//   - chat_history 改成请求级 lazy load（按 userID + limit 即查即用）
//
// 仅 LTM 仍走启动期全量加载——它是单进程内全用户共享缓存，
// 召回时由 RecallByFilter 按 userID 过滤实现隔离。
package chat

import (
	"context"

	"agi-assistant/internal/domain/knowledge"
	graphmem "agi-assistant/internal/domain/memory/graph"
	"agi-assistant/internal/domain/memory/longterm"
	"agi-assistant/internal/domain/rag"
	"agi-assistant/internal/infrastructure/llm"
	"agi-assistant/internal/pkg/logger"
)

// restoreFromDB 启动时从 PostgreSQL 恢复长期记忆。
// preference / chat_history 改为请求级懒加载，本函数不再处理。
func (a *UnifiedAgent) restoreFromDB() {
	if a.repos.memoryTx != nil {
		records, err := a.repos.memoryTx.LoadActive(context.Background())
		if err != nil {
			logger.L().Warn("authoritative long-term memory restore failed", "err", err)
			return
		}
		a.mem.ltm.ReplaceCommitted(records)
		if len(records) > 0 {
			logger.L().Info("long-term memory restored from source of truth",
				"count", len(records))
		}
		return
	}

	// Compatibility fallback for tests and transitional callers that have not
	// yet injected memorytx. Production wiring always supplies memorytx.
	// 恢复长期记忆（含所有用户的条目；召回时由 UserID 过滤实现隔离）
	rows := a.repos.ltm.Load()
	for _, row := range rows {
		a.mem.ltm.StoreItem(longterm.Item{
			ID:               row.ID,
			UserID:           row.UserID,
			Content:          row.Content,
			Importance:       row.Importance,
			Embedding:        row.Embedding,
			CreatedAt:        row.CreatedAt,
			LastAccessed:     row.LastAccessed,
			Category:         row.Category,
			Tags:             row.Tags,
			SlotHint:         row.SlotHint,
			Quarantined:      row.Quarantined,
			QuarantineReason: row.QuarantineReason,
			Superseded:       row.Superseded,
			SupersededAt:     row.SupersededAt,
			Supersedes:       row.Supersedes,
		})
	}

	if len(rows) > 0 {
		logger.L().Info("long-term memory restored (multi-user, per-user filter on recall)", "count", len(rows))
	}
}

// restoreRAGFromDB 从 PostgreSQL 加载持久化的 RAG chunks 到 TF 兜底索引
func (a *UnifiedAgent) restoreRAGFromDB() {
	chunkRows, err := a.repos.ragChunk.LoadAll()
	if err != nil || len(chunkRows) == 0 {
		return
	}
	var chunks []rag.Chunk
	for i, row := range chunkRows {
		chunks = append(chunks, rag.Chunk{ID: i, Content: row.Content})
	}
	a.rag.RestoreChunks(chunks)
	logger.L().Info("rag chunks restored", "count", len(chunks))
}

// initKnowledgeGraph 初始化 Neo4j 知识图谱存储，并注入到 RAG 引擎 + GraphMemory
func (a *UnifiedAgent) initKnowledgeGraph() {
	kg := knowledge.NewKGStore(a.cfg, func(systemPrompt, userMsg string) string {
		return a.llm.Chat(systemPrompt, []llm.Message{{Role: "user", Content: userMsg}})
	})
	a.kg = kg
	a.rag.SetKGStore(kg)

	// 构建图记忆层（包装现有 ltm）；复用 kg 的 Neo4j 客户端避免双连接
	gm := graphmem.New(a.mem.ltm, kg, kg.Client(), a.cfg.MemoryConsolidationSimilarity)
	gm.SyncPrevID() // 从 DB 恢复后对齐 prevID
	a.mem.attachGraph(gm)

	if kg.Available() {
		logger.L().Info("knowledge graph ready (neo4j); rag upgraded to 3-way hybrid; memory graph layer attached")
	} else {
		logger.L().Info("neo4j unavailable; rag stays 2-way; memory falls back to vector-only")
	}
}

// KG 暴露知识图谱实例，供 HTTP handler 或记忆模块使用
func (a *UnifiedAgent) KG() *knowledge.KGStore { return a.kg }
