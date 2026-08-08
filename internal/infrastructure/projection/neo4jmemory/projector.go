package neo4jmemory

import (
	"agi-assistant/internal/domain/memory/consistency"
	"agi-assistant/internal/infrastructure/projection/versioned"
)

type Store = versioned.Store
type Projector struct{ *versioned.Projector }

func New(s Store) *Projector {
	return &Projector{&versioned.Projector{Store: s, TargetValue: consistency.TargetNeo4j, Upserts: map[consistency.EventType]bool{consistency.EventUpsertMemoryGraphNode: true, consistency.EventUpsertMemoryGraphEdges: true}, Deletes: map[consistency.EventType]bool{consistency.EventDeleteMemoryGraphNode: true, consistency.EventDeleteMemoryGraphEdges: true}}}
}
