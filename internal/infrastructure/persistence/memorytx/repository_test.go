package memorytx_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"agi-assistant/internal/domain/memory/consistency"
	"agi-assistant/internal/infrastructure/persistence/memorytx"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestRepositoryCreateCommitsMemoryAndOutboxAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO long_term_memory`).
		WithArgs("u1", "用户喜欢咖啡", 0.8, sqlmock.AnyArg(), "preference", sqlmock.AnyArg(), "profile", "", "").
		WillReturnRows(sqlmock.NewRows([]string{"id", "version", "created_at", "updated_at"}).
			AddRow(int64(41), int64(1), now, now))
	mock.ExpectExec(`UPDATE long_term_memory SET content_hash`).
		WithArgs(sqlmock.AnyArg(), int64(41), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectOutboxInsert(mock, 41, consistency.EventUpsertMemoryVector, consistency.TargetMilvus)
	expectOutboxInsert(mock, 41, consistency.EventUpsertMemoryGraphNode, consistency.TargetNeo4j)
	mock.ExpectCommit()

	repo := memorytx.New(db)
	changes, err := repo.Create(context.Background(), memorytx.CreateCommand{
		UserID:     "u1",
		Content:    "用户喜欢咖啡",
		Importance: 0.8,
		Embedding:  []float64{0.1, 0.2},
		Category:   "preference",
		Tags:       []string{"src:user", "preference"},
		SlotHint:   "profile",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Upserts) != 1 {
		t.Fatalf("upserts=%d, want 1", len(changes.Upserts))
	}
	if got := changes.Upserts[0]; got.ID != 41 || got.Version != 1 || got.ContentHash == "" {
		t.Fatalf("unexpected committed record: %+v", got)
	}
	if len(changes.EventIDs) != 2 {
		t.Fatalf("event ids=%d, want 2", len(changes.EventIDs))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryCreateRollsBackWhenOutboxInsertFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO long_term_memory`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "version", "created_at", "updated_at"}).
			AddRow(int64(42), int64(1), now, now))
	mock.ExpectExec(`UPDATE long_term_memory SET content_hash`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectOutboxInsert(mock, 42, consistency.EventUpsertMemoryVector, consistency.TargetMilvus)
	mock.ExpectExec(`INSERT INTO memory_outbox`).
		WillReturnError(errors.New("neo4j outbox unavailable"))
	mock.ExpectRollback()

	repo := memorytx.New(db)
	changes, err := repo.Create(context.Background(), memorytx.CreateCommand{
		UserID:     "u1",
		Content:    "用户喜欢咖啡",
		Importance: 0.8,
		Category:   "preference",
	})
	if err == nil {
		t.Fatal("Create succeeded after outbox failure")
	}
	if len(changes.Upserts) != 0 || len(changes.EventIDs) != 0 {
		t.Fatalf("rollback returned committed changes: %+v", changes)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryUpdateReturnsVersionConflictWithoutOutbox(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE long_term_memory`).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	repo := memorytx.New(db)
	_, err = repo.Update(context.Background(), memorytx.UpdateCommand{
		ID:              41,
		UserID:          "u1",
		ExpectedVersion: 1,
		Content:         "用户更喜欢茶",
		Importance:      0.9,
		Category:        "preference",
	})
	if !errors.Is(err, memorytx.ErrVersionConflict) {
		t.Fatalf("error=%v, want ErrVersionConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryUpdateCommitsNewVersionAndOutbox(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	createdAt := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE long_term_memory`).
		WithArgs(
			"用户更喜欢茶", 0.9, sqlmock.AnyArg(), "preference",
			sqlmock.AnyArg(), "profile", "", "", int64(41), int64(1),
		).
		WillReturnRows(sqlmock.NewRows([]string{"version", "created_at", "updated_at"}).
			AddRow(int64(2), createdAt, updatedAt))
	mock.ExpectExec(`UPDATE long_term_memory SET content_hash`).
		WithArgs(sqlmock.AnyArg(), int64(41), int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectOutboxInsertVersion(mock, 41, 2, consistency.EventUpsertMemoryVector, consistency.TargetMilvus)
	expectOutboxInsertVersion(mock, 41, 2, consistency.EventUpsertMemoryGraphNode, consistency.TargetNeo4j)
	mock.ExpectCommit()

	repo := memorytx.New(db)
	changes, err := repo.Update(context.Background(), memorytx.UpdateCommand{
		ID:              41,
		UserID:          "u1",
		ExpectedVersion: 1,
		Content:         "用户更喜欢茶",
		Importance:      0.9,
		Embedding:       []float64{0.3, 0.4},
		Category:        "preference",
		Tags:            []string{"preference"},
		SlotHint:        "profile",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Upserts) != 1 || changes.Upserts[0].Version != 2 || changes.Upserts[0].ContentHash == "" {
		t.Fatalf("unexpected committed update: %+v", changes)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryTombstoneCommitsAllDeleteEvents(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE long_term_memory`).
		WithArgs(int64(41), int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"version", "deleted_at", "created_at", "updated_at"}).
			AddRow(int64(4), now, now.Add(-time.Hour), now))
	mock.ExpectExec(`UPDATE long_term_memory SET content_hash`).
		WithArgs(sqlmock.AnyArg(), int64(41), int64(4)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectOutboxInsertVersion(mock, 41, 4, consistency.EventDeleteMemoryVector, consistency.TargetMilvus)
	expectOutboxInsertVersion(mock, 41, 4, consistency.EventDeleteMemoryGraphEdges, consistency.TargetNeo4j)
	expectOutboxInsertVersion(mock, 41, 4, consistency.EventDeleteMemoryGraphNode, consistency.TargetNeo4j)
	mock.ExpectCommit()

	repo := memorytx.New(db)
	changes, err := repo.Tombstone(context.Background(), memorytx.DeleteCommand{
		ID:              41,
		UserID:          "u1",
		ExpectedVersion: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Deletes) != 1 || changes.Deletes[0].Version != 4 || changes.Deletes[0].DeletedAt == nil {
		t.Fatalf("unexpected committed delete: %+v", changes)
	}
	if len(changes.EventIDs) != 3 {
		t.Fatalf("event ids=%d, want 3", len(changes.EventIDs))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryLoadActiveReturnsVersionedNonDeletedRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`FROM long_term_memory WHERE deleted_at IS NULL`).
		WillReturnRows(memoryRows().
			AddRow(
				int64(41), "u1", "用户喜欢咖啡", 0.8, []byte(`[0.1,0.2]`),
				"", "", now, now, now, "preference", "{preference,src:user}", "profile",
				false, "", false, nil, "{2,3}", int64(4), "hash-4", nil,
			))

	repo := memorytx.New(db)
	records, err := repo.LoadActive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records=%d, want 1", len(records))
	}
	got := records[0]
	if got.ID != 41 || got.Version != 4 || got.ContentHash != "hash-4" {
		t.Fatalf("unexpected record: %+v", got)
	}
	if len(got.Embedding) != 2 || len(got.Tags) != 2 || len(got.Supersedes) != 2 {
		t.Fatalf("projection fields not decoded: %+v", got)
	}
	if got.DeletedAt != nil {
		t.Fatalf("active record has tombstone: %+v", got.DeletedAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryLoadPageIncludesTombstonesForReconciliation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`FROM long_term_memory WHERE id > \$1 ORDER BY id LIMIT \$2`).
		WithArgs(int64(40), 100).
		WillReturnRows(memoryRows().
			AddRow(
				int64(41), "u1", "", 0.0, []byte(`[]`),
				"", "", now, now, now, "general", "{}", "",
				false, "", false, nil, "{}", int64(5), "delete-hash", now,
			))

	repo := memorytx.New(db)
	records, err := repo.LoadPage(context.Background(), 40, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].DeletedAt == nil || records[0].Version != 5 {
		t.Fatalf("tombstone not returned: %+v", records)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyConsolidationRollsBackWhenVersionChanged(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, version FROM long_term_memory`).WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"id", "version"}).AddRow(1, 3))
	mock.ExpectRollback()
	_, err := memorytx.New(db).ApplyConsolidation(context.Background(), consistency.ConsolidationPlan{Deletes: []consistency.MemoryDelete{{ID: 1, UserID: "u1", ExpectedVersion: 2}}})
	if !errors.Is(err, memorytx.ErrVersionConflict) {
		t.Fatalf("err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func memoryRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "user_id", "content", "importance", "embedding",
		"embedding_model", "embedding_revision",
		"created_at", "updated_at", "last_accessed",
		"category", "tags", "slot_hint",
		"quarantined", "quarantine_reason",
		"superseded", "superseded_at", "supersedes",
		"version", "content_hash", "deleted_at",
	})
}

func expectOutboxInsert(mock sqlmock.Sqlmock, memoryID int64, eventType consistency.EventType, target consistency.Target) {
	expectOutboxInsertVersion(mock, memoryID, 1, eventType, target)
}

func expectOutboxInsertVersion(mock sqlmock.Sqlmock, memoryID, version int64, eventType consistency.EventType, target consistency.Target) {
	mock.ExpectExec(`INSERT INTO memory_outbox`).
		WithArgs(
			sqlmock.AnyArg(),
			memoryID,
			"u1",
			version,
			string(eventType),
			string(target),
			sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
}
