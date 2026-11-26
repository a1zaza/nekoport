package app

import (
	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/a1zaza/nekoport/internal/storage"
)

// Only revert the failed flag intent, never a later operation or remote identity.
func rollbackOperation(s domain.State, o domain.Operation) []storage.Op {
	if !o.Optimistic || (o.Kind != "seen" && o.Kind != "starred") {
		return nil
	}
	m, ok := s.Messages[o.MessageID]
	if !ok {
		return nil
	}
	for _, newer := range s.Operations {
		if newer.ID != o.ID && newer.MessageID == o.MessageID && newer.Kind == o.Kind && !newer.CreatedAt.Before(o.CreatedAt) {
			return nil
		}
	}
	if o.Kind == "seen" {
		m.Seen = o.PreviousValue
	} else {
		m.Starred = o.PreviousValue
	}
	m.Revision++
	return []storage.Op{storage.Put("messages", m.ID, m)}
}
