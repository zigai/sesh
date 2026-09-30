package capture

import (
	"errors"
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

func graph(snapshot gotmux.Snapshot, selected []gotmux.SessionID) (model.Manifest, error) {
	var m model.Manifest
	if snapshot.Consistency != gotmux.ConsistencyComplete {
		return m, ErrChanged
	}

	wanted, err := closure(snapshot, selected)
	if err != nil {
		return m, err
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

func graphWindows(snapshot gotmux.Snapshot, wanted map[gotmux.SessionID]bool, m model.Manifest) (model.Manifest, error) {
	windows := make(map[gotmux.WindowID]bool)

	for _, info := range snapshot.Links() {
		if !wanted[info.SessionID] {
			continue
		}

		windows[info.WindowID] = true
		m.Links = append(m.Links, model.Link{Session: ref("s", string(info.SessionID)), Window: ref("w", string(info.WindowID)), Index: info.Index, Active: info.Active, Last: strings.Contains(info.Flags, "-"), Flags: model.Bytes(info.Flags)})
	}

	panes := make(map[string]string)

	for _, info := range snapshot.Panes() {
		if !windows[info.WindowID] {
			continue
		}

		panes[string(info.ID)] = ref("p", string(info.ID))
		m.Panes = append(m.Panes, paneObservation(info))
	}

	for _, info := range snapshot.Windows() {
		if !windows[info.ID] {
			continue
		}

		layout, err := integration.DecodeLayout(info.Layout, panes)
		if err != nil {
			return m, oops.In("snapshot.capture").
				Code("capture_unsupported").
				With("operation", "decode_layout", "domain", "S03", "reason", "layout_decode").
				Wrap(ErrUnsupported)
		}

		m.Windows = append(m.Windows, model.Window{ID: ref("w", string(info.ID)), Name: model.Bytes(info.Name), Width: info.Width, Height: info.Height, Layout: layout, Zoomed: info.Zoomed, Options: nil})
	}

	return m, nil
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
			return nil, ErrSelection
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
