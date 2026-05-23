// Package infra 管理所有外部基础设施连接：Milvus / PostgreSQL / Elasticsearch / Kafka
// 每个连接失败时优雅降级，不影响应用启动。
package infra

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"final/config"
	"fmt"
	"log"
	"strings"
	"time"

	es "github.com/elastic/go-elasticsearch/v8"
	_ "github.com/lib/pq"
	milvusClient "github.com/milvus-io/milvus-sdk-go/v2/client"
	"github.com/milvus-io/milvus-sdk-go/v2/entity"
	"github.com/segmentio/kafka-go"
)

// Status 记录各基础设施的连接状态
type Status struct {
	Milvus string `json:"milvus"`
	PG     string `json:"pg"`
	ES     string `json:"elasticsearch"`
	Kafka  string `json:"kafka"`
}

// Infrastructure 持有所有外部连接句柄
type Infrastructure struct {
	cfg    *config.APIConfig
	milvus milvusClient.Client
	pg     *sql.DB
	es     *es.Client
	kafkaW *kafka.Writer
	Ready  Status
}

// New 尝试连接所有基础设施，失败则降级为内存模式。
func New(cfg *config.APIConfig) *Infrastructure {
	inf := &Infrastructure{cfg: cfg}
	inf.connectMilvus()
	inf.connectPostgres()
	inf.connectES()
	inf.connectKafka()
	return inf
}

// ─────────────────────────────── 连接初始化 ───────────────────────────────

func (inf *Infrastructure) connectMilvus() {
	mc, err := milvusClient.NewClient(context.Background(), milvusClient.Config{
		Address: inf.cfg.MilvusAddr(),
	})
	if err != nil {
		log.Printf("⚠️  Milvus 连接失败: %v (将使用内存向量库)", err)
		inf.Ready.Milvus = "disconnected"
		return
	}
	inf.milvus = mc
	inf.Ready.Milvus = "connected"
	log.Println("✅ Milvus 已连接:", inf.cfg.MilvusAddr())
}

func (inf *Infrastructure) connectPostgres() {
	pg, err := sql.Open("postgres", inf.cfg.PGDSN())
	if err != nil {
		log.Printf("⚠️  PostgreSQL 打开失败: %v", err)
		inf.Ready.PG = "disconnected"
		return
	}
	if err := pg.Ping(); err != nil {
		log.Printf("⚠️  PostgreSQL Ping 失败: %v", err)
		inf.Ready.PG = "disconnected"
		return
	}
	inf.pg = pg
	inf.Ready.PG = "connected"
	inf.initPGSchema()
	log.Println("✅ PostgreSQL 已连接:", inf.cfg.PGDSN())
}

func (inf *Infrastructure) connectES() {
	esCfg := es.Config{
		Addresses: inf.cfg.ESAddresses,
		Username:  inf.cfg.ESUsername,
		Password:  inf.cfg.ESPassword,
	}
	esClient, err := es.NewClient(esCfg)
	if err != nil {
		log.Printf("⚠️  Elasticsearch 连接失败: %v", err)
		inf.Ready.ES = "disconnected"
		return
	}
	res, err := esClient.Info()
	if err != nil {
		log.Printf("⚠️  Elasticsearch Ping 失败: %v", err)
		inf.Ready.ES = "disconnected"
		return
	}
	res.Body.Close()
	inf.es = esClient
	inf.Ready.ES = "connected"
	log.Println("✅ Elasticsearch 已连接:", inf.cfg.ESAddresses)
}

func (inf *Infrastructure) connectKafka() {
	inf.kafkaW = &kafka.Writer{
		Addr:         kafka.TCP(inf.cfg.KafkaBrokers...),
		Topic:        inf.cfg.KafkaTopic,
		Balancer:     &kafka.LeastBytes{},
		BatchTimeout: 10 * time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := kafka.DialLeader(ctx, "tcp", inf.cfg.KafkaBrokers[0], inf.cfg.KafkaTopic, 0)
	if err != nil {
		log.Printf("⚠️  Kafka 连接失败: %v (事件将输出到日志)", err)
		inf.Ready.Kafka = "disconnected"
		return
	}
	conn.Close()
	inf.Ready.Kafka = "connected"
	log.Println("✅ Kafka 已连接:", inf.cfg.KafkaBrokers)
}

// ─────────────────────────────── PostgreSQL ───────────────────────────────

// initPGSchema 建表（幂等）
func (inf *Infrastructure) initPGSchema() {
	if inf.pg == nil {
		return
	}
	ddls := []string{
		`CREATE TABLE IF NOT EXISTS user_preferences (
			user_id    TEXT NOT NULL,
			key        TEXT NOT NULL,
			value      TEXT NOT NULL,
			updated_at TIMESTAMP DEFAULT NOW(),
			PRIMARY KEY (user_id, key)
		)`,
		`CREATE TABLE IF NOT EXISTS task_snapshots (
			task_id    TEXT PRIMARY KEY,
			state      JSONB NOT NULL,
			created_at TIMESTAMP DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS chat_history (
			id         SERIAL PRIMARY KEY,
			role       TEXT NOT NULL,
			content    TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS long_term_memory (
			id            SERIAL PRIMARY KEY,
			content       TEXT NOT NULL,
			importance    FLOAT NOT NULL DEFAULT 0.5,
			embedding     JSONB,
			created_at    TIMESTAMP DEFAULT NOW(),
			last_accessed TIMESTAMP DEFAULT NOW()
		)`,
		`ALTER TABLE long_term_memory ADD COLUMN IF NOT EXISTS last_accessed TIMESTAMP DEFAULT NOW()`,
		`CREATE TABLE IF NOT EXISTS rag_chunks (
			id          BIGSERIAL PRIMARY KEY,
			doc_hash    TEXT NOT NULL,
			chunk_idx   INT NOT NULL,
			content     TEXT NOT NULL,
			embedding   JSONB,
			created_at  TIMESTAMP DEFAULT NOW(),
			UNIQUE(doc_hash, chunk_idx)
		)`,
	}
	for _, ddl := range ddls {
		if _, err := inf.pg.Exec(ddl); err != nil {
			log.Printf("⚠️  PG 建表失败: %v", err)
		}
	}
	log.Println("✅ PostgreSQL 表结构已初始化")
}

// SavePreference 持久化用户偏好到 PostgreSQL（upsert）
func (inf *Infrastructure) SavePreference(userID, key, value string) {
	if inf.pg == nil {
		return
	}
	_, err := inf.pg.Exec(
		`INSERT INTO user_preferences (user_id, key, value) VALUES ($1, $2, $3)
		 ON CONFLICT (user_id, key) DO UPDATE SET value = $3, updated_at = NOW()`,
		userID, key, value,
	)
	if err != nil {
		log.Printf("⚠️  偏好保存到 PG 失败: %v", err)
	}
}

// SaveSnapshot 持久化任务快照到 PostgreSQL（upsert）
func (inf *Infrastructure) SaveSnapshot(taskID string, stateJSON []byte) {
	if inf.pg == nil {
		return
	}
	_, err := inf.pg.Exec(
		`INSERT INTO task_snapshots (task_id, state) VALUES ($1, $2)
		 ON CONFLICT (task_id) DO UPDATE SET state = $2, created_at = NOW()`,
		taskID, stateJSON,
	)
	if err != nil {
		log.Printf("⚠️  快照保存到 PG 失败: %v", err)
	}
}

// LoadPreferences 从 PostgreSQL 加载指定用户的全部偏好，返回 map[key]value
func (inf *Infrastructure) LoadPreferences(userID string) map[string]string {
	result := make(map[string]string)
	if inf.pg == nil {
		return result
	}
	rows, err := inf.pg.Query(`SELECT key, value FROM user_preferences WHERE user_id = $1`, userID)
	if err != nil {
		log.Printf("⚠️  加载偏好失败: %v", err)
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err == nil {
			result[k] = v
		}
	}
	return result
}

// LongTermRow 是从 PG 读取的长期记忆行
type LongTermRow struct {
	ID           int
	Content      string
	Importance   float64
	Embedding    []float64
	CreatedAt    time.Time
	LastAccessed time.Time
}

// SaveLongTermItem 将一条长期记忆持久化到 PostgreSQL，返回数据库自增 ID
func (inf *Infrastructure) SaveLongTermItem(content string, importance float64, embeddingJSON []byte) int {
	if inf.pg == nil {
		return -1
	}
	var id int
	err := inf.pg.QueryRow(
		`INSERT INTO long_term_memory (content, importance, embedding) VALUES ($1, $2, $3) RETURNING id`,
		content, importance, embeddingJSON,
	).Scan(&id)
	if err != nil {
		log.Printf("⚠️  长期记忆保存失败: %v", err)
		return -1
	}
	return id
}

// LoadLongTermItems 从 PostgreSQL 加载全部长期记忆条目
func (inf *Infrastructure) LoadLongTermItems() []LongTermRow {
	if inf.pg == nil {
		return nil
	}
	rows, err := inf.pg.Query(`SELECT id, content, importance, embedding,
		COALESCE(created_at, NOW()), COALESCE(last_accessed, NOW())
		FROM long_term_memory ORDER BY id`)
	if err != nil {
		log.Printf("⚠️  加载长期记忆失败: %v", err)
		return nil
	}
	defer rows.Close()
	var items []LongTermRow
	for rows.Next() {
		var row LongTermRow
		var embJSON []byte
		if err := rows.Scan(&row.ID, &row.Content, &row.Importance, &embJSON, &row.CreatedAt, &row.LastAccessed); err != nil {
			continue
		}
		if len(embJSON) > 0 {
			json.Unmarshal(embJSON, &row.Embedding)
		}
		items = append(items, row)
	}
	return items
}

// ─────────────────────── 长期记忆合并（PG 同步）───────────────────────

// UpdateLongTermItem 更新长期记忆条目的内容和重要性
func (inf *Infrastructure) UpdateLongTermItem(id int, content string, importance float64, embeddingJSON []byte) {
	if inf.pg == nil {
		return
	}
	_, err := inf.pg.Exec(
		`UPDATE long_term_memory SET content = $1, importance = $2, embedding = $3, last_accessed = NOW() WHERE id = $4`,
		content, importance, embeddingJSON, id,
	)
	if err != nil {
		log.Printf("⚠️  长期记忆更新失败 (id=%d): %v", id, err)
	}
}

// DeleteLongTermItems 批量删除长期记忆条目
func (inf *Infrastructure) DeleteLongTermItems(ids []int) {
	if inf.pg == nil || len(ids) == 0 {
		return
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	query := fmt.Sprintf("DELETE FROM long_term_memory WHERE id IN (%s)", strings.Join(placeholders, ","))
	if _, err := inf.pg.Exec(query, args...); err != nil {
		log.Printf("⚠️  长期记忆批量删除失败: %v", err)
	}
}

// ─────────────────────── RAG Chunk 持久化（PostgreSQL）─────────────────────

// ChunkRow 是从 PG 读取的 RAG chunk 行
type ChunkRow struct {
	ID      int64
	Content string
}

// SaveRAGChunk 将一条 RAG chunk 持久化到 PostgreSQL（upsert），返回数据库 ID
func (inf *Infrastructure) SaveRAGChunk(docHash string, chunkIdx int, content string, embeddingJSON []byte) (int64, error) {
	if inf.pg == nil {
		return -1, fmt.Errorf("postgres not connected")
	}
	var id int64
	err := inf.pg.QueryRow(
		`INSERT INTO rag_chunks (doc_hash, chunk_idx, content, embedding) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (doc_hash, chunk_idx) DO UPDATE SET content = EXCLUDED.content, embedding = EXCLUDED.embedding
		 RETURNING id`,
		docHash, chunkIdx, content, embeddingJSON,
	).Scan(&id)
	if err != nil {
		return -1, fmt.Errorf("save rag chunk failed: %w", err)
	}
	return id, nil
}

// LoadRAGChunksByIDs 按 ID 列表从 PostgreSQL 批量加载 RAG chunk
func (inf *Infrastructure) LoadRAGChunksByIDs(ids []int64) ([]ChunkRow, error) {
	if inf.pg == nil || len(ids) == 0 {
		return nil, fmt.Errorf("postgres not connected or empty ids")
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	query := fmt.Sprintf("SELECT id, content FROM rag_chunks WHERE id IN (%s)", strings.Join(placeholders, ","))
	rows, err := inf.pg.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ChunkRow
	for rows.Next() {
		var r ChunkRow
		if err := rows.Scan(&r.ID, &r.Content); err == nil {
			result = append(result, r)
		}
	}
	return result, nil
}

// LoadAllRAGChunks 从 PostgreSQL 加载全部 RAG chunk（用于启动时恢复 TF 索引）
func (inf *Infrastructure) LoadAllRAGChunks() ([]ChunkRow, error) {
	if inf.pg == nil {
		return nil, fmt.Errorf("postgres not connected")
	}
	rows, err := inf.pg.Query("SELECT id, content FROM rag_chunks ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ChunkRow
	for rows.Next() {
		var r ChunkRow
		if err := rows.Scan(&r.ID, &r.Content); err == nil {
			result = append(result, r)
		}
	}
	return result, nil
}

// DeleteRAGChunksByDocHash 按 doc_hash 删除 PG 中的 RAG chunks，返回被删除的 pg_id 列表
func (inf *Infrastructure) DeleteRAGChunksByDocHash(docHash string) ([]int64, error) {
	if inf.pg == nil {
		return nil, fmt.Errorf("postgres not connected")
	}
	// 先查出要删除的 id
	rows, err := inf.pg.Query("SELECT id FROM rag_chunks WHERE doc_hash = $1", docHash)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()

	if len(ids) == 0 {
		return nil, nil
	}

	_, err = inf.pg.Exec("DELETE FROM rag_chunks WHERE doc_hash = $1", docHash)
	if err != nil {
		return nil, fmt.Errorf("delete rag chunks from PG failed: %w", err)
	}
	return ids, nil
}

// DeleteRAGChunksFromES 从 Elasticsearch 中按 pg_id 列表删除文档
func (inf *Infrastructure) DeleteRAGChunksFromES(pgIDs []int64) error {
	if inf.es == nil || len(pgIDs) == 0 {
		return nil
	}
	for _, id := range pgIDs {
		resp, err := inf.es.Delete("rag_chunks", fmt.Sprintf("%d", id))
		if err != nil {
			log.Printf("⚠️  ES 删除文档失败 (pg_id=%d): %v", id, err)
			continue
		}
		resp.Body.Close()
	}
	return nil
}

// DeleteRAGChunksFromMilvus 从 Milvus 中按 pg_id 列表删除向量
func (inf *Infrastructure) DeleteRAGChunksFromMilvus(pgIDs []int64) error {
	if inf.milvus == nil || len(pgIDs) == 0 {
		return nil
	}
	// 构造表达式: pg_id in [1, 2, 3]
	var idStrs []string
	for _, id := range pgIDs {
		idStrs = append(idStrs, fmt.Sprintf("%d", id))
	}
	expr := fmt.Sprintf("pg_id in [%s]", strings.Join(idStrs, ", "))
	return inf.milvus.Delete(context.Background(), "rag_chunks", "", expr)
}

// ─────────────────────────────── Elasticsearch ───────────────────────────

// SearchES 在 Elasticsearch 中执行 JSON 查询，返回原始响应字符串
func (inf *Infrastructure) SearchES(index, queryJSON string) (string, error) {
	if inf.es == nil {
		return "", fmt.Errorf("elasticsearch not connected")
	}
	resp, err := inf.es.Search(
		inf.es.Search.WithIndex(index),
		inf.es.Search.WithBody(strings.NewReader(queryJSON)),
	)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	data, _ := json.Marshal(result)
	return string(data), nil
}

// ─────────────────────── RAG ES 索引管理 ─────────────────────────────────

// EnsureRAGIndex 创建 rag_chunks ES 索引（如不存在）
func (inf *Infrastructure) EnsureRAGIndex() error {
	if inf.es == nil {
		return fmt.Errorf("elasticsearch not connected")
	}
	resp, err := inf.es.Indices.Exists([]string{"rag_chunks"})
	if err == nil && resp.StatusCode == 200 {
		resp.Body.Close()
		return nil
	}
	if resp != nil {
		resp.Body.Close()
	}
	mapping := `{
		"mappings": {
			"properties": {
				"pg_id":     {"type": "long"},
				"content":   {"type": "text", "analyzer": "standard"},
				"doc_hash":  {"type": "keyword"},
				"chunk_idx": {"type": "integer"}
			}
		}
	}`
	createResp, err := inf.es.Indices.Create("rag_chunks", inf.es.Indices.Create.WithBody(strings.NewReader(mapping)))
	if err != nil {
		return fmt.Errorf("create rag_chunks ES index failed: %w", err)
	}
	createResp.Body.Close()
	log.Println("✅ ES rag_chunks 索引已创建")
	return nil
}

// IndexRAGChunk 将一条 RAG chunk 索引到 Elasticsearch
func (inf *Infrastructure) IndexRAGChunk(pgID int64, content, docHash string, chunkIdx int) error {
	if inf.es == nil {
		return fmt.Errorf("elasticsearch not connected")
	}
	doc := map[string]interface{}{
		"pg_id":     pgID,
		"content":   content,
		"doc_hash":  docHash,
		"chunk_idx": chunkIdx,
	}
	body, _ := json.Marshal(doc)
	resp, err := inf.es.Index("rag_chunks", bytes.NewReader(body),
		inf.es.Index.WithDocumentID(fmt.Sprintf("%d", pgID)),
		inf.es.Index.WithRefresh("false"),
	)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ESHit 是 ES BM25 检索的单条结果
type ESHit struct {
	PGID  int64   `json:"pg_id"`
	Score float64 `json:"_score"`
}

// SearchRAGChunks 在 Elasticsearch 中执行 BM25 关键词搜索
func (inf *Infrastructure) SearchRAGChunks(query string, topK int) ([]ESHit, error) {
	if inf.es == nil {
		return nil, fmt.Errorf("elasticsearch not connected")
	}
	q := fmt.Sprintf(`{
		"size": %d,
		"query": {"match": {"content": {"query": %q}}},
		"_source": ["pg_id"]
	}`, topK, query)
	raw, err := inf.SearchES("rag_chunks", q)
	if err != nil {
		return nil, err
	}
	var result struct {
		Hits struct {
			Hits []struct {
				ID     string  `json:"_id"`
				Score  float64 `json:"_score"`
				Source struct {
					PGID int64 `json:"pg_id"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, err
	}
	var hits []ESHit
	for _, h := range result.Hits.Hits {
		hits = append(hits, ESHit{PGID: h.Source.PGID, Score: h.Score})
	}
	return hits, nil
}

// ─────────────────────────────── Milvus ──────────────────────────────────

// MilvusSearch 在 Milvus 中进行向量近邻搜索，返回匹配文档 ID 列表
func (inf *Infrastructure) MilvusSearch(collection string, vector []float32, topK int) ([]int64, error) {
	if inf.milvus == nil {
		return nil, fmt.Errorf("milvus not connected")
	}
	sp, _ := entity.NewIndexFlatSearchParam()
	results, err := inf.milvus.Search(
		context.Background(), collection, []string{},
		"", []string{"content"},
		[]entity.Vector{entity.FloatVector(vector)},
		"embedding", entity.L2,
		topK, sp,
	)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, r := range results {
		for _, id := range r.IDs.FieldData().GetScalars().GetLongData().Data {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// ─────────────────────── RAG Milvus 管理 ─────────────────────────────────

// EnsureRAGCollection 创建 rag_chunks Milvus collection（如不存在或维度不匹配则重建）
func (inf *Infrastructure) EnsureRAGCollection(dim int) error {
	if inf.milvus == nil {
		return fmt.Errorf("milvus not connected")
	}
	ctx := context.Background()
	has, err := inf.milvus.HasCollection(ctx, "rag_chunks")
	if err != nil {
		return fmt.Errorf("check collection failed: %w", err)
	}
	if has {
		// 检查现有 collection 的向量维度和主键是否匹配
		coll, err := inf.milvus.DescribeCollection(ctx, "rag_chunks")
		needRecreate := false
		if err == nil {
			for _, f := range coll.Schema.Fields {
				if f.Name == "embedding" && f.DataType == entity.FieldTypeFloatVector {
					existingDim := f.TypeParams["dim"]
					if existingDim != fmt.Sprintf("%d", dim) {
						log.Printf("⚠️  Milvus rag_chunks 维度不匹配 (现有=%s, 期望=%d)，重建 collection", existingDim, dim)
						needRecreate = true
					}
				}
				// 主键必须是 pg_id，否则搜索返回的 ID 与 PG 不对齐
				if f.Name == "id" && f.PrimaryKey {
					log.Printf("⚠️  Milvus rag_chunks 主键为 id (应为 pg_id)，重建 collection")
					needRecreate = true
				}
			}
		}
		if needRecreate {
			inf.milvus.DropCollection(ctx, "rag_chunks")
			has = false
		}
		if has {
			return nil
		}
	}
	schema := &entity.Schema{
		CollectionName: "rag_chunks",
		Fields: []*entity.Field{
			{Name: "pg_id", DataType: entity.FieldTypeInt64, PrimaryKey: true},
			{Name: "content", DataType: entity.FieldTypeVarChar, TypeParams: map[string]string{"max_length": "4096"}},
			{Name: "embedding", DataType: entity.FieldTypeFloatVector, TypeParams: map[string]string{"dim": fmt.Sprintf("%d", dim)}},
		},
	}
	if err := inf.milvus.CreateCollection(ctx, schema, 1); err != nil {
		return fmt.Errorf("create rag_chunks collection failed: %w", err)
	}
	idx, _ := entity.NewIndexIvfFlat(entity.L2, 128)
	if err := inf.milvus.CreateIndex(ctx, "rag_chunks", "embedding", idx, false); err != nil {
		log.Printf("⚠️  Milvus rag_chunks 索引创建失败: %v", err)
	}
	if err := inf.milvus.LoadCollection(ctx, "rag_chunks", false); err != nil {
		log.Printf("⚠️  Milvus rag_chunks 加载失败: %v", err)
	}
	log.Println("✅ Milvus rag_chunks collection 已创建")
	return nil
}

// InsertRAGChunks 批量将 RAG chunk 向量插入 Milvus
func (inf *Infrastructure) InsertRAGChunks(pgIDs []int64, contents []string, embeddings [][]float32) error {
	if inf.milvus == nil {
		return fmt.Errorf("milvus not connected")
	}
	_, err := inf.milvus.Insert(
		context.Background(), "rag_chunks", "",
		entity.NewColumnInt64("pg_id", pgIDs),
		entity.NewColumnVarChar("content", contents),
		entity.NewColumnFloatVector("embedding", len(embeddings[0]), embeddings),
	)
	return err
}

// MilvusHit 是 Milvus 向量检索的单条结果（含距离分数）
type MilvusHit struct {
	ID       int64
	Distance float32
}

// MilvusSearchWithScores 在 Milvus 中进行向量近邻搜索，返回 ID + 距离
func (inf *Infrastructure) MilvusSearchWithScores(collection string, vector []float32, topK int) ([]MilvusHit, error) {
	if inf.milvus == nil {
		return nil, fmt.Errorf("milvus not connected")
	}
	sp, _ := entity.NewIndexFlatSearchParam()
	results, err := inf.milvus.Search(
		context.Background(), collection, []string{},
		"", []string{"pg_id"},
		[]entity.Vector{entity.FloatVector(vector)},
		"embedding", entity.L2,
		topK, sp,
	)
	if err != nil {
		return nil, err
	}
	var hits []MilvusHit
	for _, r := range results {
		ids := r.IDs.FieldData().GetScalars().GetLongData().Data
		for i, id := range ids {
			hits = append(hits, MilvusHit{ID: id, Distance: r.Scores[i]})
		}
	}
	return hits, nil
}

// InitRAGInfra 初始化 RAG 所需的 Milvus collection 和 ES 索引
func (inf *Infrastructure) InitRAGInfra(dim int) {
	if inf.Ready.Milvus == "connected" {
		if err := inf.EnsureRAGCollection(dim); err != nil {
			log.Printf("⚠️  Milvus rag_chunks 初始化失败: %v", err)
		}
	}
	if inf.Ready.ES == "connected" {
		if err := inf.EnsureRAGIndex(); err != nil {
			log.Printf("⚠️  ES rag_chunks 初始化失败: %v", err)
		}
	}
}

// PublishEvent 向 Kafka 发布事件；未连接时退化为日志输出
func (inf *Infrastructure) PublishEvent(eventType, payload string) {
	msg := kafka.Message{
		Key:   []byte(eventType),
		Value: []byte(payload),
	}
	if inf.Ready.Kafka == "connected" {
		if err := inf.kafkaW.WriteMessages(context.Background(), msg); err != nil {
			log.Printf("⚠️  Kafka 写入失败: %v", err)
		}
	} else {
		log.Printf("📋 [Kafka-fallback] %s: %s", eventType, payload)
	}
}

// ─────────────────────────────── 生命周期 ────────────────────────────────

// SaveChatHistory 持久化一条聊天记录到 PostgreSQL
func (inf *Infrastructure) SaveChatHistory(role, content string) {
	if inf.pg == nil {
		return
	}
	_, err := inf.pg.Exec(
		`INSERT INTO chat_history (role, content) VALUES ($1, $2)`,
		role, content,
	)
	if err != nil {
		log.Printf("⚠️  聊天记录保存到 PG 失败: %v", err)
	}
}

// LoadChatHistory 从 PostgreSQL 加载最近 N 条聊天记录
func (inf *Infrastructure) LoadChatHistory(limit int) []struct {
	Role      string
	Content   string
	CreatedAt string
} {
	if inf.pg == nil {
		return nil
	}
	rows, err := inf.pg.Query(
		`SELECT role, content, TO_CHAR(created_at, 'HH24:MI:SS') FROM chat_history ORDER BY id DESC LIMIT $1`,
		limit,
	)
	if err != nil {
		log.Printf("⚠️  加载聊天记录失败: %v", err)
		return nil
	}
	defer rows.Close()
	var result []struct {
		Role      string
		Content   string
		CreatedAt string
	}
	for rows.Next() {
		var r struct {
			Role      string
			Content   string
			CreatedAt string
		}
		if err := rows.Scan(&r.Role, &r.Content, &r.CreatedAt); err == nil {
			result = append(result, r)
		}
	}
	// 反转为时间正序
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}

// Close 释放所有连接资源
func (inf *Infrastructure) Close() {
	if inf.milvus != nil {
		inf.milvus.Close()
	}
	if inf.pg != nil {
		inf.pg.Close()
	}
	if inf.kafkaW != nil {
		inf.kafkaW.Close()
	}
}
