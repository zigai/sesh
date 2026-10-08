package capture

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/snapshot/model"
)

var (
	ErrChanged        = errors.New("tmux state changed during observation")
	ErrSelection      = errors.New("explicit session selection required")
	ErrUnsupported    = errors.New("state cannot be represented faithfully by published API")
	ErrProcessDeleted = errors.New("process executable or working directory was deleted")
)

type SessionNotFoundError struct {
	ID gotmux.SessionID
}

func (e *SessionNotFoundError) Error() string {
	return "no tmux session has ID " + string(e.ID)
}

// graph projects the selected sessions into a manifest. Floating panes are left out because the
// snapshot format models only the tiled layout; the count of omitted panes is returned.
func graph(snapshot gotmux.Snapshot, selected []gotmux.SessionID) (model.Manifest, int, error) {
	var m model.Manifest
	if snapshot.Consistency != gotmux.ConsistencyComplete {
		return m, 0, ErrChanged
	}

	wanted, err := closure(snapshot, selected)
	if err != nil {
		return m, 0, err
	}

	groups := make(map[string]int)

	for _, info := range snapshot.Sessions() {
		if !wanted[info.ID] {
			continue
		}

		session := model.Session{ID: ref("s", string(info.ID)), Name: model.Bytes(info.Name), Directory: model.Bytes(info.Path), Group: "", Options: nil, Environment: nil}
		if name, grouped := info.Group.Get(); grouped {
			index, found := groups[name]
			if !found {
				index = len(m.Groups)
				groups[name] = index
				m.Groups = append(m.Groups, model.Group{ID: "g" + strconv.Itoa(index), Name: model.Bytes(name), Members: nil})
			}

			m.Groups[index].Members = append(m.Groups[index].Members, session.ID)
			session.Group = m.Groups[index].ID
		}

		m.Sessions = append(m.Sessions, session)
	}

	return graphWindows(snapshot, wanted, m)
}

func graphWindows(snapshot gotmux.Snapshot, wanted map[gotmux.SessionID]bool, m model.Manifest) (model.Manifest, int, error) {
	windows := graphLinks(snapshot, wanted, &m)

	layouts, floating, err := graphLayouts(snapshot, windows, &m)
	if err != nil {
		return m, 0, err
	}

	for _, info := range snapshot.Panes() {
		if windows[info.WindowID] && !floating[ref("p", string(info.ID))] {
			m.Panes = append(m.Panes, paneObservation(info))
		}
	}

	for window, layout := range layouts {
		if len(layout.Floating) > 0 {
			retileWindowPanes(snapshot, m.Panes, window, layout)
		}
	}

	return m, len(floating), nil
}

func graphLinks(snapshot gotmux.Snapshot, wanted map[gotmux.SessionID]bool, m *model.Manifest) map[gotmux.WindowID]bool {
	windows := make(map[gotmux.WindowID]bool)

	for _, info := range snapshot.Links() {
		if !wanted[info.SessionID] {
			continue
		}

		windows[info.WindowID] = true
		m.Links = append(m.Links, model.Link{Session: ref("s", string(info.SessionID)), Window: ref("w", string(info.WindowID)), Index: info.Index, Active: info.Active, Last: strings.Contains(info.Flags, "-"), Flags: model.Bytes(info.Flags)})
	}

	return windows
}

// graphLayouts records each selected window with its tiled layout and returns the decoded layouts
// with the set of floating panes they exclude.
func graphLayouts(snapshot gotmux.Snapshot, windows map[gotmux.WindowID]bool, m *model.Manifest) (map[string]integration.WindowLayout, map[string]bool, error) {
	panes := make(map[string]string)

	for _, info := range snapshot.Panes() {
		if windows[info.WindowID] {
			panes[string(info.ID)] = ref("p", string(info.ID))
		}
	}

	layouts := make(map[string]integration.WindowLayout)
	floating := make(map[string]bool)

	for _, info := range snapshot.Windows() {
		if !windows[info.ID] {
			continue
		}

		layout, err := integration.DecodeLayout(info.Layout, panes)
		if err != nil {
			return nil, nil, oops.In("snapshot.capture").
				Code("capture_unsupported").
				With("operation", "decode_layout", "domain", "S03", "reason", "layout_decode").
				Wrap(ErrUnsupported)
		}

		id := ref("w", string(info.ID))
		layouts[id] = layout

		for _, pane := range layout.Floating {
			floating[pane] = true
		}

		m.Windows = append(m.Windows, model.Window{ID: id, Name: model.Bytes(info.Name), Width: info.Width, Height: info.Height, Layout: layout.Tiled, Zoomed: info.Zoomed, Options: nil})
	}

	return layouts, floating, nil
}

// retileWindowPanes renumbers a window's tiled panes as tmux numbers them once its floating
// panes are gone, and moves the active flag to the tiled pane used most recently when a
// floating pane was active.
func retileWindowPanes(snapshot gotmux.Snapshot, panes []model.Pane, window string, layout integration.WindowLayout) {
	base := -1

	for _, info := range snapshot.Panes() {
		if ref("w", string(info.WindowID)) == window && (base < 0 || info.Index < base) {
			base = info.Index
		}
	}

	var tiled []int

	for index := range panes {
		if panes[index].Window == window {
			tiled = append(tiled, index)
		}
	}

	slices.SortFunc(tiled, func(left, right int) int { return panes[left].Index - panes[right].Index })

	active := false

	for rank, index := range tiled {
		panes[index].Index = base + rank
		active = active || panes[index].Active
	}

	if active {
		return
	}

	recent := layout.Recent
	if recent == "" {
		recent = firstLayoutPane(layout.Tiled)
	}

	for _, index := range tiled {
		panes[index].Active = panes[index].ID == recent
	}
}

func firstLayoutPane(node model.Layout) string {
	for node.Pane == "" && len(node.Children) > 0 {
		node = node.Children[0]
	}

	return node.Pane
}

func paneObservation(info gotmux.PaneInfo) model.Pane {
	mode, _ := info.Mode.Get()

	dead := info.Dead || info.PID <= 0

	launch := model.Launch{
		Executable:  nil,
		Arguments:   nil,
		Directory:   nil,
		Environment: nil,
		Birth:       nil,
		Recovery:    model.RecoveryUnverified,
		Reason:      "",
	}

	if dead {
		launch.Recovery = model.RecoveryNonrestartable
		launch.Reason = string(model.ReasonPaneDead)
	}

	return model.Pane{
		ID: ref("p", string(info.ID)), Window: ref("w", string(info.WindowID)), Index: info.Index,
		Title: model.Bytes(info.Title), Directory: model.Bytes(info.CurrentPath), Width: info.Width, Height: info.Height,
		Left: info.Left, Top: info.Top, Active: info.Active, Dead: dead, Options: nil,
		Terminal: model.Terminal{Artifact: "", History: info.HistorySize, Alternate: info.Alternate, CursorX: info.CursorX, CursorY: info.CursorY, Mode: model.Bytes(mode)},
		Launch:   launch,
	}
}

func ref(prefix, id string) string { return prefix + id[1:] }

func closure(snapshot gotmux.Snapshot, selected []gotmux.SessionID) (map[gotmux.SessionID]bool, error) {
	if len(selected) == 0 {
		return nil, ErrSelection
	}

	wanted := make(map[gotmux.SessionID]bool)

	for _, id := range selected {
		if _, found := snapshot.Session(id); !found {
			return nil, &SessionNotFoundError{ID: id}
		}

		wanted[id] = true
	}

	links := snapshot.Links()

	for {
		before := len(wanted)
		windows := make(map[gotmux.WindowID]bool)

		for _, link := range links {
			if wanted[link.SessionID] {
				windows[link.WindowID] = true
			}
		}

		for _, link := range links {
			if windows[link.WindowID] {
				wanted[link.SessionID] = true
			}
		}

		if len(wanted) == before {
			return wanted, nil
		}
	}
}
