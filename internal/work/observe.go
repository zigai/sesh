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

// Observe records sightings: a known conversation updates its item, a new conversation in the
// pane and directory an open item last ran in continues that item, and any other conversation
// starts a new item. A finished conversation seen in another pane was resumed, and one prompted
// again after it finished was continued; either reopens it.
// checkout reads the branch and project of a new item or one that moved. It reports whether
// anything changed.
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

func (s *State) match(seen Sighting) int {
	if index := slices.IndexFunc(s.Items, func(item Item) bool {
		return item.Harness == seen.Harness && item.Conversation == seen.Conversation
	}); index >= 0 {
		return index
	}

	latest := -1

	for index, item := range s.Items {
		continues := !item.Finished() && item.Harness == seen.Harness && item.Server == seen.Server &&
			item.Pane == seen.Pane && item.PanePID == seen.PanePID && item.Directory == seen.Directory
		if continues && (latest < 0 || item.TurnAt > s.Items[latest].TurnAt) {
			latest = index
		}
	}

	return latest
}

func apply(item Item, seen Sighting, now time.Time, checkout Checkout) Item {
	if item.Conversation == "" || item.Directory != seen.Directory {
		item.Branch, item.Project = checkout(seen.Directory)
	}

	moved := item.Server != seen.Server || item.Pane != seen.Pane || item.PanePID != seen.PanePID
	prompt := unixOrZero(seen.PromptAt)

	if item.Finished() && (moved || prompt > item.FinishedAt) {
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

	item.PromptAt = max(item.PromptAt, prompt)

	return item
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
