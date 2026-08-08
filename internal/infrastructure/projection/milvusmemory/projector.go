package milvusmemory

import (
	"agi-assistant/internal/domain/memory/consistency"
	"agi-assistant/internal/infrastructure/projection/versioned"
)

type Store = versioned.Store
type Projector struct{ *versioned.Projector }

func New(s Store) *Projector {
	return &Projector{&versioned.Projector{Store: s, TargetValue: consistency.TargetMilvus, Upserts: map[consistency.EventType]bool{consistency.EventUpsertMemoryVector: true}, Deletes: map[consistency.EventType]bool{consistency.EventDeleteMemoryVector: true}}}
}
