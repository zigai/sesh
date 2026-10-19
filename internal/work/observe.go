package work

import (
	"reflect"
	"slices"
	"time"
)

type Sighting struct {
	Harness      string
	Conversation string
	SessionPath  string
	Resume       []string
	Title        string
	Directory    string
	Project      string
	Task         string
	Server       string
	Pane         string
	PanePID      int
	TurnAt       time.Time
	// PromptAt is when the user last sent the agent a prompt, zero when the agent's latest event is
	// not a prompt.
	PromptAt time.Time
}

type Checkout func(dir string) (branch, project string)

// Observe records sightings into their items, starting an item for a conversation no item
// continues, and reports whether anything changed. checkout reads the branch and project of a new
// item or one that moved.
func (s *State) Observe(sightings []Sighting, now time.Time, checkout Checkout) bool {
	changed := false

	for _, seen := range sightings {
		if seen.Conversation == "" || len(seen.Resume) == 0 || seen.Directory == "" {
			continue
		}

		index := s.match(seen)
		if index < 0 {
			item := EmptyItem()
			item.ID = s.NextID()
			item.CreatedAt = now.Unix()
			s.Items = append(s.Items, item)
			index = len(s.Items) - 1
		}

		updated := apply(s.Items[index], seen, now, checkout)
		if !same(updated, s.Items[index]) {
			s.Items[index] = updated
			changed = true
		}
	}

	return changed
}

func (s *State) changedBy(sightings []Sighting, now time.Time) bool {
	probe := *s
	probe.Items = slices.Clone(s.Items)

	return probe.Observe(sightings, now, func(string) (string, string) { return "", "" })
}

func (s *State) match(seen Sighting) int {
	if index := slices.IndexFunc(s.Items, func(item Item) bool {
		return item.Harness == seen.Harness && item.Conversation == seen.Conversation
	}); index >= 0 {
		return index
	}

	latest := -1

	for index, item := range s.Items {
		if continuesItem(item, seen) && (latest < 0 || item.TurnAt > s.Items[latest].TurnAt) {
			latest = index
		}
	}

	return latest
}

func apply(item Item, seen Sighting, now time.Time, checkout Checkout) Item {
	if item.Conversation == "" || item.Directory != seen.Directory {
		item.Branch, item.Project = checkout(seen.Directory)
	}

	if reopenedBy(item, seen) {
		item = item.reopened(now)
	}

	item.Harness = seen.Harness
	item.Conversation = seen.Conversation
	item.SessionPath = seen.SessionPath
	item.Resume = slices.Clone(seen.Resume)
	item.Directory = seen.Directory
	item.Task = seen.Task
	item.Server = seen.Server
	item.Pane = seen.Pane
	item.PanePID = seen.PanePID

	if seen.Project != "" {
		item.Project = seen.Project
	}

	if seen.Title != "" {
		item.Title = seen.Title
	}

	if turn := seen.TurnAt.Unix(); !seen.TurnAt.IsZero() && turn > item.TurnAt {
		item.TurnAt = turn
	}

	item.PromptAt = max(item.PromptAt, unixOrZero(seen.PromptAt))

	return item
}

func continuesItem(item Item, seen Sighting) bool {
	return !item.Finished() && item.Harness == seen.Harness && item.Server == seen.Server &&
		item.Pane == seen.Pane && item.PanePID == seen.PanePID && item.Directory == seen.Directory
}

func reopenedBy(item Item, seen Sighting) bool {
	moved := item.Server != seen.Server || item.Pane != seen.Pane || item.PanePID != seen.PanePID

	return item.Finished() && (moved || unixOrZero(seen.PromptAt) > item.FinishedAt)
}

func same(a, b Item) bool {
	return reflect.DeepEqual(withoutEmptySlices(a), withoutEmptySlices(b))
}

func withoutEmptySlices(item Item) Item {
	if len(item.Resume) == 0 {
		item.Resume = nil
	}

	if len(item.PRs) == 0 {
		item.PRs = nil
	}

	return item
}
