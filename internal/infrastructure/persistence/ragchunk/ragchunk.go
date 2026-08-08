// Package ragchunk 是 RAG chunk 的统一仓储。
//
// 一条 chunk 的写入会扇出到三个存储：
//   - PG 持久化原文 + embedding（可恢复源）
//   - Milvus 持久化向量索引（向量近邻搜索）
//   - ES 持久化倒排索引（BM25 关键词搜索）
//
// 三路写入若 Milvus / ES 任一缺席，仍以 PG 为真相源，应用整体可降级。
package ragchunk

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"agi-assistant/internal/pkg/logger"

	es "github.com/elastic/go-elasticsearch/v8"
	milvusClient "github.com/milvus-io/milvus-sdk-go/v2/client"
	"github.com/milvus-io/milvus-sdk-go/v2/entity"
)

// Row 是从 PG 读取的 RAG chunk 行
//
// ParentContent 为父块原文（small-to-big 检索时用于回填给 LLM）。
// 老数据没有父块时为空字符串，调用方应回退到 Content 自身。
type Row struct {
	ID            int64
	Content       string
	ParentContent string
	DocumentID    string
	VersionID     string
	Section       string
}

// ESHit 是 ES BM25 检索的单条结果
type ESHit struct {
	PGID  int64   `json:"pg_id"`
	Score float64 `json:"_score"`
}

// MilvusHit 是 Milvus 向量检索的单条结果（含距离分数）
type MilvusHit struct {
	ID       int64
	Distance float32
}

type Metadata struct {
	DocumentID string
	VersionID  string
	Section    string
}

// Repo RAG chunk 综合仓储接口
type Repo interface {
	// 写入
	SavePG(docHash string, chunkIdx int, content string, embeddingJSON []byte) (int64, error)
	// SavePGWithParent 写入子块同时关联父块原文（small-to-big）
	// parentContent 为空字符串时等价于 SavePG
	SavePGWithParent(docHash string, chunkIdx int, content, parentContent string, embeddingJSON []byte) (int64, error)
	SavePGWithMetadata(docHash string, chunkIdx int, content, parentContent string, embeddingJSON []byte, metadata Metadata) (int64, error)
	IndexES(pgID int64, content, docHash string, chunkIdx int) error
	InsertMilvus(pgIDs []int64, contents []string, embeddings [][]float32) error
	// 读取
	LoadAll() ([]Row, error)
	LoadByIDs(ids []int64) ([]Row, error)
	// 检索
	SearchES(query string, topK int) ([]ESHit, error)
	SearchMilvus(vector []float32, topK int) ([]MilvusHit, error)
	// 删除（按 doc_hash 级联删除三个存储）
	Delete(docHash string) error
	// 启动初始化（创建 collection / index）
	Init(dim int)
	// 后端可用性
	PGAvailable() bool
	MilvusAvailable() bool
	ESAvailable() bool
}

// Store 是默认实现，组合三个底层 client
type Store struct {
	pg     *sql.DB
	milvus milvusClient.Client
	es     *es.Client
}

// NewStore 创建多后端存储；任一参数可为 nil（降级）
func NewStore(pg *sql.DB, milvus milvusClient.Client, esClient *es.Client) *Store {
	return &Store{pg: pg, milvus: milvus, es: esClient}
}

// MilvusAvailable 报告 Milvus 是否可用
func (s *Store) MilvusAvailable() bool { return s.milvus != nil }

// ESAvailable 报告 ES 是否可用
func (s *Store) ESAvailable() bool { return s.es != nil }

// PGAvailable 报告 PostgreSQL 是否可用
func (s *Store) PGAvailable() bool { return s.pg != nil }

// ─────────────────────────────── PG ────────────────────────────────────────

// SavePG upsert chunk 到 PG，返回数据库自增 ID
func (s *Store) SavePG(docHash string, chunkIdx int, content string, embeddingJSON []byte) (int64, error) {
	return s.SavePGWithParent(docHash, chunkIdx, content, "", embeddingJSON)
}

// SavePGWithParent upsert chunk 到 PG，同时写入父块原文用于 small-to-big 检索
func (s *Store) SavePGWithParent(docHash string, chunkIdx int, content, parentContent string, embeddingJSON []byte) (int64, error) {
	return s.SavePGWithMetadata(docHash, chunkIdx, content, parentContent, embeddingJSON, Metadata{})
}

func (s *Store) SavePGWithMetadata(docHash string, chunkIdx int, content, parentContent string, embeddingJSON []byte, metadata Metadata) (int64, error) {
	if s.pg == nil {
		return -1, fmt.Errorf("postgres not connected")
	}
	// parentContent 走 NULLIF：空串当 NULL 存，老逻辑回填一致
	var id int64
	err := s.pg.QueryRow(
		`INSERT INTO rag_chunks (doc_hash, chunk_idx, content, parent_content, embedding, document_id, version_id, section)
		 VALUES ($1, $2, $3, NULLIF($4, ''), $5, NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''))
		 ON CONFLICT (doc_hash, chunk_idx) DO UPDATE
		   SET content = EXCLUDED.content,
		       parent_content = EXCLUDED.parent_content,
		       embedding = EXCLUDED.embedding,
		       document_id = EXCLUDED.document_id,
		       version_id = EXCLUDED.version_id,
		       section = EXCLUDED.section
		 RETURNING id`,
		docHash, chunkIdx, content, parentContent, embeddingJSON, metadata.DocumentID, metadata.VersionID, metadata.Section,
	).Scan(&id)
	if err != nil {
		return -1, fmt.Errorf("save rag chunk failed: %w", err)
	}
	return id, nil
}

// LoadByIDs 按 ID 列表批量读取 chunk（含父块原文）
func (s *Store) LoadByIDs(ids []int64) ([]Row, error) {
	if s.pg == nil || len(ids) == 0 {
		return nil, fmt.Errorf("postgres not connected or empty ids")
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	query := fmt.Sprintf(
		"SELECT id, content, COALESCE(parent_content, ''), COALESCE(document_id, ''), COALESCE(version_id, ''), COALESCE(section, '') FROM rag_chunks WHERE id IN (%s)",
		strings.Join(placeholders, ","),
	)
	rows, err := s.pg.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Row
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.ID, &r.Content, &r.ParentContent, &r.DocumentID, &r.VersionID, &r.Section); err == nil {
			result = append(result, r)
		}
	}
	return result, nil
}

// LoadAll 加载所有 chunk（启动期 TF 索引重建用）
func (s *Store) LoadAll() ([]Row, error) {
	if s.pg == nil {
		return nil, fmt.Errorf("postgres not connected")
	}
	rows, err := s.pg.Query("SELECT id, content, COALESCE(parent_content, ''), COALESCE(document_id, ''), COALESCE(version_id, ''), COALESCE(section, '') FROM rag_chunks ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Row
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.ID, &r.Content, &r.ParentContent, &r.DocumentID, &r.VersionID, &r.Section); err == nil {
			result = append(result, r)
		}
	}
	return result, nil
}

// ─────────────────────────────── ES ────────────────────────────────────────

// EnsureESIndex 创建 rag_chunks ES 索引（如不存在）
func (s *Store) EnsureESIndex() error {
	if s.es == nil {
		return fmt.Errorf("elasticsearch not connected")
	}
	resp, err := s.es.Indices.Exists([]string{"rag_chunks"})
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
	createResp, err := s.es.Indices.Create("rag_chunks", s.es.Indices.Create.WithBody(strings.NewReader(mapping)))
	if err != nil {
		return fmt.Errorf("create rag_chunks ES index failed: %w", err)
	}
	createResp.Body.Close()
	logger.L().Info("es rag_chunks index created")
	return nil
}

// IndexES 索引一条 chunk 到 ES
func (s *Store) IndexES(pgID int64, content, docHash string, chunkIdx int) error {
	if s.es == nil {
		return fmt.Errorf("elasticsearch not connected")
	}
	doc := map[string]interface{}{
		"pg_id":     pgID,
		"content":   content,
		"doc_hash":  docHash,
		"chunk_idx": chunkIdx,
	}
	body, _ := json.Marshal(doc)
	resp, err := s.es.Index("rag_chunks", bytes.NewReader(body),
		s.es.Index.WithDocumentID(fmt.Sprintf("%d", pgID)),
		s.es.Index.WithRefresh("false"),
	)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// SearchES 在 ES 上做 BM25 关键词检索
func (s *Store) SearchES(query string, topK int) ([]ESHit, error) {
	if s.es == nil {
		return nil, fmt.Errorf("elasticsearch not connected")
	}
	q := fmt.Sprintf(`{
		"size": %d,
		"query": {"match": {"content": {"query": %q}}},
		"_source": ["pg_id"]
	}`, topK, query)
	resp, err := s.es.Search(
		s.es.Search.WithIndex("rag_chunks"),
		s.es.Search.WithBody(strings.NewReader(q)),
	)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
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
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	var hits []ESHit
	for _, h := range result.Hits.Hits {
		hits = append(hits, ESHit{PGID: h.Source.PGID, Score: h.Score})
	}
	return hits, nil
}

// ─────────────────────────────── Milvus ─────────────────────────────────────

// EnsureMilvusCollection 创建 / 校验 / 重建 rag_chunks collection
func (s *Store) EnsureMilvusCollection(dim int) error {
	if s.milvus == nil {
		return fmt.Errorf("milvus not connected")
	}
	ctx := context.Background()
	has, err := s.milvus.HasCollection(ctx, "rag_chunks")
	if err != nil {
		return fmt.Errorf("check collection failed: %w", err)
	}
	if has {
		// 检查现有 collection 的向量维度和主键是否匹配
		coll, err := s.milvus.DescribeCollection(ctx, "rag_chunks")
		needRecreate := false
		if err == nil {
			for _, f := range coll.Schema.Fields {
				if f.Name == "embedding" && f.DataType == entity.FieldTypeFloatVector {
					existingDim := f.TypeParams["dim"]
					if existingDim != fmt.Sprintf("%d", dim) {
						logger.L().Warn("milvus rag_chunks dim mismatch, recreating collection",
							"existing", existingDim, "expected", dim)
						needRecreate = true
					}
				}
				// 主键必须是 pg_id，否则搜索返回的 ID 与 PG 不对齐
				if f.Name == "id" && f.PrimaryKey {
					logger.L().Warn("milvus rag_chunks primary key must be pg_id, recreating")
					needRecreate = true
				}
			}
		}
		if needRecreate {
			s.milvus.DropCollection(ctx, "rag_chunks")
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
	if err := s.milvus.CreateCollection(ctx, schema, 1); err != nil {
		return fmt.Errorf("create rag_chunks collection failed: %w", err)
	}
	idx, _ := entity.NewIndexIvfFlat(entity.L2, 128)
	if err := s.milvus.CreateIndex(ctx, "rag_chunks", "embedding", idx, false); err != nil {
		logger.L().Warn("milvus rag_chunks index create failed", "err", err)
	}
	if err := s.milvus.LoadCollection(ctx, "rag_chunks", false); err != nil {
		logger.L().Warn("milvus rag_chunks load failed", "err", err)
	}
	logger.L().Info("milvus rag_chunks collection created")
	return nil
}

// InsertMilvus 批量插入 chunk 向量到 Milvus
func (s *Store) InsertMilvus(pgIDs []int64, contents []string, embeddings [][]float32) error {
	if s.milvus == nil {
		return fmt.Errorf("milvus not connected")
	}
	_, err := s.milvus.Insert(
		context.Background(), "rag_chunks", "",
		entity.NewColumnInt64("pg_id", pgIDs),
		entity.NewColumnVarChar("content", contents),
		entity.NewColumnFloatVector("embedding", len(embeddings[0]), embeddings),
	)
	return err
}

// SearchMilvus 在 Milvus 上做向量近邻检索（含距离分数）
func (s *Store) SearchMilvus(vector []float32, topK int) ([]MilvusHit, error) {
	if s.milvus == nil {
		return nil, fmt.Errorf("milvus not connected")
	}
	sp, _ := entity.NewIndexFlatSearchParam()
	results, err := s.milvus.Search(
		context.Background(), "rag_chunks", []string{},
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

// ─────────────────────────────── 删除（三路级联）───────────────────────────

// Delete 按 doc_hash 删除三个存储中相关的 chunk
func (s *Store) Delete(docHash string) error {
	pgIDs, err := s.deletePG(docHash)
	if err != nil {
		return fmt.Errorf("PG 删除失败: %w", err)
	}
	if len(pgIDs) == 0 {
		return nil
	}
	if s.es != nil {
		if err := s.deleteES(pgIDs); err != nil {
			logger.L().Warn("cascade delete: ES delete failed", "err", err)
		}
	}
	if s.milvus != nil {
		if err := s.deleteMilvus(pgIDs); err != nil {
			logger.L().Warn("cascade delete: Milvus delete failed", "err", err)
		}
	}
	return nil
}

// deletePG 从 PG 删除并返回被删除的 ID
func (s *Store) deletePG(docHash string) ([]int64, error) {
	if s.pg == nil {
		return nil, fmt.Errorf("postgres not connected")
	}
	rows, err := s.pg.Query("SELECT id FROM rag_chunks WHERE doc_hash = $1", docHash)
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
	_, err = s.pg.Exec("DELETE FROM rag_chunks WHERE doc_hash = $1", docHash)
	return ids, err
}

func (s *Store) deleteES(pgIDs []int64) error {
	for _, id := range pgIDs {
		resp, err := s.es.Delete("rag_chunks", fmt.Sprintf("%d", id))
		if err != nil {
			logger.L().Warn("es delete doc failed", "pg_id", id, "err", err)
			continue
		}
		resp.Body.Close()
	}
	return nil
}

func (s *Store) deleteMilvus(pgIDs []int64) error {
	if len(pgIDs) == 0 {
		return nil
	}
	var idStrs []string
	for _, id := range pgIDs {
		idStrs = append(idStrs, fmt.Sprintf("%d", id))
	}
	expr := fmt.Sprintf("pg_id in [%s]", strings.Join(idStrs, ", "))
	return s.milvus.Delete(context.Background(), "rag_chunks", "", expr)
}

// ─────────────────────────────── 启动初始化 ─────────────────────────────────

// Init 启动期建表/建索引
func (s *Store) Init(dim int) {
	if s.MilvusAvailable() {
		if err := s.EnsureMilvusCollection(dim); err != nil {
			logger.L().Warn("milvus rag_chunks init failed", "err", err)
		}
	}
	if s.ESAvailable() {
		if err := s.EnsureESIndex(); err != nil {
			logger.L().Warn("es rag_chunks init failed", "err", err)
		}
	}
}
