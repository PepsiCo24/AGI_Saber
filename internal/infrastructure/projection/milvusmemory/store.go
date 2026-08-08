package milvusmemory

import (
	"agi-assistant/internal/domain/memory/consistency"
	"context"
	"fmt"
	mc "github.com/milvus-io/milvus-sdk-go/v2/client"
	"github.com/milvus-io/milvus-sdk-go/v2/entity"
)

const collection = "long_term_memory_vectors"

type MilvusStore struct {
	client mc.Client
	dim    int
}

func NewMilvusStore(c mc.Client, dim int) *MilvusStore { return &MilvusStore{c, dim} }
func (s *MilvusStore) Init(ctx context.Context) error {
	if s.client == nil {
		return fmt.Errorf("milvus unavailable")
	}
	has, err := s.client.HasCollection(ctx, collection)
	if err != nil {
		return err
	}
	if has {
		return s.client.LoadCollection(ctx, collection, false)
	}
	schema := &entity.Schema{CollectionName: collection, Fields: []*entity.Field{{Name: "memory_id", DataType: entity.FieldTypeInt64, PrimaryKey: true}, {Name: "user_id", DataType: entity.FieldTypeVarChar, TypeParams: map[string]string{"max_length": "256"}}, {Name: "version", DataType: entity.FieldTypeInt64}, {Name: "content_hash", DataType: entity.FieldTypeVarChar, TypeParams: map[string]string{"max_length": "64"}}, {Name: "embedding", DataType: entity.FieldTypeFloatVector, TypeParams: map[string]string{"dim": fmt.Sprint(s.dim)}}}}
	if err = s.client.CreateCollection(ctx, schema, 1); err != nil {
		return err
	}
	return s.client.LoadCollection(ctx, collection, false)
}

type row struct {
	ID      int64  `milvus:"memory_id"`
	Version int64  `milvus:"version"`
	Hash    string `milvus:"content_hash"`
}

func (s *MilvusStore) Get(ctx context.Context, id int64) (consistency.ProjectionState, bool, error) {
	rs, err := s.client.Query(ctx, collection, nil, fmt.Sprintf("memory_id == %d", id), []string{"memory_id", "version", "content_hash"})
	if err != nil {
		return consistency.ProjectionState{}, false, err
	}
	var rows []*row
	if err = rs.Unmarshal(&rows); err != nil {
		return consistency.ProjectionState{}, false, err
	}
	if len(rows) == 0 {
		return consistency.ProjectionState{}, false, nil
	}
	return consistency.ProjectionState{ID: rows[0].ID, Version: rows[0].Version, ContentHash: rows[0].Hash}, true, nil
}
func (s *MilvusStore) Upsert(ctx context.Context, r consistency.MemoryRecord) error {
	v := make([]float32, s.dim)
	for i := range r.Embedding {
		if i < len(v) {
			v[i] = float32(r.Embedding[i])
		}
	}
	_, err := s.client.Upsert(ctx, collection, "", entity.NewColumnInt64("memory_id", []int64{r.ID}), entity.NewColumnVarChar("user_id", []string{r.UserID}), entity.NewColumnInt64("version", []int64{r.Version}), entity.NewColumnVarChar("content_hash", []string{r.ContentHash}), entity.NewColumnFloatVector("embedding", s.dim, [][]float32{v}))
	return err
}
func (s *MilvusStore) Delete(ctx context.Context, id, version int64) error {
	return s.client.Delete(ctx, collection, "", fmt.Sprintf("memory_id == %d && version <= %d", id, version))
}
func (s *MilvusStore) ListPage(ctx context.Context, after int64, limit int) ([]consistency.ProjectionState, error) {
	rs, err := s.client.Query(ctx, collection, nil, fmt.Sprintf("memory_id > %d", after), []string{"memory_id", "version", "content_hash"}, mc.WithLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	var rows []*row
	if err = rs.Unmarshal(&rows); err != nil {
		return nil, err
	}
	out := make([]consistency.ProjectionState, len(rows))
	for i, r := range rows {
		out[i] = consistency.ProjectionState{ID: r.ID, Version: r.Version, ContentHash: r.Hash}
	}
	return out, nil
}
