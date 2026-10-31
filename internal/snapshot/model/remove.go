package model

import (
	"bytes"
	"maps"
	"slices"

	"github.com/samber/oops"
)

type Removal struct {
	Retained  Manifest
	Removed   []string
	Artifacts []string
}

type retention struct {
	sessions map[string]bool
	windows  map[string]bool
}

func (r Removal) Empty() bool {
	return len(r.Retained.Sessions) == 0
}

func RemoveSessions(m Manifest, selected []string) (Removal, error) {
	if err := m.Validate(); err != nil {
		return Removal{}, err
	}

	removed, err := validSelection(m, selected)
	if err != nil {
		return Removal{}, err
	}

	retained, artifacts := retainedGraph(m, removed)

	return Removal{
		Retained:  retained,
		Removed:   slices.Sorted(maps.Keys(removed)),
		Artifacts: artifacts,
	}, nil
}

func validSelection(m Manifest, selected []string) (map[string]bool, error) {
	if len(selected) > MaxEntities {
		return nil, removalFailure("bounds")
	}

	known := make(map[string]bool, len(m.Sessions))
	for _, session := range m.Sessions {
		known[session.ID] = true
	}

	removed := make(map[string]bool, min(len(selected), len(m.Sessions)))

	for _, id := range selected {
		if !ValidID(id) {
			return nil, removalFailure("malformed_session")
		}

		if !known[id] {
			return nil, removalFailure("unknown_session")
		}

		removed[id] = true
	}

	return removed, nil
}

func removalFailure(reason string) error {
	return oops.In("snapshot.model").Code("snapshot_invalid").
		With("operation", "remove", "phase", "selection", "reason", reason).
		Wrap(ErrInvalid)
}

func retentionOf(m Manifest, removed map[string]bool) retention {
	sessions := make(map[string]bool, len(m.Sessions))

	for _, session := range m.Sessions {
		sessions[session.ID] = !removed[session.ID]
	}

	windows := make(map[string]bool, len(m.Windows))

	for _, link := range m.Links {
		if sessions[link.Session] {
			windows[link.Window] = true
		}
	}

	return retention{sessions: sessions, windows: windows}
}

func retainedGraph(m Manifest, removed map[string]bool) (Manifest, []string) {
	survivors := retentionOf(m, removed)

	panes, referenced := survivors.panes(m)
	artifacts, dropped := survivors.artifacts(m, referenced)

	graph := Manifest{
		Format:    m.Format,
		Version:   m.Version,
		Started:   m.Started,
		Finished:  m.Finished,
		Source:    cloneSource(m.Source),
		Evidence:  cloneEvidence(m.Evidence),
		Sessions:  survivors.sessionsOf(m),
		Groups:    survivors.groupsOf(m),
		Windows:   survivors.windowsOf(m),
		Links:     survivors.linksOf(m),
		Panes:     panes,
		Artifacts: artifacts,
	}

	if len(graph.Sessions) == 0 {
		var empty Manifest

		return empty, dropped
	}

	return graph, dropped
}

func (r retention) sessionsOf(m Manifest) []Session {
	sessions := make([]Session, 0, len(m.Sessions))

	for _, session := range m.Sessions {
		if r.sessions[session.ID] {
			sessions = append(sessions, cloneSession(session))
		}
	}

	return sessions
}

func (r retention) groupsOf(m Manifest) []Group {
	if m.Groups == nil {
		return nil
	}

	groups := make([]Group, 0, len(m.Groups))
	for _, group := range m.Groups {
		members := r.members(group.Members)
		if len(members) == 0 {
			continue
		}

		groups = append(groups, Group{ID: group.ID, Name: cloneBytes(group.Name), Members: members})
	}

	return groups
}

func (r retention) members(members []string) []string {
	retained := make([]string, 0, len(members))

	for _, member := range members {
		if r.sessions[member] {
			retained = append(retained, member)
		}
	}

	return retained
}

func (r retention) windowsOf(m Manifest) []Window {
	windows := make([]Window, 0, len(m.Windows))

	for _, window := range m.Windows {
		if r.windows[window.ID] {
			windows = append(windows, cloneWindow(window))
		}
	}

	return windows
}

func (r retention) linksOf(m Manifest) []Link {
	links := make([]Link, 0, len(m.Links))

	for _, link := range m.Links {
		if r.sessions[link.Session] {
			links = append(links, cloneLink(link))
		}
	}

	return links
}

func (r retention) panes(m Manifest) ([]Pane, map[string]bool) {
	panes := make([]Pane, 0, len(m.Panes))
	referenced := make(map[string]bool, len(m.Artifacts))

	for _, pane := range m.Panes {
		if !r.windows[pane.Window] {
			continue
		}

		panes = append(panes, clonePane(pane))

		if pane.Terminal.Artifact != "" {
			referenced[pane.Terminal.Artifact] = true
		}
	}

	return panes, referenced
}

func (r retention) artifacts(m Manifest, referenced map[string]bool) ([]Artifact, []string) {
	if m.Artifacts == nil {
		return nil, nil
	}

	artifacts := make([]Artifact, 0, len(m.Artifacts))
	dropped := make(map[string]bool, len(m.Artifacts))

	for _, artifact := range m.Artifacts {
		if referenced[artifact.ID] {
			artifacts = append(artifacts, artifact)
			continue
		}

		dropped[artifact.ID] = true
	}

	return artifacts, slices.Sorted(maps.Keys(dropped))
}

func cloneSlice[T any](values []T, clone func(T) T) []T {
	if values == nil {
		return nil
	}

	cloned := make([]T, len(values))
	for index, value := range values {
		cloned[index] = clone(value)
	}

	return cloned
}

func cloneBytes(value Bytes) Bytes {
	return bytes.Clone(value)
}

func cloneSource(source Source) Source {
	return Source{
		TmuxVersion:    source.TmuxVersion,
		LibraryVersion: source.LibraryVersion,
		Consistency:    source.Consistency,
		EndpointScope:  source.EndpointScope,
		Options:        cloneOptions(source.Options),
		Environment:    cloneVariables(source.Environment),
	}
}

func cloneEvidence(evidence []Evidence) []Evidence {
	return cloneSlice(evidence, func(item Evidence) Evidence { return item })
}

func cloneSession(session Session) Session {
	return Session{
		ID:          session.ID,
		Name:        cloneBytes(session.Name),
		Directory:   cloneBytes(session.Directory),
		Group:       session.Group,
		Options:     cloneOptions(session.Options),
		Environment: cloneVariables(session.Environment),
	}
}

func cloneWindow(window Window) Window {
	return Window{
		ID:      window.ID,
		Name:    cloneBytes(window.Name),
		Width:   window.Width,
		Height:  window.Height,
		Layout:  cloneLayout(window.Layout),
		Zoomed:  window.Zoomed,
		Options: cloneOptions(window.Options),
	}
}

func cloneLayout(layout Layout) Layout {
	return Layout{
		Width:    layout.Width,
		Height:   layout.Height,
		Left:     layout.Left,
		Top:      layout.Top,
		Pane:     layout.Pane,
		Axis:     layout.Axis,
		Children: cloneSlice(layout.Children, cloneLayout),
	}
}

func cloneLink(link Link) Link {
	return Link{
		Session: link.Session,
		Window:  link.Window,
		Index:   link.Index,
		Active:  link.Active,
		Last:    link.Last,
		Flags:   cloneBytes(link.Flags),
	}
}

func clonePane(pane Pane) Pane {
	return Pane{
		ID:        pane.ID,
		Window:    pane.Window,
		Index:     pane.Index,
		Title:     cloneBytes(pane.Title),
		Directory: cloneBytes(pane.Directory),
		Width:     pane.Width,
		Height:    pane.Height,
		Left:      pane.Left,
		Top:       pane.Top,
		Active:    pane.Active,
		Last:      pane.Last,
		Dead:      pane.Dead,
		Options:   cloneOptions(pane.Options),
		Terminal:  cloneTerminal(pane.Terminal),
		Launch:    cloneLaunch(pane.Launch),
	}
}

func cloneTerminal(terminal Terminal) Terminal {
	return Terminal{
		Artifact:  terminal.Artifact,
		History:   terminal.History,
		Alternate: terminal.Alternate,
		CursorX:   terminal.CursorX,
		CursorY:   terminal.CursorY,
		Mode:      cloneBytes(terminal.Mode),
	}
}

func cloneLaunch(launch Launch) Launch {
	return Launch{
		Executable:  cloneBytes(launch.Executable),
		Arguments:   cloneSlice(launch.Arguments, cloneBytes),
		Directory:   cloneBytes(launch.Directory),
		Environment: cloneVariables(launch.Environment),
		Birth:       cloneBytes(launch.Birth),
		Recovery:    launch.Recovery,
		Reason:      launch.Reason,
	}
}

func cloneOptions(options []Option) []Option {
	return cloneSlice(options, func(option Option) Option {
		return Option{
			Scope:     option.Scope,
			Name:      cloneBytes(option.Name),
			Value:     cloneBytes(option.Value),
			Present:   option.Present,
			Inherited: option.Inherited,
		}
	})
}

func cloneVariables(environment []Variable) []Variable {
	return cloneSlice(environment, func(variable Variable) Variable {
		return Variable{
			Name:   cloneBytes(variable.Name),
			Value:  cloneBytes(variable.Value),
			State:  variable.State,
			Hidden: variable.Hidden,
		}
	})
}
