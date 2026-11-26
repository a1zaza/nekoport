package domain

// PendingMoves is a display overlay. Canonical mailbox/UID identities and body
// references remain intact until the server confirms a safe UID MOVE.
func PendingMoves(s State) map[string]bool {
	hidden := map[string]bool{}
	for _, o := range s.Operations {
		if (o.State == "queued" || o.State == "running") && (o.Kind == "move" || o.Kind == "trash") {
			if _, exists := s.Messages[o.MessageID]; exists {
				hidden[o.MessageID] = true
			}
		}
	}
	return hidden
}
