//go:build linux && integration

package picker

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/tui"
)

func TestPickerInitialLoadMatchesSubprocessReadsAndReleasesControlClient(t *testing.T) {
	server := tmuxtest.NewServer(t)
	ctx := t.Context()

	origin := pickerOriginSession(t, server)
	attachPickerTestClient(t, origin.Handle())

	if _, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "other"}); err != nil {
		t.Fatal(err)
	}

	originPane := pickerFirstPane(t, origin.Handle())

	initial := newPickerInitialReadsServices(t, server, string(origin.ID), originPane)
	initial.startInitialReads(ctx)

	initialCatalog, err := initial.LoadCatalog(ctx)
	if err != nil {
		t.Fatalf("initial catalog: %v", err)
	}

	initialPanes, err := initial.TmuxPaneLocations(ctx)
	if err != nil {
		t.Fatalf("initial panes: %v", err)
	}

	if got := pickerControlClientCount(t, server); got != 1 {
		t.Fatalf("control clients during initial load = %d, want 1", got)
	}

	initial.FinishInitialLoad()

	direct := newPickerInitialReadsServices(t, server, string(origin.ID), originPane)

	directCatalog, err := direct.LoadCatalog(ctx)
	if err != nil {
		t.Fatalf("subprocess catalog: %v", err)
	}

	directPanes, err := direct.TmuxPaneLocations(ctx)
	if err != nil {
		t.Fatalf("subprocess panes: %v", err)
	}

	assertPickerCatalogsEqual(t, initialCatalog, directCatalog)

	if !reflect.DeepEqual(initialPanes, directPanes) {
		t.Fatalf("panes over control = %+v, over subprocess = %+v", initialPanes, directPanes)
	}

	laterCatalog, err := initial.LoadCatalog(ctx)
	if err != nil {
		t.Fatalf("catalog after initial load: %v", err)
	}

	assertPickerCatalogsEqual(t, laterCatalog, directCatalog)

	if err := initial.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if got := pickerControlClientCount(t, server); got != 0 {
		t.Fatalf("control clients after close = %d, want 0", got)
	}
}

func TestPickerInitialLoadFailsWhenInvokingSessionIsGone(t *testing.T) {
	server := tmuxtest.NewServer(t)
	ctx := t.Context()

	origin := pickerOriginSession(t, server)

	gone, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "gone"})
	if err != nil {
		t.Fatal(err)
	}

	if err := gone.Kill(ctx); err != nil {
		t.Fatal(err)
	}

	services := newPickerInitialReadsServices(t, server, string(gone.ID()), pickerFirstPane(t, origin.Handle()))
	services.startInitialReads(ctx)

	if _, err := services.LoadCatalog(ctx); err == nil {
		t.Fatal("initial catalog load succeeded without its control connection")
	}

	if err := services.Close(); err != nil {
		t.Fatalf("close after failed open: %v", err)
	}
}

func TestPickerCloseReleasesControlClientBeforeInitialLoadFinishes(t *testing.T) {
	server := tmuxtest.NewServer(t)
	ctx := t.Context()

	origin := pickerOriginSession(t, server)

	services := newPickerInitialReadsServices(t, server, string(origin.ID), pickerFirstPane(t, origin.Handle()))
	services.startInitialReads(ctx)

	if _, err := services.TmuxPaneLocations(ctx); err != nil {
		t.Fatal(err)
	}

	if err := services.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if got := pickerControlClientCount(t, server); got != 0 {
		t.Fatalf("control clients after close = %d, want 0", got)
	}
}

func TestPickerInitialReadsStopWaitingWhenCanceled(t *testing.T) {
	server := tmuxtest.NewServer(t)

	origin := pickerOriginSession(t, server)

	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	services := newPickerInitialReadsServices(t, server, string(origin.ID), pickerFirstPane(t, origin.Handle()))
	services.startInitialReads(canceled)

	if _, err := services.LoadCatalog(t.Context()); err == nil {
		t.Fatal("catalog load succeeded although the control connection was canceled")
	}

	if err := services.Close(); err != nil {
		t.Fatalf("close after canceled open: %v", err)
	}

	if got := pickerControlClientCount(t, server); got != 0 {
		t.Fatalf("control clients after canceled open = %d, want 0", got)
	}
}

func newPickerInitialReadsServices(t *testing.T, server *gotmux.Server, sessionID, pane string) *pickerServices {
	t.Helper()

	root := t.TempDir()

	services, err := newPickerServices(t.Context(), config.DefaultConfig(), server.Endpoint().SocketPath,
		"", filepath.Join(root, "leases"), filepath.Join(root, "history.json"),
		filepath.Join(root, "agents.json"), gotmux.Streams{}, RemoteServices{})
	if err != nil {
		t.Fatal(err)
	}

	services.currentSessionID = sessionID
	services.currentPane = pane

	t.Cleanup(func() { _ = services.Close() })

	return services
}

func pickerFirstPane(t *testing.T, target gotmux.Session) string {
	t.Helper()

	panes, err := target.Panes(t.Context())
	if err != nil || len(panes) == 0 {
		t.Fatalf("session panes: %v", err)
	}

	return string(panes[0].ID)
}

func pickerControlClientCount(t *testing.T, server *gotmux.Server) int {
	t.Helper()

	clients, err := server.Clients(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	count := 0

	for _, client := range clients {
		if client.Control {
			count++
		}
	}

	return count
}

func assertPickerCatalogsEqual(t *testing.T, got, want session.CatalogResult) {
	t.Helper()

	if got.LiveErr != nil || got.SavedErr != nil || want.LiveErr != nil || want.SavedErr != nil {
		t.Fatalf("catalog errors: got live=%v saved=%v, want live=%v saved=%v", got.LiveErr, got.SavedErr, want.LiveErr, want.SavedErr)
	}

	if got.Status != want.Status || !reflect.DeepEqual(got.Sessions, want.Sessions) {
		t.Fatalf("catalog = %+v, want %+v", got, want)
	}
}

var _ tui.InitialLoadFinisher = (*pickerServices)(nil)
