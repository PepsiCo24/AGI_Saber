// Package rag 混合检索存储：语义向量（Milvus）+ 关键词（ES BM25）+ RRF 融合
package rag

import (
	"crypto/sha256"
	"encoding/json"
	"final/config"
	"final/internal/infra"
	"fmt"
	"log"
	"sort"
)

// ─────────────────────── HybridStore ────────────────────────────────────

// HybridStore 实现企业级混合检索：
//   - Milvus 语义向量检索
//   - Elasticsearch BM25 关键词检索
//   - Reciprocal Rank Fusion 融合两路结果
//   - PostgreSQL chunk 持久化
type HybridStore struct {
	cfg     *config.APIConfig
	inf     *infra.Infrastructure
	embedFn func(text string) ([]float64, error)
	mode    string // "hybrid" | "semantic" | "keyword" | "unavailable"
}

// NewHybridStore 创建混合检索存储，根据基础设施可用性自动选择模式
func NewHybridStore(cfg *config.APIConfig, inf *infra.Infrastructure) *HybridStore {
	hs := &HybridStore{
		cfg:  cfg,
		inf:  inf,
		mode: "unavailable",
	}
	milvusOK := inf.Ready.Milvus == "connected"
	esOK := inf.Ready.ES == "connected"
	switch {
	case milvusOK && esOK:
		hs.mode = "hybrid"
	case milvusOK:
		hs.mode = "semantic"
	case esOK:
		hs.mode = "keyword"
	default:
		hs.mode = "unavailable"
	}
	return hs
}

// SetEmbedFn 注入 Embedding 回调（由 agent 通过 llm.Embed 注入）
func (hs *HybridStore) SetEmbedFn(fn func(text string) ([]float64, error)) {
	hs.embedFn = fn
}

// Mode 返回当前检索模式
func (hs *HybridStore) Mode() string { return hs.mode }

// ─────────────────────── Index ──────────────────────────────────────────

// Index 将 chunks 持久化到 PG + Milvus + ES，返回文档哈希（用于后续删除）
func (hs *HybridStore) Index(chunks []Chunk, docContent string) string {
	// 计算文档哈希（幂等摄入）
	docHash := fmt.Sprintf("%x", sha256.Sum256([]byte(docContent)))[:16]

	var pgIDs []int64
	var contents []string
	var embeddings [][]float32

	for i, c := range chunks {
		// Embedding 向量化
		var emb []float64
		if hs.embedFn != nil {
			emb, _ = hs.embedFn(c.Content)
		}
		embJSON, _ := json.Marshal(emb)

		// 持久化到 PostgreSQL
		pgID, err := hs.inf.SaveRAGChunk(docHash, i, c.Content, embJSON)
		if err != nil {
			log.Printf("⚠️  RAG chunk 写入 PG 失败 (idx=%d): %v", i, err)
			continue
		}

		// 索引到 Elasticsearch
		if hs.inf.Ready.ES == "connected" {
			if err := hs.inf.IndexRAGChunk(pgID, c.Content, docHash, i); err != nil {
				log.Printf("⚠️  RAG chunk 索引到 ES 失败 (pg_id=%d): %v", pgID, err)
			}
		}

		// 收集 Milvus 批量写入数据
		if hs.inf.Ready.Milvus == "connected" && len(emb) > 0 {
			pgIDs = append(pgIDs, pgID)
			contents = append(contents, c.Content)
			emb32 := make([]float32, len(emb))
			for j, v := range emb {
				emb32[j] = float32(v)
			}
			embeddings = append(embeddings, emb32)
		}
	}

	// 批量写入 Milvus
	if len(pgIDs) > 0 {
		if err := hs.inf.InsertRAGChunks(pgIDs, contents, embeddings); err != nil {
			log.Printf("⚠️  RAG chunks 写入 Milvus 失败: %v", err)
		}
	}
	return docHash
}

// Delete 按 doc_hash 删除文档的所有 chunks（PG + ES + Milvus）
func (hs *HybridStore) Delete(docHash string) error {
	pgIDs, err := hs.inf.DeleteRAGChunksByDocHash(docHash)
	if err != nil {
		return fmt.Errorf("delete rag chunks failed: %w", err)
	}
	if len(pgIDs) == 0 {
		return nil
	}
	// 删除 ES 索引
	if hs.inf.Ready.ES == "connected" {
		if err := hs.inf.DeleteRAGChunksFromES(pgIDs); err != nil {
			log.Printf("⚠️  ES 删除 RAG chunks 失败: %v", err)
		}
	}
	// 删除 Milvus 向量
	if hs.inf.Ready.Milvus == "connected" {
		if err := hs.inf.DeleteRAGChunksFromMilvus(pgIDs); err != nil {
			log.Printf("⚠️  Milvus 删除 RAG chunks 失败: %v", err)
		}
	}
	return nil
}

// RestoreChunks 标记 chunks 已从 PG 恢复（由 Engine 设置 Loaded）
func (hs *HybridStore) RestoreChunks(chunks []Chunk) {
	// chunks 已持久化在 PG/Milvus/ES 中，无需额外操作
}

// ─────────────────────── Search ─────────────────────────────────────────

// HybridResult 是混合检索的单条结果
type HybridResult struct {
	Chunk  Chunk   `json:"chunk"`
	Score  float64 `json:"score"`
	Source string  `json:"source"` // "hybrid" | "semantic" | "keyword" | "unavailable"
}

// Search 根据当前模式执行检索
func (hs *HybridStore) Search(query string, topK int) []HybridResult {
	switch hs.mode {
	case "hybrid":
		return hs.searchHybrid(query, topK)
	case "semantic":
		return hs.searchSemantic(query, topK)
	case "keyword":
		return hs.searchKeyword(query, topK)
	default:
		log.Printf("⚠️  检索基础设施不可用（Milvus 和 ES 均未连接）")
		return nil
	}
}

// ─────────────────────── Hybrid: RRF 融合 ──────────────────────────────

// searchHybrid: Milvus 语义 + ES BM25，使用 Reciprocal Rank Fusion 融合
func (hs *HybridStore) searchHybrid(query string, topK int) []HybridResult {
	// 查询向量化
	queryEmb, err := hs.embedFn(query)
	if err != nil {
		log.Printf("⚠️  查询向量化失败，降级到关键词检索: %v", err)
		return hs.searchKeyword(query, topK)
	}
	queryEmb32 := make([]float32, len(queryEmb))
	for i, v := range queryEmb {
		queryEmb32[i] = float32(v)
	}

	// 从两路各取 2*topK 保证融合后有足够候选
	fetchK := topK * 2
	if fetchK < 10 {
		fetchK = 10
	}

	milvusHits, milvusErr := hs.inf.MilvusSearchWithScores("rag_chunks", queryEmb32, fetchK)
	esHits, esErr := hs.inf.SearchRAGChunks(query, fetchK)

	if milvusErr != nil && esErr != nil {
		log.Printf("⚠️  Milvus 和 ES 均检索失败: %v / %v", milvusErr, esErr)
		return nil
	}
	if milvusErr != nil {
		log.Printf("⚠️  Milvus 检索失败，使用关键词检索: %v", milvusErr)
		return hs.searchKeyword(query, topK)
	}
	if esErr != nil {
		log.Printf("⚠️  ES 检索失败，使用语义检索: %v", esErr)
		return hs.searchSemantic(query, topK)
	}

	// Reciprocal Rank Fusion: score(d) = Σ 1/(k + rank_i(d))
	k := hs.cfg.RRFConstantK
	if k <= 0 {
		k = 60
	}

	rrfScores := make(map[int64]float64)
	for rank, hit := range milvusHits {
		rrfScores[hit.ID] += 1.0 / float64(k+rank+1)
	}
	for rank, hit := range esHits {
		rrfScores[hit.PGID] += 1.0 / float64(k+rank+1)
	}

	// 按 RRF 分数排序
	type idScore struct {
		id    int64
		score float64
	}
	var sorted []idScore
	for id, score := range rrfScores {
		sorted = append(sorted, idScore{id, score})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].score > sorted[j].score
	})
	if topK < len(sorted) {
		sorted = sorted[:topK]
	}

	// 从 PG 批量取回 chunk 内容
	var ids []int64
	for _, s := range sorted {
		ids = append(ids, s.id)
	}
	rows, err := hs.inf.LoadRAGChunksByIDs(ids)
	if err != nil {
		log.Printf("⚠️  从 PG 加载 RAG chunk 失败: %v", err)
		return nil
	}

	contentMap := make(map[int64]string)
	for _, r := range rows {
		contentMap[r.ID] = r.Content
	}

	var results []HybridResult
	for _, s := range sorted {
		if content, ok := contentMap[s.id]; ok {
			results = append(results, HybridResult{
				Chunk:  Chunk{Content: content},
				Score:  s.score,
				Source: "hybrid",
			})
		}
	}
	return results
}

// ─────────────────────── Semantic: Milvus ──────────────────────────────

// searchSemantic: 仅 Milvus 语义向量检索
func (hs *HybridStore) searchSemantic(query string, topK int) []HybridResult {
	queryEmb, err := hs.embedFn(query)
	if err != nil {
		log.Printf("⚠️  查询向量化失败: %v", err)
		return nil
	}
	queryEmb32 := make([]float32, len(queryEmb))
	for i, v := range queryEmb {
		queryEmb32[i] = float32(v)
	}

	hits, err := hs.inf.MilvusSearchWithScores("rag_chunks", queryEmb32, topK)
	if err != nil {
		log.Printf("⚠️  Milvus 检索失败: %v", err)
		return nil
	}

	var ids []int64
	for _, h := range hits {
		ids = append(ids, h.ID)
	}
	rows, _ := hs.inf.LoadRAGChunksByIDs(ids)
	contentMap := make(map[int64]string)
	for _, r := range rows {
		contentMap[r.ID] = r.Content
	}

	var results []HybridResult
	for _, h := range hits {
		if c, ok := contentMap[h.ID]; ok {
			results = append(results, HybridResult{
				Chunk:  Chunk{Content: c},
				Score:  float64(h.Distance),
				Source: "semantic",
			})
		}
	}
	return results
}

// ─────────────────────── Keyword: ES BM25 ──────────────────────────────

// searchKeyword: 仅 Elasticsearch BM25 关键词检索
func (hs *HybridStore) searchKeyword(query string, topK int) []HybridResult {
	hits, err := hs.inf.SearchRAGChunks(query, topK)
	if err != nil {
		log.Printf("⚠️  ES 检索失败: %v", err)
		return nil
	}

	var ids []int64
	for _, h := range hits {
		ids = append(ids, h.PGID)
	}
	rows, _ := hs.inf.LoadRAGChunksByIDs(ids)
	contentMap := make(map[int64]string)
	for _, r := range rows {
		contentMap[r.ID] = r.Content
	}

	var results []HybridResult
	for _, h := range hits {
		if c, ok := contentMap[h.PGID]; ok {
			results = append(results, HybridResult{
				Chunk:  Chunk{Content: c},
				Score:  h.Score,
				Source: "keyword",
			})
		}
	}
	return results
}
