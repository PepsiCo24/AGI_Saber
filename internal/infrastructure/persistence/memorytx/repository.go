// Package memorytx implements authoritative long-term-memory mutations.
// Every mutation and its projection events commit in one PostgreSQL
// transaction.
package memorytx

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"agi-assistant/internal/domain/memory/consistency"

	"github.com/lib/pq"
)

type Repository struct {
	db *sql.DB
}

var ErrVersionConflict = errors.New("memorytx: version conflict")

type Store interface {
	Create(context.Context, CreateCommand) (consistency.CommittedChangeSet, error)
	Update(context.Context, UpdateCommand) (consistency.CommittedChangeSet, error)
	Tombstone(context.Context, DeleteCommand) (consistency.CommittedChangeSet, error)
	LoadActive(context.Context) ([]consistency.MemoryRecord, error)
	LoadPage(context.Context, int64, int) ([]consistency.MemoryRecord, error)
	ApplyConsolidation(context.Context, consistency.ConsolidationPlan) (consistency.CommittedChangeSet, error)
}

func (r *Repository) ApplyConsolidation(ctx context.Context, plan consistency.ConsolidationPlan) (changes consistency.CommittedChangeSet, err error) {
	if r.db == nil {
		return changes, errorsUnavailable()
	}
	ids := make([]int64, 0, len(plan.Updates)+len(plan.Deletes))
	expected := map[int64]int64{}
	for _, u := range plan.Updates {
		ids = append(ids, u.Record.ID)
		expected[u.Record.ID] = u.ExpectedVersion
	}
	for _, d := range plan.Deletes {
		ids = append(ids, d.ID)
		expected[d.ID] = d.ExpectedVersion
	}
	if len(ids) == 0 {
		return changes, nil
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return changes, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	rows, err := tx.QueryContext(ctx, `SELECT id, version FROM long_term_memory WHERE id = ANY($1) ORDER BY id FOR UPDATE`, pq.Array(ids))
	if err != nil {
		return changes, err
	}
	seen := 0
	for rows.Next() {
		var id, version int64
		if err = rows.Scan(&id, &version); err != nil {
			rows.Close()
			return changes, err
		}
		seen++
		if expected[id] != version {
			rows.Close()
			return changes, ErrVersionConflict
		}
	}
	rows.Close()
	if seen != len(expected) {
		return changes, ErrVersionConflict
	}
	for _, u := range plan.Updates {
		rec := u.Record
		rec.Version = u.ExpectedVersion + 1
		rec.UpdatedAt = time.Now()
		rec.ContentHash, err = consistency.ComputeContentHash(rec)
		if err != nil {
			return changes, err
		}
		emb, _ := json.Marshal(rec.Embedding)
		res, e := tx.ExecContext(ctx, `UPDATE long_term_memory SET content=$1,importance=$2,embedding=$3,version=$4,updated_at=$5,content_hash=$6 WHERE id=$7 AND version=$8`, rec.Content, rec.Importance, emb, rec.Version, rec.UpdatedAt, rec.ContentHash, rec.ID, u.ExpectedVersion)
		if e != nil {
			return changes, e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return changes, ErrVersionConflict
		}
		eventIDs, e := insertEvents(ctx, tx, rec, []eventSpec{{consistency.EventUpsertMemoryVector, consistency.TargetMilvus}, {consistency.EventUpsertMemoryGraphNode, consistency.TargetNeo4j}, {consistency.EventUpsertMemoryGraphEdges, consistency.TargetNeo4j}})
		if e != nil {
			return changes, e
		}
		changes.EventIDs = append(changes.EventIDs, eventIDs...)
		changes.Upserts = append(changes.Upserts, rec)
	}
	for _, d := range plan.Deletes {
		now := time.Now()
		rec := consistency.MemoryRecord{ID: d.ID, UserID: d.UserID, Version: d.ExpectedVersion + 1, DeletedAt: &now, UpdatedAt: now}
		rec.ContentHash, _ = consistency.ComputeContentHash(rec)
		res, e := tx.ExecContext(ctx, `UPDATE long_term_memory SET deleted_at=$1,superseded=TRUE,superseded_at=$1,version=$2,updated_at=$1,content_hash=$3 WHERE id=$4 AND version=$5`, now, rec.Version, rec.ContentHash, d.ID, d.ExpectedVersion)
		if e != nil {
			return changes, e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return changes, ErrVersionConflict
		}
		eventIDs, e := insertEvents(ctx, tx, rec, []eventSpec{{consistency.EventDeleteMemoryVector, consistency.TargetMilvus}, {consistency.EventDeleteMemoryGraphEdges, consistency.TargetNeo4j}, {consistency.EventDeleteMemoryGraphNode, consistency.TargetNeo4j}})
		if e != nil {
			return changes, e
		}
		changes.EventIDs = append(changes.EventIDs, eventIDs...)
		changes.Deletes = append(changes.Deletes, rec)
	}
	if err = tx.Commit(); err != nil {
		return consistency.CommittedChangeSet{}, err
	}
	committed = true
	return changes, nil
}

func New(db *sql.DB) *Repository {
	return &Repository{db: db}
}

type CreateCommand struct {
	UserID            string
	Content           string
	Importance        float64
	Embedding         []float64
	EmbeddingModel    string
	EmbeddingRevision string
	Category          string
	Tags              []string
	SlotHint          string
	EmitGraphEdges    bool
}

type UpdateCommand struct {
	ID                int64
	UserID            string
	ExpectedVersion   int64
	Content           string
	Importance        float64
	Embedding         []float64
	EmbeddingModel    string
	EmbeddingRevision string
	Category          string
	Tags              []string
	SlotHint          string
	EmitGraphEdges    bool
}

type DeleteCommand struct {
	ID              int64
	UserID          string
	ExpectedVersion int64
}

func (r *Repository) Create(ctx context.Context, cmd CreateCommand) (changes consistency.CommittedChangeSet, err error) {
	if r.db == nil {
		return changes, errorsUnavailable()
	}
	if cmd.UserID == "" {
		return changes, fmt.Errorf("memorytx: user id is required")
	}
	if cmd.Category == "" {
		cmd.Category = "general"
	}
	embeddingJSON, err := json.Marshal(cmd.Embedding)
	if err != nil {
		return changes, fmt.Errorf("memorytx: encode embedding: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return changes, fmt.Errorf("memorytx: begin create: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	record := consistency.MemoryRecord{
		UserID:            cmd.UserID,
		Content:           cmd.Content,
		Importance:        cmd.Importance,
		Embedding:         append([]float64(nil), cmd.Embedding...),
		EmbeddingModel:    cmd.EmbeddingModel,
		EmbeddingRevision: cmd.EmbeddingRevision,
		Category:          cmd.Category,
		Tags:              append([]string(nil), cmd.Tags...),
		SlotHint:          cmd.SlotHint,
	}
	err = tx.QueryRowContext(ctx,
		`INSERT INTO long_term_memory
			(user_id, content, importance, embedding, category, tags, slot_hint,
			 embedding_model, embedding_revision, version, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8, $9, 1, NOW())
		 RETURNING id, version, created_at, updated_at`,
		cmd.UserID, cmd.Content, cmd.Importance, embeddingJSON, cmd.Category,
		pq.Array(cmd.Tags), cmd.SlotHint, cmd.EmbeddingModel, cmd.EmbeddingRevision,
	).Scan(&record.ID, &record.Version, &record.CreatedAt, &record.UpdatedAt)
	if err != nil {
		return changes, fmt.Errorf("memorytx: insert memory: %w", err)
	}
	record.LastAccessed = record.CreatedAt
	record.ContentHash, err = consistency.ComputeContentHash(record)
	if err != nil {
		return changes, fmt.Errorf("memorytx: hash memory: %w", err)
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE long_term_memory SET content_hash = $1 WHERE id = $2 AND version = $3`,
		record.ContentHash, record.ID, record.Version)
	if err != nil {
		return changes, fmt.Errorf("memorytx: save content hash: %w", err)
	}
	if affected, affErr := result.RowsAffected(); affErr != nil || affected != 1 {
		if affErr != nil {
			return changes, fmt.Errorf("memorytx: content hash rows affected: %w", affErr)
		}
		return changes, fmt.Errorf("memorytx: content hash update affected %d rows", affected)
	}

	eventTypes := []eventSpec{
		{consistency.EventUpsertMemoryVector, consistency.TargetMilvus},
		{consistency.EventUpsertMemoryGraphNode, consistency.TargetNeo4j},
	}
	if cmd.EmitGraphEdges {
		eventTypes = append(eventTypes, eventSpec{consistency.EventUpsertMemoryGraphEdges, consistency.TargetNeo4j})
	}
	eventIDs, err := insertEvents(ctx, tx, record, eventTypes)
	if err != nil {
		return consistency.CommittedChangeSet{}, err
	}
	if err = tx.Commit(); err != nil {
		return consistency.CommittedChangeSet{}, fmt.Errorf("memorytx: commit create: %w", err)
	}
	committed = true
	return consistency.CommittedChangeSet{
		Upserts:  []consistency.MemoryRecord{record},
		EventIDs: eventIDs,
	}, nil
}

func (r *Repository) Update(ctx context.Context, cmd UpdateCommand) (changes consistency.CommittedChangeSet, err error) {
	if r.db == nil {
		return changes, errorsUnavailable()
	}
	if cmd.UserID == "" {
		return changes, fmt.Errorf("memorytx: user id is required")
	}
	if cmd.Category == "" {
		cmd.Category = "general"
	}
	embeddingJSON, err := json.Marshal(cmd.Embedding)
	if err != nil {
		return changes, fmt.Errorf("memorytx: encode embedding: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return changes, fmt.Errorf("memorytx: begin update: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	record := consistency.MemoryRecord{
		ID:                cmd.ID,
		UserID:            cmd.UserID,
		Content:           cmd.Content,
		Importance:        cmd.Importance,
		Embedding:         append([]float64(nil), cmd.Embedding...),
		EmbeddingModel:    cmd.EmbeddingModel,
		EmbeddingRevision: cmd.EmbeddingRevision,
		Category:          cmd.Category,
		Tags:              append([]string(nil), cmd.Tags...),
		SlotHint:          cmd.SlotHint,
	}
	err = tx.QueryRowContext(ctx,
		`UPDATE long_term_memory
		 SET content = $1, importance = $2, embedding = $3, category = $4,
		     tags = $5, slot_hint = NULLIF($6, ''), embedding_model = $7,
		     embedding_revision = $8, version = version + 1,
		     updated_at = NOW(), content_hash = ''
		 WHERE id = $9 AND version = $10 AND deleted_at IS NULL
		 RETURNING version, created_at, updated_at`,
		cmd.Content, cmd.Importance, embeddingJSON, cmd.Category, pq.Array(cmd.Tags),
		cmd.SlotHint, cmd.EmbeddingModel, cmd.EmbeddingRevision, cmd.ID, cmd.ExpectedVersion,
	).Scan(&record.Version, &record.CreatedAt, &record.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return changes, ErrVersionConflict
	}
	if err != nil {
		return changes, fmt.Errorf("memorytx: update memory: %w", err)
	}
	record.LastAccessed = record.UpdatedAt
	record.ContentHash, err = consistency.ComputeContentHash(record)
	if err != nil {
		return changes, fmt.Errorf("memorytx: hash memory: %w", err)
	}
	if err = updateContentHash(ctx, tx, record); err != nil {
		return changes, err
	}
	specs := []eventSpec{
		{consistency.EventUpsertMemoryVector, consistency.TargetMilvus},
		{consistency.EventUpsertMemoryGraphNode, consistency.TargetNeo4j},
	}
	if cmd.EmitGraphEdges {
		specs = append(specs, eventSpec{consistency.EventUpsertMemoryGraphEdges, consistency.TargetNeo4j})
	}
	eventIDs, err := insertEvents(ctx, tx, record, specs)
	if err != nil {
		return changes, err
	}
	if err = tx.Commit(); err != nil {
		return consistency.CommittedChangeSet{}, fmt.Errorf("memorytx: commit update: %w", err)
	}
	committed = true
	return consistency.CommittedChangeSet{
		Upserts:  []consistency.MemoryRecord{record},
		EventIDs: eventIDs,
	}, nil
}

func (r *Repository) Tombstone(ctx context.Context, cmd DeleteCommand) (changes consistency.CommittedChangeSet, err error) {
	if r.db == nil {
		return changes, errorsUnavailable()
	}
	if cmd.UserID == "" {
		return changes, fmt.Errorf("memorytx: user id is required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return changes, fmt.Errorf("memorytx: begin tombstone: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	record := consistency.MemoryRecord{ID: cmd.ID, UserID: cmd.UserID}
	err = tx.QueryRowContext(ctx,
		`UPDATE long_term_memory
		 SET deleted_at = NOW(), version = version + 1, updated_at = NOW(), content_hash = ''
		 WHERE id = $1 AND version = $2 AND deleted_at IS NULL
		 RETURNING version, deleted_at, created_at, updated_at`,
		cmd.ID, cmd.ExpectedVersion,
	).Scan(&record.Version, &record.DeletedAt, &record.CreatedAt, &record.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return changes, ErrVersionConflict
	}
	if err != nil {
		return changes, fmt.Errorf("memorytx: tombstone memory: %w", err)
	}
	record.ContentHash, err = consistency.ComputeContentHash(record)
	if err != nil {
		return changes, fmt.Errorf("memorytx: hash tombstone: %w", err)
	}
	if err = updateContentHash(ctx, tx, record); err != nil {
		return changes, err
	}
	eventIDs, err := insertEvents(ctx, tx, record, []eventSpec{
		{consistency.EventDeleteMemoryVector, consistency.TargetMilvus},
		{consistency.EventDeleteMemoryGraphEdges, consistency.TargetNeo4j},
		{consistency.EventDeleteMemoryGraphNode, consistency.TargetNeo4j},
	})
	if err != nil {
		return changes, err
	}
	if err = tx.Commit(); err != nil {
		return consistency.CommittedChangeSet{}, fmt.Errorf("memorytx: commit tombstone: %w", err)
	}
	committed = true
	return consistency.CommittedChangeSet{
		Deletes:  []consistency.MemoryRecord{record},
		EventIDs: eventIDs,
	}, nil
}

const memoryColumns = `id, user_id, content, importance, embedding,
	embedding_model, embedding_revision,
	created_at, updated_at, last_accessed,
	category, tags, COALESCE(slot_hint, ''),
	quarantined, COALESCE(quarantine_reason, ''),
	superseded, superseded_at, supersedes,
	version, content_hash, deleted_at`

func (r *Repository) LoadActive(ctx context.Context) ([]consistency.MemoryRecord, error) {
	if r.db == nil {
		return nil, errorsUnavailable()
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+memoryColumns+`
		 FROM long_term_memory WHERE deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("memorytx: load active: %w", err)
	}
	return scanRecords(rows)
}

// LoadPage returns authoritative rows including tombstones. Reconciliation
// needs tombstones to distinguish target orphans from delayed deletes.
func (r *Repository) LoadPage(ctx context.Context, afterID int64, limit int) ([]consistency.MemoryRecord, error) {
	if r.db == nil {
		return nil, errorsUnavailable()
	}
	if limit <= 0 {
		return nil, fmt.Errorf("memorytx: page limit must be positive")
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+memoryColumns+`
		 FROM long_term_memory WHERE id > $1 ORDER BY id LIMIT $2`,
		afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("memorytx: load page: %w", err)
	}
	return scanRecords(rows)
}

func scanRecords(rows *sql.Rows) ([]consistency.MemoryRecord, error) {
	defer rows.Close()
	var records []consistency.MemoryRecord
	for rows.Next() {
		var (
			record        consistency.MemoryRecord
			embeddingJSON []byte
			tags          pq.StringArray
			supersedes    pq.Int64Array
			supersededAt  sql.NullTime
			deletedAt     sql.NullTime
		)
		if err := rows.Scan(
			&record.ID, &record.UserID, &record.Content, &record.Importance, &embeddingJSON,
			&record.EmbeddingModel, &record.EmbeddingRevision,
			&record.CreatedAt, &record.UpdatedAt, &record.LastAccessed,
			&record.Category, &tags, &record.SlotHint,
			&record.Quarantined, &record.QuarantineReason,
			&record.Superseded, &supersededAt, &supersedes,
			&record.Version, &record.ContentHash, &deletedAt,
		); err != nil {
			return nil, fmt.Errorf("memorytx: scan memory: %w", err)
		}
		if len(embeddingJSON) > 0 {
			if err := json.Unmarshal(embeddingJSON, &record.Embedding); err != nil {
				return nil, fmt.Errorf("memorytx: decode embedding for %d: %w", record.ID, err)
			}
		}
		record.Tags = append([]string(nil), tags...)
		record.Supersedes = make([]int64, len(supersedes))
		copy(record.Supersedes, supersedes)
		if record.Version <= 0 {
			record.Version = 1
		}
		if supersededAt.Valid {
			value := supersededAt.Time
			record.SupersededAt = &value
		}
		if deletedAt.Valid {
			value := deletedAt.Time
			record.DeletedAt = &value
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memorytx: iterate memories: %w", err)
	}
	return records, nil
}

type eventSpec struct {
	eventType consistency.EventType
	target    consistency.Target
}

func updateContentHash(ctx context.Context, tx *sql.Tx, record consistency.MemoryRecord) error {
	result, err := tx.ExecContext(ctx,
		`UPDATE long_term_memory SET content_hash = $1 WHERE id = $2 AND version = $3`,
		record.ContentHash, record.ID, record.Version)
	if err != nil {
		return fmt.Errorf("memorytx: save content hash: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("memorytx: content hash rows affected: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("memorytx: content hash update affected %d rows", affected)
	}
	return nil
}

func insertEvents(ctx context.Context, tx *sql.Tx, record consistency.MemoryRecord, specs []eventSpec) ([]string, error) {
	payload, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("memorytx: encode projection payload: %w", err)
	}
	eventIDs := make([]string, 0, len(specs))
	for _, spec := range specs {
		eventID, idErr := newEventID()
		if idErr != nil {
			return nil, fmt.Errorf("memorytx: event id: %w", idErr)
		}
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO memory_outbox
				(event_id, aggregate_id, user_id, aggregate_version, event_type, target, payload)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			eventID, record.ID, record.UserID, record.Version,
			string(spec.eventType), string(spec.target), payload,
		); err != nil {
			return nil, fmt.Errorf("memorytx: insert %s outbox: %w", spec.eventType, err)
		}
		eventIDs = append(eventIDs, eventID)
	}
	return eventIDs, nil
}

func newEventID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	var dst [36]byte
	hex.Encode(dst[0:8], value[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], value[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], value[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], value[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], value[10:16])
	return string(dst[:]), nil
}

func errorsUnavailable() error {
	return fmt.Errorf("memorytx: postgres unavailable")
}
