//go:build (linux || darwin) && integration

package picker

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/app"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func TestPickerCatalogKeepsLiveRowsWhenRepositoryCannotBeOpened(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")

	server := tmuxtest.NewServer(t)
	ctx := t.Context()

	if _, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "live-work"}); err != nil {
		t.Fatalf("create fixture session: %v", err)
	}

	dirs := t.TempDir()

	repository := filepath.Join(dirs, "repository")
	if err := os.WriteFile(repository, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	services, err := newServices(
		ctx,
		resolvePickerTestWorkspace(t, config.DefaultConfig(), app.Locations{
			Socket:     server.Endpoint().SocketPath,
			Repository: repository,
			LeaseRoot:  filepath.Join(dirs, "leases"),
			History: filepath.Join(
				dirs,
				"history.json",
			),
			FinalSaveStatus: "",
			AgentStore:      filepath.Join(dirs, "agents.json"),
		}),
		gotmux.Streams{},
		RemoteServices{},
	)
	if err != nil {
		t.Fatalf("new picker services: %v", err)
	}

	t.Cleanup(func() {
		if err := services.Close(); err != nil {
			t.Errorf("close picker services: %v", err)
		}
	})

	catalog, err := services.LoadCatalog(ctx)
	if err == nil || catalog.Status != session.StatusPartial || catalog.SavedErr == nil {
		t.Fatalf(
			"catalog status = %q, saved error = %v, error = %v; want a partial catalog that reports the repository",
			catalog.Status,
			catalog.SavedErr,
			err,
		)
	}

	if !slices.ContainsFunc(
		catalog.Sessions,
		func(row session.Session) bool { return row.Name == "live-work" },
	) {
		t.Fatalf("live session missing from partial catalog: %+v", catalog.Sessions)
	}
}

func TestPickerServiceRejectsReplacementSessionBeforeMutation(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")

	server := tmuxtest.NewServer(t)
	ctx := t.Context()

	original, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "picker-target"})
	if err != nil {
		t.Fatalf("create fixture session: %v", err)
	}

	dirs := t.TempDir()
	cfg := config.DefaultConfig()

	services, err := newServices(ctx, resolvePickerTestWorkspace(t, cfg, app.Locations{
		Socket:     server.Endpoint().SocketPath,
		Repository: filepath.Join(dirs, "repository"),
		LeaseRoot:  filepath.Join(dirs, "leases"),
		History: filepath.Join(
			dirs,
			"history.json",
		),
		FinalSaveStatus: "",
		AgentStore:      filepath.Join(dirs, "agents.json"),
	}), gotmux.Streams{}, RemoteServices{})
	if err != nil {
		t.Fatalf("new picker services: %v", err)
	}

	t.Cleanup(func() {
		if err := services.Close(); err != nil {
			t.Errorf("close picker services: %v", err)
		}
	})

	catalog, err := services.LoadCatalog(ctx)
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}

	var selected session.Session

	for _, row := range catalog.Sessions {
		if row.ID == string(original.ID()) {
			selected = row
			break
		}
	}

	if selected.ID == "" {
		t.Fatal("fixture session missing from picker catalog")
	}

	if err := original.Kill(ctx); err != nil {
		t.Fatalf("kill original: %v", err)
	}

	replacement, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "picker-target"})
	if err != nil {
		t.Fatalf("create replacement: %v", err)
	}

	_, err = action.Run(ctx, services.actions, action.SessionKill, action.Local(),
		action.Caller{Client: "", Pane: "", Session: "", Width: "", Height: ""},
		action.KillSession{Session: action.RefOf(selected), Force: false})
	if action.CodeOf(err) != action.CodeTargetChanged {
		t.Fatalf("kill stale target error = %v, want target changed", err)
	}

	if _, err := replacement.Info(ctx); err != nil {
		t.Fatalf("replacement session was mutated: %v", err)
	}
}
