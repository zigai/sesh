package model

import "slices"

func (m Manifest) validateLinks() error {
	sessions, windows := make(map[string]bool), make(map[string]bool)
	for _, session := range m.Sessions {
		sessions[session.ID] = true
	}

	for _, window := range m.Windows {
		windows[window.ID] = true
	}

	slots := make(map[struct {
		session string
		index   int
	}]bool)
	active, last, linked := make(map[string]int), make(map[string]int), make(map[string]bool)

	for _, link := range m.Links {
		key := struct {
			session string
			index   int
		}{link.Session, link.Index}
		if !sessions[link.Session] || !windows[link.Window] || link.Index < 0 || slots[key] || link.Active && link.Last {
			return ErrInvalid
		}

		slots[key], linked[link.Window] = true, true
		if link.Active {
			active[link.Session]++
		}

		if link.Last {
			last[link.Session]++
		}
	}

	return validateLinkCoverage(sessions, windows, linked, active, last)
}

func validateLinkCoverage(sessions, windows, linked map[string]bool, active, last map[string]int) error {
	for session := range sessions {
		if active[session] != 1 || last[session] > 1 {
			return ErrInvalid
		}
	}

	if len(linked) != len(windows) {
		return ErrInvalid
	}

	return nil
}

func (m Manifest) validateGroups() error {
	groups, members := make(map[string]bool), make(map[string]string)
	for _, group := range m.Groups {
		if !ValidID(group.ID) || groups[group.ID] || len(group.Members) == 0 {
			return ErrInvalid
		}

		groups[group.ID] = true
		for _, member := range group.Members {
			if members[member] != "" {
				return ErrInvalid
			}

			members[member] = group.ID
			if !sameMembership(m.Links, group.Members[0], member) {
				return ErrInvalid
			}
		}
	}

	for _, session := range m.Sessions {
		if members[session.ID] != session.Group {
			return ErrInvalid
		}

		delete(members, session.ID)
	}

	if len(members) != 0 {
		return ErrInvalid
	}

	return nil
}

func sameMembership(links []Link, first, second string) bool {
	slots := make(map[int]string)

	for _, link := range links {
		if link.Session == first {
			slots[link.Index] = link.Window
		}
	}

	count := 0

	for _, link := range links {
		if link.Session != second {
			continue
		}

		if slots[link.Index] != link.Window {
			return false
		}

		count++
	}

	return count == len(slots)
}

func (m Manifest) Closure(selected []string) ([]string, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}

	known := make(map[string]bool)
	for _, session := range m.Sessions {
		known[session.ID] = true
	}

	wanted := make(map[string]bool)

	for _, id := range selected {
		if !known[id] {
			return nil, ErrInvalid
		}

		wanted[id] = true
	}

	for {
		before := len(wanted)
		windows := make(map[string]bool)

		for _, link := range m.Links {
			if wanted[link.Session] {
				windows[link.Window] = true
			}
		}

		for _, link := range m.Links {
			if windows[link.Window] {
				wanted[link.Session] = true
			}
		}

		if len(wanted) == before {
			break
		}
	}

	result := make([]string, 0, len(wanted))
	for id := range wanted {
		result = append(result, id)
	}

	slices.Sort(result)

	return result, nil
}
