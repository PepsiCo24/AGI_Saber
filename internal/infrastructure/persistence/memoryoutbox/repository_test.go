package memoryoutbox_test

import (
	"context"
	"testing"
	"time"

	"agi-assistant/internal/domain/memory/consistency"
	"agi-assistant/internal/infrastructure/persistence/memoryoutbox"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestClaimUsesSkipLockedAndReturnsClaimedEvents(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE SKIP LOCKED`).WithArgs("milvus", 10).
		WillReturnRows(sqlmock.NewRows([]string{"id", "event_id", "aggregate_id", "user_id", "aggregate_version", "event_type", "target", "payload", "attempts", "created_at"}).
			AddRow(1, "e1", 41, "u1", 2, "upsert_memory_vector", "milvus", []byte(`{}`), 0, now))
	mock.ExpectExec(`UPDATE memory_outbox SET status = 'processing'`).WithArgs("w1", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	events, err := memoryoutbox.New(db).Claim(context.Background(), consistency.TargetMilvus, "w1", 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].EventID != "e1" {
		t.Fatalf("events=%+v", events)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
