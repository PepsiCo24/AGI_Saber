package knowledge

import (
	"agi-assistant/config"
	"agi-assistant/internal/infrastructure/platform/neo4j"
	"agi-assistant/internal/pkg/logger"
	"context"
	"fmt"
	"sort"
)

// KGStore 在 platform/neo4j.Client 之上封装 RAG 专用的图操作：
//   - IndexDocument：文档摄入时写入实体节点和关系边
//   - DeleteDocument：删除文档及其关联的孤立节点
//   - Search：根据查询实体做 1~2 跳子图扩展，返回关联的 ChunkID 列表
//   - ExpandMemoryNeighbors：记忆图扩展（供 memory 包复用）
type KGStore struct {
	neo4j     *neo4j.Client
	maxHops   int
	kgWeight  float64
	extractor *Extractor
}

// NewKGStore 创建知识图谱存储
func NewKGStore(cfg *config.APIConfig, llmFn func(systemPrompt, userMsg string) string) *KGStore {
	c := neo4j.Connect(cfg.Neo4jConfig)
	return &KGStore{
		neo4j:     c,
		maxHops:   cfg.KGMaxHops,
		kgWeight:  cfg.KGWeight,
		extractor: NewExtractor(llmFn),
	}
}

// Available 图存储是否可用
func (ks *KGStore) Available() bool { return ks.neo4j.Available() }

// Close 关闭底层连接
func (ks *KGStore) Close() { ks.neo4j.Close() }

// Client 暴露底层 Neo4j 客户端，供 memory 包共享同一连接驱动记忆图
func (ks *KGStore) Client() *neo4j.Client { return ks.neo4j }

// ─────────────────────────────── 文档摄入 ────────────────────────────────────

// IndexDocument 为一批 chunks 抽取实体关系并写入图
// 以异步方式调用，不阻塞主 Ingest 流程
func (ks *KGStore) IndexDocument(docHash string, chunks []ChunkRef) {
	if !ks.neo4j.Available() {
		return
	}
	for _, c := range chunks {
		result := ks.extractor.Extract(c.Content)
		if len(result.Entities) == 0 {
			continue
		}
		ctx := context.Background()
		// 写入实体节点
		for _, ent := range result.Entities {
			ent.DocHash = docHash
			ent.ChunkID = c.ID
			ent.PGID = c.PGID
			ks.upsertEntity(ctx, ent)
		}
		// 写入关系边
		for _, rel := range result.Relations {
			rel.DocHash = docHash
			rel.ChunkID = c.ID
			rel.PGID = c.PGID
			ks.upsertRelation(ctx, rel)
		}
	}
	logger.L().Info("knowledge graph indexed", "doc_hash", docHash, "chunks", len(chunks))
}

// ChunkRef 是 KGStore 摄入时需要的 chunk 信息（避免直接依赖 rag 包形成循环）
type ChunkRef struct {
	ID      int   // 文档内 chunk idx（0-based）
	PGID    int64 // PostgreSQL 自增 ID，KG 节点上同时持久化以支持 RAG RRF 融合
	Content string
}

// upsertEntity MERGE 实体节点（幂等）
func (ks *KGStore) upsertEntity(ctx context.Context, ent Entity) {
	sess := ks.neo4j.Session()
	defer sess.Close(ctx)
	query := `MERGE (e:Entity {name: $name})
	          SET e.type = $type, e.doc_hash = $doc_hash, e.chunk_id = $chunk_id, e.pg_id = $pg_id`
	_, err := sess.Run(ctx, query, map[string]any{
		"name":     ent.Name,
		"type":     string(ent.Type),
		"doc_hash": ent.DocHash,
		"chunk_id": ent.ChunkID,
		"pg_id":    ent.PGID,
	})
	if err != nil {
		logger.L().Warn("neo4j upsertEntity failed", "name", ent.Name, "err", err)
	}
}

// upsertRelation MERGE 关系边（幂等）
func (ks *KGStore) upsertRelation(ctx context.Context, rel Relation) {
	sess := ks.neo4j.Session()
	defer sess.Close(ctx)
	// 动态关系类型无法用参数传递，必须拼入查询字符串
	// 安全性由 isValidRelType 保证（extractor 已过滤非法类型）
	query := `MERGE (a:Entity {name: $from})
	          MERGE (b:Entity {name: $to})
	          MERGE (a)-[r:` + rel.RelType + ` {doc_hash: $doc_hash}]->(b)
	          SET r.chunk_id = $chunk_id, r.pg_id = $pg_id`
	_, err := sess.Run(ctx, query, map[string]any{
		"from":     rel.FromName,
		"to":       rel.ToName,
		"doc_hash": rel.DocHash,
		"chunk_id": rel.ChunkID,
		"pg_id":    rel.PGID,
	})
	if err != nil {
		logger.L().Warn("neo4j upsertRelation failed",
			"from", rel.FromName, "to", rel.ToName, "err", err)
	}
}

// ─────────────────────────────── 文档删除 ────────────────────────────────────

// DeleteDocument 删除与 docHash 关联的所有关系，并清理孤立实体节点
func (ks *KGStore) DeleteDocument(docHash string) {
	if !ks.neo4j.Available() {
		return
	}
	ctx := context.Background()
	sess := ks.neo4j.Session()
	defer sess.Close(ctx)

	// 删除所有归属此文档的关系
	_, err := sess.Run(ctx,
		`MATCH ()-[r {doc_hash: $doc_hash}]-() DELETE r`,
		map[string]any{"doc_hash": docHash})
	if err != nil {
		logger.L().Warn("neo4j delete document relations failed", "err", err)
	}
	// 清理孤立实体节点
	_, err = sess.Run(ctx,
		`MATCH (e:Entity) WHERE NOT (e)--() AND e.doc_hash = $doc_hash DELETE e`,
		map[string]any{"doc_hash": docHash})
	if err != nil {
		logger.L().Warn("neo4j orphan cleanup failed", "err", err)
	}
}

// ─────────────────────────────── 图检索 ──────────────────────────────────────

// Search 根据查询文本抽取实体，执行 1~2 跳子图遍历，返回关联的 ChunkID
func (ks *KGStore) Search(queryText string, topK int) []GraphSearchResult {
	if !ks.neo4j.Available() {
		return nil
	}

	// 抽取查询中的实体
	extracted := ks.extractor.Extract(queryText)
	if len(extracted.Entities) == 0 {
		return nil
	}

	ctx := context.Background()
	sess := ks.neo4j.Session()
	defer sess.Close(ctx)

	// 构建实体名列表
	names := make([]string, 0, len(extracted.Entities))
	for _, e := range extracted.Entities {
		names = append(names, e.Name)
	}

	// Cypher：从命中节点出发做最多 maxHops 跳遍历，收集相关 chunk_id + pg_id
	// 每跳权重递减（直接命中 > 1跳 > 2跳）
	hops := ks.maxHops
	if hops <= 0 {
		hops = 2
	}
	if hops > 3 { // 防御性 clamp，避免配置错误拖死 Neo4j
		hops = 3
	}
	query := `
	MATCH (e:Entity) WHERE e.name IN $names
	CALL apoc.path.subgraphNodes(e, {
	  maxLevel: $hops,
	  relationshipFilter: "RELATES_TO|PART_OF|CAUSES|DESCRIBES|MENTIONS|WORKS_FOR|LOCATED_IN"
	})
	YIELD node AS neighbor
	WHERE neighbor:Entity AND neighbor.chunk_id IS NOT NULL
	WITH e.name AS seed, neighbor.name AS nb, neighbor.chunk_id AS cid,
	     COALESCE(neighbor.pg_id, 0) AS pgid,
	     toInteger(apoc.node.degree(neighbor)) AS degree
	RETURN cid, pgid, collect(DISTINCT seed) AS seeds, collect(DISTINCT nb) AS neighbors, max(degree) AS deg
	ORDER BY size(seeds) DESC, deg DESC
	LIMIT $limit`

	records, err := sess.Run(ctx, query, map[string]any{
		"names": names,
		"hops":  int64(hops),
		"limit": int64(topK * 3),
	})
	if err != nil {
		// APOC 不可用时降级为直接节点匹配
		return ks.searchDirect(ctx, names, topK)
	}

	// 收集结果
	type rawResult struct {
		chunkID   int
		pgID      int64
		seeds     []string
		neighbors []string
		degree    int64
	}
	var raw []rawResult
	for records.Next(ctx) {
		rec := records.Record()
		cid, _ := rec.Get("cid")
		pgid, _ := rec.Get("pgid")
		seeds, _ := rec.Get("seeds")
		nbs, _ := rec.Get("neighbors")
		deg, _ := rec.Get("deg")

		chunkID := toInt(cid)
		if chunkID < 0 {
			continue
		}
		r := rawResult{
			chunkID:   chunkID,
			pgID:      toInt64(pgid),
			seeds:     toStringSlice(seeds),
			neighbors: toStringSlice(nbs),
			degree:    toInt64(deg),
		}
		raw = append(raw, r)
	}
	if err := records.Err(); err != nil {
		logger.L().Warn("neo4j graph search records error", "err", err)
	}

	// 计算分数：命中种子越多 + 图中心度越高 → 分越高
	seen := make(map[int64]bool)
	var results []GraphSearchResult
	for _, r := range raw {
		if r.pgID == 0 || seen[r.pgID] { // 没有 pg_id 的节点（旧数据）跳过
			continue
		}
		seen[r.pgID] = true
		score := float64(len(r.seeds))*0.6 + float64(r.degree)*0.01
		score *= ks.kgWeight
		results = append(results, GraphSearchResult{
			ChunkID:  r.chunkID,
			PGID:     r.pgID,
			Score:    score,
			Entities: r.seeds,
			HopPath:  r.neighbors,
		})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if len(results) > topK {
		results = results[:topK]
	}
	return results
}

// searchDirect APOC 不可用时的降级版本：直接匹配实体所在 chunk
func (ks *KGStore) searchDirect(ctx context.Context, names []string, topK int) []GraphSearchResult {
	sess := ks.neo4j.Session()
	defer sess.Close(ctx)

	records, err := sess.Run(ctx,
		`MATCH (e:Entity) WHERE e.name IN $names AND e.chunk_id IS NOT NULL
		 RETURN e.chunk_id AS cid, COALESCE(e.pg_id, 0) AS pgid, e.name AS name ORDER BY cid LIMIT $limit`,
		map[string]any{"names": names, "limit": int64(topK)})
	if err != nil {
		return nil
	}

	seen := make(map[int64]bool)
	var results []GraphSearchResult
	for records.Next(ctx) {
		rec := records.Record()
		cid := toInt(rec.Values[0])
		pgid := toInt64(rec.Values[1])
		name := toString(rec.Values[2])
		if pgid == 0 || seen[pgid] {
			continue
		}
		seen[pgid] = true
		results = append(results, GraphSearchResult{
			ChunkID:  cid,
			PGID:     pgid,
			Score:    ks.kgWeight,
			Entities: []string{name},
		})
	}
	return results
}

// ─────────────────────────────── 内部工具 ────────────────────────────────────

func toInt(v any) int {
	switch x := v.(type) {
	case int64:
		return int(x)
	case int:
		return x
	case float64:
		return int(x)
	}
	return -1
}

func toInt64(v any) int64 {
	if x, ok := v.(int64); ok {
		return x
	}
	return 0
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func toStringSlice(v any) []string {
	if arr, ok := v.([]any); ok {
		s := make([]string, 0, len(arr))
		for _, a := range arr {
			if str, ok := a.(string); ok {
				s = append(s, str)
			}
		}
		return s
	}
	return nil
}

func intStr(n int) string {
	return fmt.Sprintf("%d", n)
}
