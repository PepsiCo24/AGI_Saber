// Package rag 实现检索增强生成（Retrieval-Augmented Generation）。
// 包含：文本分割器、混合检索存储（Milvus 语义 + ES BM25 + RRF 融合）、RAG 引擎。
package rag

import (
	"final/config"
	"final/internal/infra"
	"fmt"
	"strings"
)

// ─────────────────────────────── 文本分割 ────────────────────────────────

// Chunk 是文本切片单元
type Chunk struct {
	ID      int    `json:"id"`
	Content string `json:"content"`
}

// TextSplitter 按字符窗口将长文本切成有重叠的 Chunk
type TextSplitter struct {
	chunkSize int
	overlap   int
}

// NewTextSplitter 创建文本分割器
func NewTextSplitter(chunkSize, overlap int) *TextSplitter {
	return &TextSplitter{chunkSize: chunkSize, overlap: overlap}
}

// Split 将文本切分为 Chunk 列表（Unicode 安全）
func (s *TextSplitter) Split(text string) []Chunk {
	var chunks []Chunk
	step := s.chunkSize - s.overlap
	if step <= 0 {
		step = s.chunkSize
	}
	runes := []rune(text)
	id := 0
	for i := 0; i < len(runes); i += step {
		end := i + s.chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, Chunk{ID: id, Content: string(runes[i:end])})
		id++
		if end >= len(runes) {
			break
		}
	}
	return chunks
}

// ─────────────────────────────── 检索结果 ────────────────────────────────

// SearchResult 单条检索结果
type SearchResult struct {
	Chunk      Chunk   `json:"chunk"`
	Similarity float64 `json:"similarity"`
}

// ─────────────────────────────── RAG 引擎 ────────────────────────────────

// Engine 整合文本分割、混合检索与答案生成
type Engine struct {
	cfg        *config.APIConfig
	store      *HybridStore
	splitter   *TextSplitter
	Loaded     bool
	inf        *infra.Infrastructure
	generateFn func(systemPrompt string, userMsg string) string // LLM 回调，由 agent 注入
}

// NewEngine 创建 RAG 引擎
func NewEngine(cfg *config.APIConfig, inf *infra.Infrastructure) *Engine {
	return &Engine{
		cfg:      cfg,
		store:    NewHybridStore(cfg, inf),
		splitter: NewTextSplitter(cfg.ChunkSize, cfg.ChunkOverlap),
		inf:      inf,
	}
}

// SetGenerateFn 注入 LLM 调用回调，供 Query 合成答案
func (e *Engine) SetGenerateFn(fn func(systemPrompt string, userMsg string) string) {
	e.generateFn = fn
}

// SetEmbedFn 注入 Embedding 回调，供 HybridStore 语义向量化
func (e *Engine) SetEmbedFn(fn func(text string) ([]float64, error)) {
	e.store.SetEmbedFn(fn)
}

// Mode 返回当前检索模式
func (e *Engine) Mode() string {
	return e.store.Mode()
}

// Ingest 将文档切分并建立混合检索索引，返回 (chunk数量, docHash)
func (e *Engine) Ingest(doc string) (int, string) {
	chunks := e.splitter.Split(doc)
	docHash := e.store.Index(chunks, doc)
	e.Loaded = true
	e.inf.PublishEvent("rag.ingest", fmt.Sprintf(`{"chunk_count":%d,"mode":"%s","doc_hash":"%s"}`, len(chunks), e.store.Mode(), docHash))
	return len(chunks), docHash
}

// Delete 按 docHash 删除文档的所有 chunks（PG + ES + Milvus）
func (e *Engine) Delete(docHash string) error {
	err := e.store.Delete(docHash)
	// 删除后检查是否还有 chunks
	rows, _ := e.inf.LoadAllRAGChunks()
	e.Loaded = len(rows) > 0
	return err
}

// RestoreChunks 从 PG 恢复 chunks，设置 Loaded 标记
func (e *Engine) RestoreChunks(chunks []Chunk) {
	e.store.RestoreChunks(chunks)
	e.Loaded = len(chunks) > 0
}

// Query 检索知识库并返回答案和检索结果
func (e *Engine) Query(question string) (string, []SearchResult) {
	if !e.Loaded {
		return "知识库为空，请先上传文档。", nil
	}
	hybridResults := e.store.Search(question, e.cfg.TopK)
	// 将 HybridResult 转换为 SearchResult（保持 API 兼容）
	results := make([]SearchResult, len(hybridResults))
	for i, hr := range hybridResults {
		results[i] = SearchResult{
			Chunk:      hr.Chunk,
			Similarity: hr.Score,
		}
	}
	var parts []string
	for _, r := range results {
		if r.Chunk.Content != "" {
			parts = append(parts, r.Chunk.Content)
		}
	}
	context := strings.Join(parts, "\n\n")
	if context == "" {
		return "知识库中未找到相关内容。", results
	}
	if e.generateFn != nil {
		systemPrompt := "你是一个基于知识库回答问题的助手。请仅根据提供的上下文内容回答问题，不要编造信息。如果上下文不足以回答，请说明。"
		userMsg := fmt.Sprintf("上下文：\n%s\n\n问题：%s", context, question)
		answer := e.generateFn(systemPrompt, userMsg)
		return answer, results
	}
	// 无 LLM 时直接返回检索到的原文
	return fmt.Sprintf("【知识库检索结果】\n%s", context), results
}

// Chunks 返回当前已持久化的切片预览（从 PG 加载，供状态接口使用）
func (e *Engine) Chunks() []Chunk {
	rows, err := e.inf.LoadAllRAGChunks()
	if err != nil {
		return nil
	}
	chunks := make([]Chunk, len(rows))
	for i, r := range rows {
		chunks[i] = Chunk{ID: i, Content: r.Content}
	}
	return chunks
}
