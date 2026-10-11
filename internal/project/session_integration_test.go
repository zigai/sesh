//go:build integration

package project_test

import (
	"errors"
	"path/filepath"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/project"
)

func TestOpenCreatesATaggedSessionWithAMainWindowInTheRoot(t *testing.T) {
	server := tmuxtest.NewServer(t)
	p := gitProjectAt(t, filepath.Join(realTempDir(t), "shop"))
	before := len(sessions(t, server))

	opened, err := project.Open(t.Context(), server, p, false)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if !opened.Created || opened.Adopted || opened.Session.Name != "shop" {
		t.Fatalf("Open() = %+v, want a created session named shop", opened)
	}

	assertProjectSession(t, server, opened.Session.ID, p.Root)

	again, err := project.Open(t.Context(), server, p, false)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}

	if again.Created || again.Session.ID != opened.Session.ID || len(sessions(t, server)) != before+1 {
		t.Fatalf("second Open() = %+v, want the existing session", again)
	}
}

func TestOpenLeavesASessionWithTheProjectNameAloneUnlessAdopting(t *testing.T) {
	server := tmuxtest.NewServer(t)
	base := realTempDir(t)
	p := gitProjectAt(t, filepath.Join(base, "shop"))
	existing := newPlainSession(t, server, "shop", base)
	before := len(sessions(t, server))

	if _, err := project.Open(t.Context(), server, p, false); !errors.Is(err, project.ErrNameTaken) {
		t.Fatalf("Open() error = %v, want ErrNameTaken", err)
	}

	if root, err := project.RootOfSession(t.Context(), server, existing.ID()); err != nil || root != "" {
		t.Fatalf("session was tagged with %q (err %v) without adopting", root, err)
	}

	if count := windowCount(t, existing); count != 1 || len(sessions(t, server)) != before {
		t.Fatalf("refused Open() changed tmux: %d windows, %d sessions", count, len(sessions(t, server)))
	}

	opened, err := project.Open(t.Context(), server, p, true)
	if err != nil {
		t.Fatalf("adopting Open() error = %v", err)
	}

	if !opened.Adopted || opened.Session.ID != existing.ID() || windowCount(t, existing) != 2 {
		t.Fatalf("adopting Open() = %+v, want the existing session with its window kept and main added", opened)
	}

	assertProjectSession(t, server, existing.ID(), p.Root)
}

func TestOpenSeparatesProjectsThatShareAName(t *testing.T) {
	server := tmuxtest.NewServer(t)
	base := realTempDir(t)
	first := gitProjectAt(t, filepath.Join(base, "work", "api.v2"))
	second := gitProjectAt(t, filepath.Join(base, "personal", "api.v2"))

	openedFirst, err := project.Open(t.Context(), server, first, false)
	if err != nil {
		t.Fatal(err)
	}

	openedSecond, err := project.Open(t.Context(), server, second, false)
	if err != nil {
		t.Fatal(err)
	}

	if openedFirst.Session.Name != "api_v2" || openedSecond.Session.Name != "api_v2-2" {
		t.Fatalf("session names = %q, %q; want api_v2 and api_v2-2", openedFirst.Session.Name, openedSecond.Session.Name)
	}

	assertProjectSession(t, server, openedSecond.Session.ID, second.Root)
}

func assertProjectSession(t *testing.T, server *gotmux.Server, id gotmux.SessionID, root string) {
	t.Helper()

	if tagged, err := project.RootOfSession(t.Context(), server, id); err != nil || tagged != root {
		t.Fatalf("session is tagged with %q (err %v), want %q", tagged, err, root)
	}

	session, err := server.Session(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}

	windows, err := session.Windows(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, link := range windows {
		info, err := link.Window().InfoWith(t.Context(), gotmux.QueryOptions{Filter: "", ExtraFields: []string{project.OptionMain}})
		if err != nil {
			t.Fatal(err)
		}

		if value, _ := info.Raw(project.OptionMain); string(value) != "" {
			panes, err := link.Window().Panes(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			if info.Name != project.MainWindowName || panes[0].CurrentPath != root {
				t.Fatalf("main window %q runs in %q, want main in %q", info.Name, panes[0].CurrentPath, root)
			}

			return
		}
	}

	t.Fatal("session has no main window")
}

func gitProjectAt(t *testing.T, dir string) project.Project {
	t.Helper()

	initRepository(t, dir)

	return project.Project{Root: dir, Name: filepath.Base(dir), Kind: project.KindGit}
}

func newPlainSession(t *testing.T, server *gotmux.Server, name, dir string) gotmux.Session {
	t.Helper()

	session, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name: name, Dir: dir, Window: "", Program: gotmux.Program{}, Env: nil, TmuxEnv: nil,
		Size: gotmux.Size{Width: 0, Height: 0}, Start: gotmux.StartPolicyExistingOnly, Group: "",
	})
	if err != nil {
		t.Fatal(err)
	}

	return session
}

func sessions(t *testing.T, server *gotmux.Server) []project.LiveSession {
	t.Helper()

	live, err := project.LiveSessions(t.Context(), server)
	if err != nil {
		t.Fatal(err)
	}

	return live
}

func windowCount(t *testing.T, session gotmux.Session) int {
	t.Helper()

	windows, err := session.Windows(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	return len(windows)
}
