package project

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"
)

const (
	OptionProject = "@sesh_project"
	OptionMain    = "@sesh_main"

	MainWindowName = "main"
	maxNameSuffix  = 99
)

const (
	OpenedExisting Opening = iota
	OpenedCreated
	OpenedAdopted
)

var (
	ErrNameTaken       = errors.New("a session without a project already has this project's name")
	ErrNotAdoptable    = errors.New("session to adopt is already a project session")
	ErrNoFreeName      = errors.New("no free session name for project")
	ErrSessionNotFound = errors.New("session not found")
)

type Opening int

type Opened struct {
	Session gotmux.SessionInfo
	How     Opening
}

type LiveSession struct {
	Name string
	Root string
}

func SessionOf(ctx context.Context, server *gotmux.Server, root string) (gotmux.SessionInfo, bool, error) {
	sessions, err := taggedSessions(ctx, server)
	if err != nil {
		return emptySessionInfo(), false, err
	}

	for _, info := range sessions {
		if tag(info) == root {
			return info, true, nil
		}
	}

	return emptySessionInfo(), false, nil
}

func RootOfSession(ctx context.Context, server *gotmux.Server, id gotmux.SessionID) (string, error) {
	sessions, err := taggedSessions(ctx, server)
	if err != nil {
		return "", err
	}

	for _, info := range sessions {
		if info.ID == id {
			return tag(info), nil
		}
	}

	return "", ErrSessionNotFound
}

func NameConflict(ctx context.Context, server *gotmux.Server, p Project) (gotmux.SessionInfo, bool, error) {
	sessions, err := taggedSessions(ctx, server)
	if err != nil {
		return emptySessionInfo(), false, err
	}

	for _, info := range sessions {
		if info.Name == SessionName(p) && tag(info) == "" {
			return info, true, nil
		}
	}

	return emptySessionInfo(), false, nil
}

// CreateSession adds a numeric suffix when another project's session holds the name. An untagged
// session with the name is never renamed or replaced; the caller gets ErrNameTaken.
func CreateSession(ctx context.Context, server *gotmux.Server, p Project) (gotmux.Session, error) {
	sessions, err := taggedSessions(ctx, server)
	if err != nil {
		return gotmux.Session{}, err
	}

	name, err := freeName(sessions, p)
	if err != nil {
		return gotmux.Session{}, sessionError("create_project_session", err)
	}

	created, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name: name, Dir: p.Root, Window: MainWindowName, Program: gotmux.Program{}, Env: nil, TmuxEnv: nil,
		Size: gotmux.Size{Width: 0, Height: 0}, Start: gotmux.StartPolicyExistingOnly, Group: "",
	})
	if err != nil {
		return gotmux.Session{}, sessionError("create_project_session", err)
	}

	if err := created.Options().SetUser(ctx, OptionProject, p.Root); err != nil {
		return created, sessionError("tag_project_session", err)
	}

	windows, err := created.Windows(ctx)
	if err != nil || len(windows) == 0 {
		return created, sessionError("tag_main_window", errors.Join(err, ErrSessionNotFound))
	}

	return created, tagMainWindow(ctx, windows[0].Window())
}

func AdoptSession(ctx context.Context, server *gotmux.Server, id gotmux.SessionID, p Project) (gotmux.Session, error) {
	current, err := RootOfSession(ctx, server, id)
	if err != nil {
		return gotmux.Session{}, sessionError("adopt_project_session", err)
	}

	if current != "" {
		return gotmux.Session{}, sessionError("adopt_project_session", ErrNotAdoptable)
	}

	adopted, err := server.Session(ctx, id)
	if err != nil {
		return gotmux.Session{}, sessionError("adopt_project_session", err)
	}

	link, err := adopted.NewWindow(ctx, gotmux.NewWindowOptions{
		Name: MainWindowName, Dir: p.Root, Program: gotmux.Program{}, Env: nil, TmuxEnv: nil,
		Index: nil, Before: false, After: false, Select: false,
	})
	if err != nil {
		return adopted, sessionError("adopt_project_session", err)
	}

	if err := tagMainWindow(ctx, link.Window()); err != nil {
		return adopted, err
	}

	if err := adopted.Options().SetUser(ctx, OptionProject, p.Root); err != nil {
		return adopted, sessionError("tag_project_session", err)
	}

	return adopted, nil
}

func SessionName(p Project) string {
	return strings.NewReplacer(".", "_", ":", "_").Replace(p.Name)
}

func Open(ctx context.Context, server *gotmux.Server, p Project, adopt bool) (Opened, error) {
	existing, found, err := SessionOf(ctx, server, p.Root)
	if err != nil {
		return Opened{}, err
	}

	if found {
		return Opened{Session: existing, How: OpenedExisting}, nil
	}

	open, how := CreateSession, OpenedCreated
	if adopt {
		open, how = adoptNamesake, OpenedAdopted
	}

	opened, err := open(ctx, server, p)
	if err != nil {
		return Opened{}, err
	}

	info, err := opened.Info(ctx)
	if err != nil {
		return Opened{}, sessionError("inspect_project_session", err)
	}

	return Opened{Session: info, How: how}, nil
}

func LiveSessions(ctx context.Context, server *gotmux.Server) ([]LiveSession, error) {
	sessions, err := taggedSessions(ctx, server)
	if err != nil {
		return nil, err
	}

	live := make([]LiveSession, 0, len(sessions))
	for _, info := range sessions {
		live = append(live, LiveSession{Name: info.Name, Root: tag(info)})
	}

	return live, nil
}

func freeName(sessions []gotmux.SessionInfo, p Project) (string, error) {
	base := SessionName(p)
	taken := make(map[string]string, len(sessions))

	for _, info := range sessions {
		taken[info.Name] = tag(info)
	}

	owner, used := taken[base]
	if !used {
		return base, nil
	}

	if owner == "" {
		return "", ErrNameTaken
	}

	for suffix := 2; suffix <= maxNameSuffix; suffix++ {
		candidate := base + "-" + strconv.Itoa(suffix)
		if _, used := taken[candidate]; !used {
			return candidate, nil
		}
	}

	return "", ErrNoFreeName
}

func tagMainWindow(ctx context.Context, window gotmux.Window) error {
	if err := window.Options().SetUser(ctx, OptionMain, "1"); err != nil {
		return sessionError("tag_main_window", err)
	}

	return nil
}

func taggedSessions(ctx context.Context, server *gotmux.Server) ([]gotmux.SessionInfo, error) {
	sessions, err := server.SessionsWith(ctx, gotmux.QueryOptions{Filter: "", ExtraFields: []string{OptionProject}})
	if err != nil {
		return nil, sessionError("list_project_sessions", err)
	}

	return sessions, nil
}

func tag(info gotmux.SessionInfo) string {
	value, _ := info.Raw(OptionProject)

	return string(value)
}

func sessionError(operation string, err error) error {
	return oops.In("project").Code("project_session_failed").With("operation", operation).Wrap(err)
}

func adoptNamesake(ctx context.Context, server *gotmux.Server, p Project) (gotmux.Session, error) {
	conflict, conflicting, err := NameConflict(ctx, server, p)
	if err != nil {
		return gotmux.Session{}, err
	}

	if !conflicting {
		return gotmux.Session{}, sessionError("adopt_project_session", ErrSessionNotFound)
	}

	return AdoptSession(ctx, server, conflict.ID, p)
}

func emptySessionInfo() gotmux.SessionInfo {
	var info gotmux.SessionInfo

	return info
}
