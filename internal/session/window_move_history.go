package session

import "slices"

// MoveAgentViews changes only the semantic session owner of the moved panes.
// Missing entries remain unseen; timestamps are never advanced by a move.
// If a destination entry already exists, retain the newer acknowledgment.
func (s *State) MoveAgentViews(source Identity, destination Identity, paneIDs []string) {
	if source.Name == "" || destination.Name == "" || source == destination {
		return
	}

	moved := make([]AgentViewHistoryEntry, 0, len(paneIDs))

	s.AgentViewHistory = slices.DeleteFunc(
		s.AgentViewHistory,
		func(entry AgentViewHistoryEntry) bool {
			if entry.Session == source && slices.Contains(paneIDs, entry.PaneID) {
				entry.Session = destination
				moved = append(moved, entry)

				return true
			}

			return false
		},
	)
	for _, entry := range moved {
		found := false

		for index := range s.AgentViewHistory {
			current := &s.AgentViewHistory[index]
			if current.Session == destination && current.PaneID == entry.PaneID {
				current.ViewedAtUnixNano = max(current.ViewedAtUnixNano, entry.ViewedAtUnixNano)
				found = true

				break
			}
		}

		if !found {
			s.AgentViewHistory = append(s.AgentViewHistory, entry)
		}
	}
}
