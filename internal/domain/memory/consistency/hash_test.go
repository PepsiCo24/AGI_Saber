package consistency_test

import (
	"testing"

	"agi-assistant/internal/domain/memory/consistency"
)

func TestComputeContentHashIsStableAcrossTagOrder(t *testing.T) {
	a := consistency.MemoryRecord{
		ID:      7,
		UserID:  "u1",
		Content: "用户喜欢咖啡",
		Version: 2,
		Tags:    []string{"src:user", "preference"},
	}
	b := a
	b.Tags = []string{"preference", "src:user"}

	hashA, err := consistency.ComputeContentHash(a)
	if err != nil {
		t.Fatal(err)
	}
	hashB, err := consistency.ComputeContentHash(b)
	if err != nil {
		t.Fatal(err)
	}

	if hashA != hashB {
		t.Fatalf("tag order changed canonical hash: %s != %s", hashA, hashB)
	}
}

func TestComputeContentHashChangesWithVersion(t *testing.T) {
	a := consistency.MemoryRecord{
		ID:      7,
		UserID:  "u1",
		Content: "用户喜欢咖啡",
		Version: 1,
	}
	b := a
	b.Version = 2

	hashA, err := consistency.ComputeContentHash(a)
	if err != nil {
		t.Fatal(err)
	}
	hashB, err := consistency.ComputeContentHash(b)
	if err != nil {
		t.Fatal(err)
	}

	if hashA == hashB {
		t.Fatal("version change did not change projection hash")
	}
}

func TestComputeContentHashDoesNotMutateCallerSlices(t *testing.T) {
	record := consistency.MemoryRecord{
		ID:         9,
		UserID:     "u1",
		Content:    "x",
		Version:    3,
		Tags:       []string{"z", "a"},
		Supersedes: []int64{8, 2},
	}

	if _, err := consistency.ComputeContentHash(record); err != nil {
		t.Fatal(err)
	}

	if record.Tags[0] != "z" || record.Tags[1] != "a" {
		t.Fatalf("hashing mutated tags: %v", record.Tags)
	}
	if record.Supersedes[0] != 8 || record.Supersedes[1] != 2 {
		t.Fatalf("hashing mutated supersedes: %v", record.Supersedes)
	}
}
