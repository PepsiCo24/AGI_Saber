// Package rag 混合检索存储：语义向量（Milvus）+ 关键词（ES BM25）+ 知识图谱（Neo4j）+ RRF 融合
package rag

import (
	"agi-assistant/config"
	"agi-assistant/internal/domain/knowledge"
	"agi-assistant/internal/infrastructure/persistence/ragchunk"
	"agi-assistant/internal/pkg/logger"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// ─────────────────────── HybridStore ────────────────────────────────────

// HybridStore 实现企业级混合检索：
//   - Milvus 语义向量检索
//   - Elasticsearch BM25 关键词检索
//   - Neo4j 知识图谱实体遍历检索
//   - Reciprocal Rank Fusion 三路融合（单查询）
//   - SearchMulti：跨多条 query 的二级 RRF 合并
//   - PostgreSQL chunk 持久化
//   - 可选 Reranker：在 RRF 之后做精排
type HybridStore struct {
	cfg      *config.APIConfig
	chunks   ragchunk.Repo
	kg       *knowledge.KGStore // 知识图谱，nil 时降级跳过
	embedFn  func(text string) ([]float64, error)
	reranker Reranker // nil 时跳过精排
	mode     string   // "hybrid" | "semantic" | "keyword" | "unavailable"
}

// NewHybridStore 创建混合检索存储，根据基础设施可用性自动选择模式
func NewHybridStore(cfg *config.APIConfig, chunks ragchunk.Repo) *HybridStore {
	hs := &HybridStore{
		cfg:    cfg,
		chunks: chunks,
		mode:   "unavailable",
	}
	if !chunks.PGAvailable() {
		return hs
	}
	milvusOK := chunks.MilvusAvailable()
	esOK := chunks.ESAvailable()
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

// SetKGStore 注入知识图谱存储（由 Engine 在 NewEngine 之后注入，避免循环依赖）
func (hs *HybridStore) SetKGStore(kg *knowledge.KGStore) {
	hs.kg = kg
}

// SetEmbedFn 注入 Embedding 回调（由 agent 通过 llm.Embed 注入）
func (hs *HybridStore) SetEmbedFn(fn func(text string) ([]float64, error)) {
	hs.embedFn = fn
}

// SetReranker 注入精排器
func (hs *HybridStore) SetReranker(r Reranker) { hs.reranker = r }

// Mode 返回当前检索模式
func (hs *HybridStore) Mode() string { return hs.mode }

// ─────────────────────── Index ──────────────────────────────────────────

// IndexedChunk 是单条 chunk 摄入后的持久化引用，包含真实 PG 自增 ID
// 供 KG 索引时建立 Entity → PG ID 的反查链路
type IndexedChunk struct {
	ID      int   // 文档内 chunk idx (0-based)
	PGID    int64 // PostgreSQL 自增 ID
	Content string
}

type IngestMetadata struct {
	DocumentID string
	VersionID  string
	Section    string
}

// Index 旧入口：调用 IndexWithParents(parents=nil) 等价于无父块写入
func (hs *HybridStore) Index(chunks []Chunk, docContent string) (string, []IndexedChunk) {
	return hs.IndexWithParents(chunks, nil, docContent)
}

// IndexWithParents 将 chunks 持久化到 PG + Milvus + ES，并把每条子块对应的父块原文一并写 PG。
// parents[i] 与 chunks[i] 一一对应；parents 为 nil 时退化为旧行为（无 small-to-big）。
//
// 调用方拿到 PGID 后可异步喂给 KG，让 KG 节点上同时持有 pg_id（用于检索期 RRF 融合）
func (hs *HybridStore) IndexWithParents(chunks []Chunk, parents []string, docContent string) (string, []IndexedChunk) {
	return hs.IndexWithParentsAndMetadata(chunks, parents, docContent, IngestMetadata{})
}

func (hs *HybridStore) IndexWithParentsAndMetadata(chunks []Chunk, parents []string, docContent string, metadata IngestMetadata) (string, []IndexedChunk) {
	// 计算文档哈希（幂等摄入）
	docHash := fmt.Sprintf("%x", sha256.Sum256([]byte(docContent)))[:16]
	if !hs.chunks.PGAvailable() {
		logger.L().Warn("rag index skipped: postgres unavailable",
			"doc_hash", docHash, "chunks", len(chunks))
		return docHash, nil
	}

	var pgIDs []int64
	var contents []string
	var embeddings [][]float32
	indexed := make([]IndexedChunk, 0, len(chunks))

	for i, c := range chunks {
		// Embedding 向量化
		var emb []float64
		if hs.embedFn != nil {
			emb, _ = hs.embedFn(c.Content)
		}
		embJSON, _ := json.Marshal(emb)

		// 父块原文（无父块时为空串，repo 会落 NULL）
		var parentContent string
		if i < len(parents) {
			parentContent = parents[i]
		}

		// 持久化到 PostgreSQL
		pgID, err := hs.chunks.SavePGWithMetadata(docHash, i, c.Content, parentContent, embJSON, ragchunk.Metadata{
			DocumentID: metadata.DocumentID,
			VersionID:  metadata.VersionID,
			Section:    metadata.Section,
		})
		if err != nil {
			logger.L().Warn("rag chunk write to PG failed", "idx", i, "err", err)
			continue
		}
		indexed = append(indexed, IndexedChunk{ID: i, PGID: pgID, Content: c.Content})

		// 索引到 Elasticsearch
		if hs.chunks.ESAvailable() {
			if err := hs.chunks.IndexES(pgID, c.Content, docHash, i); err != nil {
				logger.L().Warn("rag chunk index to ES failed", "pg_id", pgID, "err", err)
			}
		}

		// 收集 Milvus 批量写入数据
		if hs.chunks.MilvusAvailable() && len(emb) > 0 {
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
		if err := hs.chunks.InsertMilvus(pgIDs, contents, embeddings); err != nil {
			logger.L().Warn("rag chunks batch insert to Milvus failed", "err", err)
		}
	}
	return docHash, indexed
}

// Delete 按 doc_hash 删除文档的所有 chunks（PG + ES + Milvus 三路级联）
func (hs *HybridStore) Delete(docHash string) error {
	return hs.chunks.Delete(docHash)
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
	// Parent 是该 chunk 对应的父块原文（small-to-big 检索的回填字段）
	// 为空字符串时表示无父块（老数据 / 简单切分），调用方应回退到 Chunk.Content
	Parent string `json:"parent,omitempty"`
}

// Search 单查询入口：根据当前模式执行检索；不做 rerank（向后兼容老调用方）
func (hs *HybridStore) Search(query string, topK int) []HybridResult {
	return hs.searchOne(query, topK, false)
}

// SearchMulti 多查询入口：每条 query 独立做 RRF 三路检索，再按 pg_id 跨查询 RRF 合并，
// 最后（若已注入）走一次 rerank 精排，截到 topK。
//
// 多查询合并的设计：
//   - 每条 query 内部三路融合时取 fetchK = 4*topK，单条召回更充分
//   - 跨查询合并继续用 RRF（rank-based 累加），消除不同 query 的分数尺度差异
//   - 跨查询 RRF 之后只截一个较大的候选集（rerankPool），交给 reranker 精排回 topK
func (hs *HybridStore) SearchMulti(queries []string, topK int) []HybridResult {
	if len(queries) == 0 {
		return nil
	}
	if len(queries) == 1 {
		return hs.finalize(queries[0], hs.searchOne(queries[0], hs.rerankPool(topK), false), topK)
	}

	// 1) 多 query 并发各自 RRF 融合
	type queryResult struct {
		query string
		res   []HybridResult
	}
	results := make([]queryResult, len(queries))
	var wg sync.WaitGroup
	wg.Add(len(queries))
	pool := hs.rerankPool(topK)
	for i, q := range queries {
		i, q := i, q
		go func() {
			defer wg.Done()
			results[i] = queryResult{query: q, res: hs.searchOne(q, pool, false)}
		}()
	}
	wg.Wait()

	// 2) 跨查询 RRF 合并：以 chunk content 哈希作为聚合键（PG ID 在 HybridResult 上未直出，
	//    用 content 是稳健做法；同一 chunk 在多 query 中重复命中会被加分）
	k := hs.cfg.RRFConstantK
	if k <= 0 {
		k = 60
	}
	type acc struct {
		score float64
		hr    HybridResult
	}
	merged := make(map[string]*acc, pool)
	for _, qr := range results {
		for rank, hr := range qr.res {
			key := hr.Chunk.Content
			if a, ok := merged[key]; ok {
				a.score += 1.0 / float64(k+rank+1)
				if hr.Score > a.hr.Score {
					a.hr = hr
				}
			} else {
				cp := hr
				cp.Score = 1.0 / float64(k+rank+1)
				merged[key] = &acc{score: cp.Score, hr: cp}
			}
		}
	}

	// 3) 排序 + 截到 rerankPool
	out := make([]HybridResult, 0, len(merged))
	for _, a := range merged {
		hr := a.hr
		hr.Score = a.score
		out = append(out, hr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > pool {
		out = out[:pool]
	}

	// 4) 用第一条 query（独立化后的版本）跑 rerank
	primaryQuery := queries[0]
	return hs.finalize(primaryQuery, out, topK)
}

// rerankPool 召回阶段的候选池大小：rerank 在则 4×topK，否则 2×topK
func (hs *HybridStore) rerankPool(topK int) int {
	pool := topK * 2
	if hs.reranker != nil {
		pool = topK * 4
	}
	if pool < 10 {
		pool = 10
	}
	return pool
}

// finalize 走 rerank（若有）并回填父块内容
func (hs *HybridStore) finalize(query string, results []HybridResult, topK int) []HybridResult {
	if hs.reranker != nil && len(results) > 1 {
		results = hs.reranker.Rerank(query, results, topK)
	} else if topK > 0 && len(results) > topK {
		results = results[:topK]
	}
	// 父块回填靠 LoadByIDs 已经把 ParentContent 一并取回（见 enrichWithParents）
	return results
}

// ─────────────────────── 单查询：模式分发 ────────────────────────────────

// searchOne 执行一次完整的三路 RRF 召回（不做 rerank）
func (hs *HybridStore) searchOne(query string, topK int, _ bool) []HybridResult {
	switch hs.mode {
	case "hybrid":
		return hs.searchHybrid(query, topK)
	case "semantic":
		return hs.searchSemantic(query, topK)
	case "keyword":
		return hs.searchKeyword(query, topK)
	default:
		logger.L().Warn("retrieval infrastructure unavailable (both Milvus and ES not connected)")
		return nil
	}
}

// ─────────────────────── Hybrid: RRF 融合 ──────────────────────────────

// searchHybrid: Milvus 语义 + ES BM25 + 知识图谱，使用 Reciprocal Rank Fusion 三路融合
func (hs *HybridStore) searchHybrid(query string, topK int) []HybridResult {
	// 查询向量化
	queryEmb, err := hs.embedFn(query)
	if err != nil {
		logger.L().Warn("query embed failed, falling back to keyword search", "err", err)
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

	milvusHits, milvusErr := hs.chunks.SearchMilvus(queryEmb32, fetchK)
	esHits, esErr := hs.chunks.SearchES(query, fetchK)

	if milvusErr != nil && esErr != nil {
		logger.L().Warn("both Milvus and ES search failed",
			"milvus_err", milvusErr, "es_err", esErr)
		return nil
	}
	if milvusErr != nil {
		logger.L().Warn("Milvus search failed, using keyword only", "err", milvusErr)
		return hs.searchKeyword(query, topK)
	}
	if esErr != nil {
		logger.L().Warn("ES search failed, using semantic only", "err", esErr)
		return hs.searchSemantic(query, topK)
	}

	// Reciprocal Rank Fusion: score(d) = Σ w_i / (k + rank_i(d))
	//
	// 三路使用统一的 rank-based 评分（避免不同检索源的原始分数尺度不一致），
	// 通过权重控制各路占比：语义/关键词默认 1.0，KG 用 cfg.KGWeight。
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

	// 知识图谱第三路：KG 节点上已持久化 pg_id（见 graph/kgstore.go upsertEntity），
	// 直接用 hit.PGID 累加到 rrfScores 即可与 Milvus / ES 路径正确合并。
	if hs.kg != nil && hs.kg.Available() {
		kgWeight := hs.cfg.KGWeight
		if kgWeight <= 0 {
			kgWeight = 1.0
		}
		kgHits := hs.kg.Search(query, fetchK)
		for rank, hit := range kgHits {
			if hit.PGID == 0 { // 老节点（升级前数据）没有 pg_id，跳过避免污染
				continue
			}
			rrfScores[hit.PGID] += kgWeight / float64(k+rank+1)
		}
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
	rows, err := hs.chunks.LoadByIDs(ids)
	if err != nil {
		logger.L().Warn("load rag chunks from PG failed", "err", err)
		return nil
	}

	type rowInfo struct {
		content string
		parent  string
	}
	rowMap := make(map[int64]rowInfo, len(rows))
	for _, r := range rows {
		rowMap[r.ID] = rowInfo{content: r.Content, parent: r.ParentContent}
	}

	var results []HybridResult
	for _, s := range sorted {
		if ri, ok := rowMap[s.id]; ok {
			results = append(results, HybridResult{
				Chunk:  Chunk{Content: ri.content},
				Score:  s.score,
				Source: "hybrid",
				Parent: ri.parent,
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
		logger.L().Warn("query embed failed", "err", err)
		return nil
	}
	queryEmb32 := make([]float32, len(queryEmb))
	for i, v := range queryEmb {
		queryEmb32[i] = float32(v)
	}

	hits, err := hs.chunks.SearchMilvus(queryEmb32, topK)
	if err != nil {
		logger.L().Warn("Milvus search failed", "err", err)
		return nil
	}

	var ids []int64
	for _, h := range hits {
		ids = append(ids, h.ID)
	}
	rows, _ := hs.chunks.LoadByIDs(ids)
	type rowInfo struct {
		content string
		parent  string
	}
	rowMap := make(map[int64]rowInfo, len(rows))
	for _, r := range rows {
		rowMap[r.ID] = rowInfo{content: r.Content, parent: r.ParentContent}
	}

	var results []HybridResult
	for _, h := range hits {
		if ri, ok := rowMap[h.ID]; ok {
			results = append(results, HybridResult{
				Chunk:  Chunk{Content: ri.content},
				Score:  float64(h.Distance),
				Source: "semantic",
				Parent: ri.parent,
			})
		}
	}
	return results
}

// ─────────────────────── Keyword: ES BM25 ──────────────────────────────

// searchKeyword: 仅 Elasticsearch BM25 关键词检索
func (hs *HybridStore) searchKeyword(query string, topK int) []HybridResult {
	hits, err := hs.chunks.SearchES(query, topK)
	if err != nil {
		logger.L().Warn("ES search failed", "err", err)
		return nil
	}

	var ids []int64
	for _, h := range hits {
		ids = append(ids, h.PGID)
	}
	rows, _ := hs.chunks.LoadByIDs(ids)
	type rowInfo struct {
		content string
		parent  string
	}
	rowMap := make(map[int64]rowInfo, len(rows))
	for _, r := range rows {
		rowMap[r.ID] = rowInfo{content: r.Content, parent: r.ParentContent}
	}

	var results []HybridResult
	for _, h := range hits {
		if ri, ok := rowMap[h.PGID]; ok {
			results = append(results, HybridResult{
				Chunk:  Chunk{Content: ri.content},
				Score:  h.Score,
				Source: "keyword",
				Parent: ri.parent,
			})
		}
	}
	return results
}
