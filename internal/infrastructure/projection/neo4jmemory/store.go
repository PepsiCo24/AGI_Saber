package neo4jmemory

import (
	"agi-assistant/internal/domain/memory/consistency"
	pneo "agi-assistant/internal/infrastructure/platform/neo4j"
	"context"
)

type Neo4jStore struct{ client *pneo.Client }

func NewNeo4jStore(c *pneo.Client) *Neo4jStore { return &Neo4jStore{c} }
func (s *Neo4jStore) Get(ctx context.Context, id int64) (consistency.ProjectionState, bool, error) {
	if s.client == nil || !s.client.Available() {
		return consistency.ProjectionState{}, false, nil
	}
	sess := s.client.Session()
	defer sess.Close(ctx)
	res, err := sess.Run(ctx, `MATCH (m:Memory {memory_id:$id}) RETURN m.version,m.content_hash`, map[string]any{"id": id})
	if err != nil {
		return consistency.ProjectionState{}, false, err
	}
	if !res.Next(ctx) {
		return consistency.ProjectionState{}, false, res.Err()
	}
	v, _ := res.Record().Values[0], true
	h, _ := res.Record().Values[1], true
	return consistency.ProjectionState{ID: id, Version: v.(int64), ContentHash: h.(string)}, true, nil
}
func (s *Neo4jStore) Upsert(ctx context.Context, r consistency.MemoryRecord) error {
	if s.client == nil || !s.client.Available() {
		return nil
	}
	sess := s.client.Session()
	defer sess.Close(ctx)
	_, err := sess.Run(ctx, `MERGE (m:Memory {memory_id:$id}) ON CREATE SET m.version=$version,m.content=$content,m.content_hash=$hash,m.user_id=$user ON MATCH SET m.version=CASE WHEN m.version <= $version THEN $version ELSE m.version END,m.content=CASE WHEN m.version <= $version THEN $content ELSE m.content END,m.content_hash=CASE WHEN m.version <= $version THEN $hash ELSE m.content_hash END`, map[string]any{"id": r.ID, "version": r.Version, "content": r.Content, "hash": r.ContentHash, "user": r.UserID})
	return err
}
func (s *Neo4jStore) Delete(ctx context.Context, id, version int64) error {
	if s.client == nil || !s.client.Available() {
		return nil
	}
	sess := s.client.Session()
	defer sess.Close(ctx)
	_, err := sess.Run(ctx, `MATCH (m:Memory {memory_id:$id}) WHERE m.version <= $version DETACH DELETE m`, map[string]any{"id": id, "version": version})
	return err
}
func (s *Neo4jStore) ListPage(ctx context.Context, after int64, limit int) ([]consistency.ProjectionState, error) {
	if s.client == nil || !s.client.Available() {
		return nil, nil
	}
	sess := s.client.Session()
	defer sess.Close(ctx)
	res, err := sess.Run(ctx, `MATCH (m:Memory) WHERE m.memory_id > $after RETURN m.memory_id,m.version,m.content_hash ORDER BY m.memory_id LIMIT $limit`, map[string]any{"after": after, "limit": int64(limit)})
	if err != nil {
		return nil, err
	}
	var out []consistency.ProjectionState
	for res.Next(ctx) {
		id, _ := res.Record().Values[0], true
		v, _ := res.Record().Values[1], true
		h, _ := res.Record().Values[2], true
		out = append(out, consistency.ProjectionState{ID: id.(int64), Version: v.(int64), ContentHash: h.(string)})
	}
	return out, res.Err()
}
