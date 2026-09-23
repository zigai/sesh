package session

import (
	"cmp"
	"slices"
)

const minSessionsToSort = 2

func SortSessions(sessions []Session, st *State) {
	if len(sessions) < minSessionsToSort {
		return
	}

	slices.SortFunc(sessions, func(a, b Session) int {
		aLastSwitch := int64(0)
		bLastSwitch := int64(0)

		if st != nil {
			aLastSwitch = st.GetLastSwitchIdentity(a.SemanticIdentity())
			bLastSwitch = st.GetLastSwitchIdentity(b.SemanticIdentity())
		}

		if order := cmp.Compare(bLastSwitch, aLastSwitch); order != 0 {
			return order
		}

		if order := b.Activity.Compare(a.Activity); order != 0 {
			return order
		}

		if order := cmp.Compare(a.Name, b.Name); order != 0 {
			return order
		}

		return cmp.Compare(a.EntityKey(), b.EntityKey())
	})
}
