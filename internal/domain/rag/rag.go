// Package rag 实现检索增强生成（Retrieval-Augmented Generation）。
//
// 完整流程：
//
//	Ingest:
//	  ParentSplitter ──► 大块 (parent)
//	         │
//	         └─► RecursiveSplitter ──► 小块 (child)，写入索引时携带父块原文
//
//	Query:
//	  Rewriter (history-aware + multi-query)
//	         │
//	         ▼
//	  HybridStore.SearchMulti（每条 query 独立三路检索 → 跨查询 RRF 合并）
//	         │
//	         ▼
//	  Reranker (LLM listwise 精排，可关闭)
//	         │
//	         ▼
//	  父块回填 (small-to-big) ──► LLM 合成
package rag

import (
	"agi-assistant/config"
	"agi-assistant/internal/domain/knowledge"
	"agi-assistant/internal/infrastructure/eventbus"
	"agi-assistant/internal/infrastructure/persistence/ragchunk"
	"agi-assistant/internal/pkg/logger"
	"fmt"
	"runtime/debug"
	"strings"
)

// goSafe 启动一个带 panic recover 的后台 goroutine。
// KG IndexDocument 涉及 Neo4j 写入，断连时驱动可能 panic — 包一层避免拖崩进程。
func goSafe(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.L().Error("goroutine panic",
					"task", name, "panic", r, "stack", string(debug.Stack()))
			}
		}()
		fn()
	}()
}

// ─────────────────────────────── 切分单元 ────────────────────────────────

// Chunk 是文本切片单元
type Chunk struct {
	ID      int    `json:"id"`
	Content string `json:"content"`
}

// ParentChunk 是父块（small-to-big 检索时返回给 LLM 的较大单元）
type ParentChunk struct {
	ID       int    // 父块 idx（0-based）
	Content  string // 父块原文
	ChildIDs []int  // 该父块包含的子块 idx 列表（仅用于调试 / 状态展示）
}

// ─────────────────────────────── 检索结果 ────────────────────────────────

// SearchResult 单条检索结果
type SearchResult struct {
	Chunk      Chunk   `json:"chunk"`
	Similarity float64 `json:"similarity"`
}

// IngestResult summarizes document ingestion and indexing.
type IngestResult struct {
	ChunkCount   int     `json:"chunk_count"`
	ParentCount  int     `json:"parent_count"`
	IndexedCount int     `json:"indexed_count"`
	DocHash      string  `json:"doc_hash"`
	ChunkPreview []Chunk `json:"chunk_preview,omitempty"`
}

// ─────────────────────────────── RAG 引擎 ────────────────────────────────

// Engine 整合文本分割、混合检索与答案生成
type Engine struct {
	cfg    *config.APIConfig
	store  *HybridStore
	kg     *knowledge.KGStore // 知识图谱，nil 时禁用
	Loaded bool
	chunks ragchunk.Repo
	events eventbus.Publisher

	// 切分：大块 + 小块
	parentSplitter Splitter // 父块切分器
	childSplitter  Splitter // 子块切分器（精准命中）

	// 检索前后的可选层
	rewriter Rewriter // nil 时跳过查询改写
	reranker Reranker // nil 时跳过精排

	generateFn func(systemPrompt string, userMsg string) string // LLM 回调，由 agent 注入
}

// NewEngine 创建 RAG 引擎
//
// 切分参数读取：
//   - cfg.ChunkSize / cfg.ChunkOverlap   →   子块（用于检索）
//   - 父块大小默认为 ChunkSize * 4，重叠为 ChunkOverlap * 2
//
// rewriter / reranker 默认为 nil（关闭），由调用方按需注入。
func NewEngine(cfg *config.APIConfig, chunks ragchunk.Repo, events eventbus.Publisher) *Engine {
	parentSize := cfg.ChunkSize * 4
	if parentSize < 600 {
		parentSize = 600
	}
	parentOverlap := cfg.ChunkOverlap * 2
	return &Engine{
		cfg:            cfg,
		store:          NewHybridStore(cfg, chunks),
		parentSplitter: NewRecursiveSplitter(parentSize, parentOverlap, nil),
		childSplitter:  NewRecursiveSplitter(cfg.ChunkSize, cfg.ChunkOverlap, nil),
		chunks:         chunks,
		events:         events,
	}
}

// SetKGStore 注入知识图谱存储（由 agent.New 在基础设施就绪后调用）
func (e *Engine) SetKGStore(kg *knowledge.KGStore) {
	e.kg = kg
	e.store.SetKGStore(kg)
}

// SetGenerateFn 注入 LLM 调用回调，供 Query 合成答案
func (e *Engine) SetGenerateFn(fn func(systemPrompt string, userMsg string) string) {
	e.generateFn = fn
}

// SetEmbedFn 注入 Embedding 回调，供 HybridStore 语义向量化
func (e *Engine) SetEmbedFn(fn func(text string) ([]float64, error)) {
	e.store.SetEmbedFn(fn)
}

// SetRewriter 注入查询改写器；nil 等价于关闭
func (e *Engine) SetRewriter(r Rewriter) { e.rewriter = r }

// SetReranker 注入重排器；nil 等价于关闭
func (e *Engine) SetReranker(r Reranker) {
	e.reranker = r
	e.store.SetReranker(r)
}

// Mode 返回当前检索模式
func (e *Engine) Mode() string {
	return e.store.Mode()
}

// Ingest 将文档切分并建立混合检索索引，返回 (子块数量, docHash)
// 知识图谱索引异步执行，不阻塞返回
//
// 切分策略：parent → child 两级
//   - 父块用语义块大小（默认 800 字符），用于回填给 LLM 时上下文完整
//   - 子块用 cfg.ChunkSize（默认 200），写入索引时关联父块，检索更精准
func (e *Engine) Ingest(doc string) IngestResult {
	return e.IngestWithMetadata(doc, IngestMetadata{})
}

func (e *Engine) IngestWithMetadata(doc string, metadata IngestMetadata) IngestResult {
	parents := e.parentSplitter.Split(doc)
	var allChildren []Chunk
	// childToParent[childIdx] = parentContent
	var childToParent []string
	for _, p := range parents {
		children := e.childSplitter.Split(p.Content)
		for _, c := range children {
			c.ID = len(allChildren) // 全局重新编号
			allChildren = append(allChildren, c)
			childToParent = append(childToParent, p.Content)
		}
	}

	docHash, indexed := e.store.IndexWithParentsAndMetadata(allChildren, childToParent, doc, metadata)
	e.Loaded = len(indexed) > 0
	if e.events != nil {
		e.events.Publish("rag.ingest",
			fmt.Sprintf(`{"chunk_count":%d,"indexed_count":%d,"parent_count":%d,"mode":"%s","doc_hash":"%s"}`,
				len(allChildren), len(indexed), len(parents), e.store.Mode(), docHash))
	}

	// 异步建图：实体关系抽取耗时较长，不阻塞主流程
	// 注意：使用 indexed（含真实 PGID）而非 chunks，否则 KG 节点上的 pg_id 缺失，
	// 检索时三路 RRF 融合会拿不到匹配的 PG 行。
	if e.kg != nil && e.kg.Available() && len(indexed) > 0 {
		refs := make([]knowledge.ChunkRef, len(indexed))
		for i, c := range indexed {
			refs[i] = knowledge.ChunkRef{ID: c.ID, PGID: c.PGID, Content: c.Content}
		}
		goSafe("rag.kg-index", func() { e.kg.IndexDocument(docHash, refs) })
	}

	return IngestResult{
		ChunkCount:   len(allChildren),
		ParentCount:  len(parents),
		IndexedCount: len(indexed),
		DocHash:      docHash,
		ChunkPreview: previewChunks(allChildren, 5),
	}
}

func previewChunks(chunks []Chunk, limit int) []Chunk {
	if limit <= 0 || len(chunks) == 0 {
		return nil
	}
	if len(chunks) < limit {
		limit = len(chunks)
	}
	out := make([]Chunk, limit)
	copy(out, chunks[:limit])
	return out
}

// Delete 按 docHash 删除文档的所有 chunks（PG + ES + Milvus + Neo4j KG）
func (e *Engine) Delete(docHash string) error {
	err := e.store.Delete(docHash)
	// 同步删除知识图谱节点
	if e.kg != nil && e.kg.Available() {
		e.kg.DeleteDocument(docHash)
	}
	// 删除后检查是否还有 chunks
	rows, _ := e.chunks.LoadAll()
	e.Loaded = len(rows) > 0
	return err
}

// RestoreChunks 从 PG 恢复 chunks，设置 Loaded 标记
func (e *Engine) RestoreChunks(chunks []Chunk) {
	e.store.RestoreChunks(chunks)
	e.Loaded = len(chunks) > 0
}

// Query 检索知识库并返回答案和检索结果（不带历史，等价于 QueryWithHistory(q, nil)）
func (e *Engine) Query(question string) (string, []SearchResult) {
	return e.QueryWithHistory(question, nil)
}

// QueryWithHistory 是 Query 的多轮版本：把对话历史交给 Rewriter 做 history-aware 改写
//
// 流程：
//  1. Rewriter 把原 query + 历史改写为 N 条等价查询（含独立化后的本句）
//  2. HybridStore.SearchMulti 对每条 query 并发三路检索，跨查询 RRF 合并
//  3. Reranker 对融合后候选做精排，截到 cfg.TopK
//  4. small-to-big：把命中的 chunk 替换为父块原文给 LLM 用
//  5. LLM 合成
func (e *Engine) QueryWithHistory(question string, history []HistoryMessage) (string, []SearchResult) {
	if !e.Loaded {
		return "知识库为空，请先上传文档。", nil
	}

	// 1) 查询改写
	queries := []string{question}
	if e.rewriter != nil {
		rewritten := e.rewriter.Rewrite(question, history)
		if len(rewritten) > 0 {
			queries = rewritten
		}
	}

	// 2) 多查询并发检索 + 跨查询 RRF 合并 + 可选 rerank（在 store 内部完成）
	hybridResults := e.store.SearchMulti(queries, e.cfg.TopK)

	results := make([]SearchResult, 0, len(hybridResults))
	var parts []string
	for _, hr := range hybridResults {
		// 3) small-to-big：优先用父块原文做上下文
		display := hr.Parent
		if display == "" {
			display = hr.Chunk.Content
		}
		results = append(results, SearchResult{
			Chunk:      Chunk{ID: hr.Chunk.ID, Content: display},
			Similarity: hr.Score,
		})
		if display != "" {
			parts = append(parts, display)
		}
	}

	context := strings.Join(parts, "\n\n")
	if context == "" {
		return "知识库中未找到相关内容。", results
	}
	if e.generateFn != nil {
		systemPrompt := "你是一个基于知识库回答问题的助手。请仅根据提供的上下文内容回答问题，不要编造信息。如果上下文不足以回答，请说明。"
		// 给 LLM 的查询用第一条改写（独立化后的版本），它最适合直接回答
		askQuery := question
		if len(queries) > 0 && queries[0] != "" {
			askQuery = queries[0]
		}
		userMsg := fmt.Sprintf("上下文：\n%s\n\n问题：%s", context, askQuery)
		answer := e.generateFn(systemPrompt, userMsg)
		return answer, results
	}
	// 无 LLM 时直接返回检索到的原文
	return fmt.Sprintf("【知识库检索结果】\n%s", context), results
}

// Chunks 返回当前已持久化的切片预览（从 PG 加载，供状态接口使用）
func (e *Engine) Chunks() []Chunk {
	rows, err := e.chunks.LoadAll()
	if err != nil {
		return nil
	}
	chunks := make([]Chunk, len(rows))
	for i, r := range rows {
		chunks[i] = Chunk{ID: i, Content: r.Content}
	}
	return chunks
}

// firstN 返回 s 的前 n 个 rune；用于日志预览时避免把整个 LLM 原始响应写进日志。
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
