package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func loaderTestServices(deps Dependencies) Services {
	services := testServices(deps)

	adapter, ok := services.Catalog.(*testServiceAdapter)
	if !ok {
		panic("loader test services catalog source is not a test service adapter")
	}

	services.Remote = adapter
	services.RemoteWatcher = adapter

	return services
}

func loaderTestModel(t *testing.T, cfg *config.Config, deps Dependencies) *Model {
	t.Helper()

	model, err := newModelWithContext(context.Background(), cfg, testState(), loaderTestServices(deps))
	if err != nil {
		t.Fatalf("newModelWithContext() error = %v", err)
	}

	t.Cleanup(model.Close)

	return model
}

func TestHandleRefreshPreservesSessionsOnError(t *testing.T) {
	model := newTestModel()
	model.sessions = []session.Session{{Name: testSupportSessionDevName}}
	model.filtered = append([]session.Session(nil), model.sessions...)

	dependenciesForTest(model).LoadCatalog = func(context.Context) (session.CatalogResult, error) {
		return session.CatalogResult{}, errTmuxUnavailable
	}

	model.handleRefresh()

	if len(model.sessions) != 1 || model.sessions[0].Name != testSupportSessionDevName {
		t.Fatalf("sessions were not preserved on refresh error: %#v", model.sessions)
	}

	if model.statusLevel != statusError {
		t.Fatalf("statusLevel = %v, want statusError", model.statusLevel)
	}

	if !strings.Contains(model.statusMessage, "tmux unavailable") {
		t.Fatalf("statusMessage = %q, want refresh error", model.statusMessage)
	}
}

func TestLoadSessionRowsLoadsSourcesConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})

	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()

	deps := Dependencies{
		LoadCatalog: func(context.Context) (session.CatalogResult, error) {
			started <- "catalog"

			<-release

			return session.CatalogResult{
				Sessions: []session.Session{{ID: "$1", Name: "live", Kind: session.KindLive}},
				Status:   session.StatusComplete,
			}, nil
		},
		LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) {
			started <- "agents"

			<-release

			return []session.AgentSummary{{TmuxSessionID: "$1", Total: 1, Active: 1}}, nil
		},
	}

	result := make(chan sessionLoadResultMsg, 1)
	go func() {
		result <- loadSessionRows(context.Background(), config.DefaultConfig(), testServices(deps))
	}()

	seen := make(map[string]bool, 2)

	timer := time.NewTimer(time.Second)
	defer timer.Stop()

	for len(seen) < 2 {
		select {
		case source := <-started:
			seen[source] = true
		case <-timer.C:
			close(release)

			released = true

			<-result

			t.Fatalf("loader sources = %#v, want catalog and agents started before either completes", seen)
		}
	}

	close(release)

	released = true

	loaded := <-result
	if loaded.catalogErr != nil || loaded.agentSummaryErr != nil {
		t.Fatalf("loadSessionRows() errors = %v/%v", loaded.catalogErr, loaded.agentSummaryErr)
	}

	if loaded.status != session.StatusComplete {
		t.Fatalf("loadSessionRows() status = %q, want complete", loaded.status)
	}

	if len(loaded.sessions) != 1 || loaded.sessions[0].AgentTotal != 1 {
		t.Fatalf("loadSessionRows() sessions = %#v, want catalog row with merged agent summary", loaded.sessions)
	}
}

func TestLoadSessionRowsSortsLocalCatalogByNativeIdentityHistoryAndActivity(t *testing.T) {
	now := time.Date(2026, time.September, 21, 12, 0, 0, 0, time.UTC)
	rows := []session.Session{
		{ID: "$1", Name: "older", Kind: session.KindLive, Activity: now.Add(-2 * time.Hour)},
		{ID: "$2", Name: "active", Kind: session.KindLive, Activity: now.Add(-time.Minute)},
		{ID: "$3", Name: "visited", Kind: session.KindLive, Activity: now.Add(-3 * time.Hour)},
	}
	state := &session.State{SwitchHistory: []session.SwitchHistoryEntry{{
		Session:    rows[2].SemanticIdentity(),
		LastSwitch: now.Unix(),
	}}}
	model := newTestModelWithDeps(Dependencies{LoadCatalog: func(context.Context) (session.CatalogResult, error) {
		return session.CatalogResult{Sessions: rows, Status: session.StatusComplete}, nil
	}})
	t.Cleanup(model.Close)
	model.state = state

	msg, ok := model.refreshSessions()().(sessionLoadResultMsg)
	if !ok {
		t.Fatal("refreshSessions() did not return a session load result")
	}

	model.handleSessionLoadResult(msg)

	if got, want := sessionNamesForTest(model.sessions), []string{"visited", "active", "older"}; !slices.Equal(got, want) {
		t.Fatalf("loaded session order = %v, want %v", got, want)
	}
}

func TestLoadSessionsKeepsLiveRowsWhenSavedCatalogFails(t *testing.T) {
	model := newTestModelWithDeps(Dependencies{
		LoadCatalog: func(context.Context) (session.CatalogResult, error) {
			return session.CatalogResult{
				Sessions: []session.Session{{ID: "$1", Name: "live", Kind: session.KindLive}},
				Status:   session.StatusPartial,
			}, errTmuxUnavailable
		},
	})
	t.Cleanup(model.Close)

	if err := model.loadSessions(); err != nil {
		t.Fatalf("loadSessions() with a partial catalog = %v, want the live rows applied", err)
	}

	if got := sessionNamesForTest(model.sessions); !slices.Equal(got, []string{"live"}) {
		t.Fatalf("sessions after partial reload = %v, want [live]", got)
	}

	if !errors.Is(model.sessionLoadErr, errTmuxUnavailable) {
		t.Fatalf("sessionLoadErr = %v, want the retained partial cause", model.sessionLoadErr)
	}
}

func TestLoadSessionRowsExposesPartialCatalogStatusAndKeepsRows(t *testing.T) {
	model := newTestModelWithDeps(Dependencies{
		LoadCatalog: func(context.Context) (session.CatalogResult, error) {
			return session.CatalogResult{
				Sessions: []session.Session{{ID: "$1", Name: "live", Kind: session.KindLive}},
				Status:   session.StatusPartial,
			}, errTmuxUnavailable
		},
	})

	loaded := loadSessionRows(context.Background(), model.config, model.deps)
	assertPartialLoadState(t, model, loaded)

	model.handleSessionLoadResult(sessionLoadResultMsg{
		sessions: []session.Session{{ID: "$1", Name: "live", Kind: session.KindLive}},
		status:   session.StatusComplete,
	})

	if _, ok := model.degradedSources[degradedSourceCatalog]; ok {
		t.Fatalf("degraded sources after recovery = %#v, want catalog cleared", model.degradedSources)
	}

	if model.statusLevel != statusNone || model.statusMessage != "" {
		t.Fatalf(
			"recovered catalog status = (%v, %q), want no status",
			model.statusLevel,
			model.statusMessage,
		)
	}
}

func TestDegradedObservationSourcesKeepRowsAndReportStatus(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Remote.Enabled = true
	cfg.Remote.Connections = []config.RemoteConnectionConfig{{ID: testSupportSessionWorkName, Host: testSupportSessionWorkName}}

	model := loaderTestModel(t, cfg, Dependencies{
		LoadCatalog: func(context.Context) (session.CatalogResult, error) {
			return session.CatalogResult{
				Sessions: []session.Session{{ID: "$1", Name: testSupportSessionDevName, Kind: session.KindLive}},
				Status:   session.StatusComplete,
			}, nil
		},
		LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) {
			return nil, errSaveFailed
		},
		LoadRemoteCatalog: func() (RemoteCatalog, error) {
			return RemoteCatalog{}, errSaveFailed
		},
	})

	model.handleSessionLoadResult(loadSessionRows(context.Background(), cfg, model.deps))

	if len(model.sessions) != 1 || model.sessions[0].Name != testSupportSessionDevName {
		t.Fatalf("sessions with degraded sources = %#v, want the catalog row", model.sessions)
	}

	if model.statusLevel != statusDegraded {
		t.Fatalf("status level = %v, want degraded", model.statusLevel)
	}

	for _, fragment := range []string{"Remote sessions unavailable", "Agent status unavailable"} {
		if !strings.Contains(model.statusMessage, fragment) {
			t.Fatalf("degraded status = %q, want it to report %q", model.statusMessage, fragment)
		}
	}

	if model.degradedSources[degradedSourceRemote] == "" || model.degradedSources[degradedSourceAgents] == "" {
		t.Fatalf("degraded sources = %#v, want remote and agents recorded", model.degradedSources)
	}

	model.handleSessionLoadResult(sessionLoadResultMsg{
		sessions: []session.Session{{ID: "$1", Name: testSupportSessionDevName, Kind: session.KindLive}},
		status:   session.StatusComplete,
	})

	if len(model.degradedSources) != 0 {
		t.Fatalf("degraded sources after healthy observation = %#v, want none", model.degradedSources)
	}

	if model.statusLevel != statusNone || model.statusMessage != "" {
		t.Fatalf(
			"status after healthy observation = (%v, %q), want no status",
			model.statusLevel,
			model.statusMessage,
		)
	}
}

func TestMergeRemoteSessionRowsKeepsLocalSavedNameCollision(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Remote.Enabled = true
	cfg.Remote.SSHPath = "ssh"
	cfg.Remote.Connections = []config.RemoteConnectionConfig{{
		ID:   testSupportSessionWorkName,
		Host: "work.example.com",
	}}

	serverStartedAt := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	sessionCreatedAt := serverStartedAt.Add(time.Minute)
	lastSeen := serverStartedAt.Add(2 * time.Minute)
	rows := mergeRemoteSessionRows(
		[]session.Session{{Name: testSupportSessionDevName, Kind: session.KindSaved}},
		RemoteCatalog{Connections: []RemoteConnection{{
			ID:             testSupportSessionWorkName,
			Label:          "Work",
			ConfigDigest:   "digest-work",
			PeerID:         "peer-1",
			StreamInstance: "stream-1",
			Connected:      false,
			LastSeen:       lastSeen,
			LastError:      RemoteErrorStale,
			Sessions: []session.Session{{
				ID:               "$1",
				Name:             testSupportSessionDevName,
				TmuxServerSocket: "/tmp/tmux/default",
				RuntimeIdentity: session.RuntimeIdentity{
					TmuxServerPID:        101,
					TmuxServerStartedAt:  serverStartedAt,
					TmuxSessionCreatedAt: sessionCreatedAt,
				},
			}},
		}}},
		cfg,
		testState(),
		lastSeen.Add(time.Minute),
	)

	if len(rows) != 2 {
		t.Fatalf("rows = %#v, want local saved and remote live rows", rows)
	}

	var local, remoteRow session.Session

	for _, row := range rows {
		if row.Origin == session.OriginSSH {
			remoteRow = row

			continue
		}

		local = row
	}

	if local.Name != testSupportSessionDevName || local.Kind != session.KindSaved {
		t.Fatalf("local row = %#v, want saved dev", local)
	}

	if remoteRow.Name != testSupportSessionDevName || remoteRow.RemoteStatus != session.RemoteStatusStale {
		t.Fatalf("remote row = %#v, want stale remote dev", remoteRow)
	}

	wantIdentity := session.RuntimeIdentity{
		ConnectionID:         testSupportSessionWorkName,
		ConnectionLabel:      "Work",
		ConfigDigest:         "digest-work",
		PeerID:               "peer-1",
		StreamInstance:       "stream-1",
		TmuxServerSocket:     "/tmp/tmux/default",
		TmuxServerPID:        101,
		TmuxServerStartedAt:  serverStartedAt,
		TmuxSessionCreatedAt: sessionCreatedAt,
	}
	if remoteRow.RuntimeIdentity != wantIdentity {
		t.Fatalf("remote runtime identity = %#v, want %#v", remoteRow.RuntimeIdentity, wantIdentity)
	}
}

func TestMergeRemoteSessionRowsAppendsOnlyConfiguredConnections(t *testing.T) {
	now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	local := []session.Session{{ID: "$local", Name: "local", Kind: session.KindLive}}
	catalog := RemoteCatalog{Connections: []RemoteConnection{
		{
			ID: testSupportSessionWorkName, Connected: true, LastSeen: now,
			Sessions: []session.Session{{ID: "$1", Name: "work-dev", Kind: session.KindLive}},
		},
		{
			ID: "retired", Connected: true, LastSeen: now,
			Sessions: []session.Session{{ID: "$2", Name: "retired-dev", Kind: session.KindLive}},
		},
	}}

	cfg := config.DefaultConfig()
	cfg.Remote.Enabled = true
	cfg.Remote.Connections = []config.RemoteConnectionConfig{{ID: testSupportSessionWorkName, Host: testSupportSessionWorkName}}

	rows := mergeRemoteSessionRows(local, catalog, cfg, testState(), now)

	names := sessionNamesForTest(rows)
	if len(rows) != 2 || !slices.Contains(names, "local") || !slices.Contains(names, "work-dev") {
		t.Fatalf("merged rows = %#v, want local and configured remote rows", names)
	}

	if slices.Contains(names, "retired-dev") {
		t.Fatalf("merged rows = %#v, want unconfigured connections skipped", names)
	}

	for _, row := range rows {
		if row.Name != "work-dev" {
			continue
		}

		if row.Origin != session.OriginSSH || row.RemoteStatus != session.RemoteStatusAvailable {
			t.Fatalf("remote row = %#v, want available cached remote row", row)
		}
	}

	cfg.Remote.Enabled = false

	if got := mergeRemoteSessionRows(local, catalog, cfg, testState(), now); len(got) != 1 || got[0].Name != "local" {
		t.Fatalf("disabled remote rows = %#v, want local rows only", sessionNamesForTest(got))
	}
}

func TestRemoteConnectionStatus(t *testing.T) {
	now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		connection RemoteConnection
		want       session.RemoteStatus
	}{
		{name: "connecting", connection: RemoteConnection{}, want: session.RemoteStatusConnecting},
		{
			name:       "unavailable",
			connection: RemoteConnection{LastError: "unavailable"},
			want:       session.RemoteStatusUnavailable,
		},
		{
			name:       "incompatible",
			connection: RemoteConnection{LastError: RemoteErrorIncompatibleProtocol},
			want:       session.RemoteStatusIncompatible,
		},
		{
			name:       "peer mismatch",
			connection: RemoteConnection{LastError: RemoteErrorPeerMismatch},
			want:       session.RemoteStatusIncompatible,
		},
		{
			name:       "stale error",
			connection: RemoteConnection{LastError: RemoteErrorStale},
			want:       session.RemoteStatusStale,
		},
		{
			name: "stale age",
			connection: RemoteConnection{
				Connected: true,
				LastSeen:  now.Add(-time.Minute),
			},
			want: session.RemoteStatusStale,
		},
		{
			name: "degraded tmux source",
			connection: RemoteConnection{
				Connected:  true,
				LastSeen:   now,
				HealthTmux: RemoteHealthDegraded,
			},
			want: session.RemoteStatusDegraded,
		},
		{
			name: "unknown agent source",
			connection: RemoteConnection{
				Connected:    true,
				LastSeen:     now,
				HealthAgents: RemoteHealthUnknown,
			},
			want: session.RemoteStatusDegraded,
		},
		{
			name: "available",
			connection: RemoteConnection{
				Connected: true,
				LastSeen:  now,
			},
			want: session.RemoteStatusAvailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := remoteConnectionStatus(tt.connection, "30s", now); got != tt.want {
				t.Fatalf("remoteConnectionStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWatchedCatalogRemovesRemoteRowsWhenConnectionsDisappear(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Remote.Enabled = true
	cfg.Remote.Connections = []config.RemoteConnectionConfig{{ID: testSupportSessionWorkName, Host: testSupportSessionWorkName}}

	model := loaderTestModel(t, cfg, Dependencies{})
	now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)

	model.handleRemoteCatalog(remoteCatalogMsg{catalog: RemoteCatalog{
		GeneratedAt: now,
		Connections: []RemoteConnection{{
			ID:        testSupportSessionWorkName,
			Connected: true,
			LastSeen:  now,
			Sessions:  []session.Session{{ID: "$1", Name: testSupportSessionDevName, Kind: session.KindLive}},
		}},
	}})

	if len(model.sessions) != 1 || model.sessions[0].Name != testSupportSessionDevName {
		t.Fatalf("sessions before catalog removal = %#v, want remote dev", model.sessions)
	}

	model.handleRemoteCatalog(remoteCatalogMsg{catalog: RemoteCatalog{GeneratedAt: now.Add(time.Second)}})

	if len(model.sessions) != 0 {
		t.Fatalf("sessions after catalog removal = %#v, want no remote rows", model.sessions)
	}
}

func TestRemoteStatusTickAgesRowsWithoutCatalogUpdatesAndPreservesSelection(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Remote.Enabled = true
	cfg.Remote.StaleAfter = "30s"
	cfg.Remote.Connections = []config.RemoteConnectionConfig{{ID: testSupportSessionWorkName, Host: testSupportSessionWorkName}}

	model := loaderTestModel(t, cfg, Dependencies{})
	now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)

	model.handleRemoteCatalog(remoteCatalogMsg{catalog: RemoteCatalog{
		GeneratedAt: now,
		Connections: []RemoteConnection{{
			ID:        testSupportSessionWorkName,
			Connected: true,
			LastSeen:  now,
			Sessions:  []session.Session{{ID: "$1", Name: testSupportSessionDevName, Kind: session.KindLive}},
		}},
	}})

	model.cursor = 0
	selected := model.selectedSessionIdentity()

	model.handleRemoteStatusTick(now.Add(31 * time.Second))

	if len(model.filtered) != 1 || model.filtered[0].RemoteStatus != session.RemoteStatusStale {
		t.Fatalf("remote rows after status tick = %#v, want stale dev", model.filtered)
	}

	if got := model.selectedSessionIdentity(); got != selected {
		t.Fatalf("selection after status tick = %#v, want %#v", got, selected)
	}
}

func TestRemoteCatalogHealthDoesNotOccupyPopupStatus(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Remote.Enabled = true
	cfg.Remote.Connections = []config.RemoteConnectionConfig{
		{ID: "thinkpad", Host: "thinkpad"},
		{ID: "vps.01", Host: "vps.01"},
	}

	model := loaderTestModel(t, cfg, Dependencies{})

	model.handleRemoteCatalog(remoteCatalogMsg{catalog: RemoteCatalog{
		Connections: []RemoteConnection{
			{ID: "thinkpad", LastError: "unavailable"},
			{ID: "vps.01"},
		},
	}})

	if model.statusLevel != statusNone || model.statusMessage != "" {
		t.Fatalf(
			"remote catalog popup status = (%d, %q), want no routine status",
			model.statusLevel,
			model.statusMessage,
		)
	}
}

func TestRemoteCatalogWatcherStopsWithModelLifecycle(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Remote.Enabled = true

	started := make(chan struct{})
	stopped := make(chan struct{})

	model := loaderTestModel(t, cfg, Dependencies{
		WatchRemoteCatalog: func(ctx context.Context, yield func(RemoteCatalog, error)) error {
			close(started)

			<-ctx.Done()
			close(stopped)

			return ctx.Err()
		},
	})

	command := model.startRemoteCatalogWatchCommand()
	if command == nil {
		t.Fatal("startRemoteCatalogWatchCommand() returned nil")
	}

	commandDone := make(chan struct{})

	go func() {
		_ = command()

		close(commandDone)
	}()

	<-started
	model.Close()
	<-stopped
	<-commandDone
}

func TestCurrentTmuxSessionIsHiddenInSeshAndVisibleInAgents(t *testing.T) {
	model := newTestModelWithDeps(Dependencies{
		LoadCatalog: func(context.Context) (session.CatalogResult, error) {
			return session.CatalogResult{
				Sessions: []session.Session{
					{ID: "$1", Name: "current", Kind: session.KindLive, IsCurrent: true, ActivePaneID: "%7"},
					{ID: "$2", Name: "other", Kind: session.KindLive},
				},
				Status: session.StatusComplete,
			}, nil
		},
		LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) {
			return []session.AgentSummary{{
				TmuxSessionID:   "$1",
				TmuxSessionName: "current",
				Total:           1,
				Active:          1,
				Agents: []session.AgentSession{{
					RegistryID: testSupportAgentRegistryID, Harness: "pi", HarnessSession: "current-agent", PaneID: "%7",
				}},
			}}, nil
		},
	})

	if err := model.loadSessions(); err != nil {
		t.Fatalf("loadSessions() returned error: %v", err)
	}

	if got := sessionNamesForTest(model.filtered); slices.Contains(got, "current") {
		t.Fatalf("SESH rows = %#v, should hide current session", got)
	}

	if !model.cycleDisplayMode() {
		t.Fatal("cycleDisplayMode() = false, want AGENTS mode")
	}

	if got := sessionNamesForTest(model.filtered); !slicesEqual(got, []string{"current", "other"}) {
		t.Fatalf("AGENTS rows = %#v, want current session and regular other session", got)
	}

	if got, want := model.selectedListRow(), (popupListRow{sessionIndex: 0, agentIndex: 0}); !got.equal(want) {
		t.Fatalf("AGENTS selection = %#v, want current session active agent %#v", got, want)
	}
}

func TestAgentsModeSelectsCurrentAgentWhenInventoryArrivesAfterSessions(t *testing.T) {
	model, err := newModel(defaultCompactTestConfig(), &session.State{PopupDisplayMode: "agents"})
	if err != nil {
		t.Fatal(err)
	}

	model.applySessionRows(sessionLoadResultMsg{
		sessions: []session.Session{
			{ID: "$1", Name: "current", Kind: session.KindLive, IsCurrent: true, ActivePaneID: "%8"},
			{ID: "$2", Name: "other", Kind: session.KindLive},
		},
		status: session.StatusComplete,
	})

	if got := model.selectedListRow(); got.sessionIndex != 0 || got.isAgent() {
		t.Fatalf("selection before inventory = %#v, want current session parent", got)
	}

	model.applyAgentSummaries([]session.AgentSummary{{
		TmuxSessionID: "$1", TmuxSessionName: "current", Total: 2,
		Agents: []session.AgentSession{
			{RegistryID: "first", PaneID: "%7"},
			{RegistryID: "active", PaneID: "%8"},
		},
	}})

	if got := model.selectedListRow(); got.sessionIndex != 0 || !got.isAgent() || model.filtered[got.sessionIndex].Agents[got.agentIndex].PaneID != "%8" {
		t.Fatalf("selection after inventory = %#v, want current pane %%8", got)
	}
}

func TestAgentsModeKeepsUserSelectionWhenInventoryArrives(t *testing.T) {
	model, err := newModel(defaultCompactTestConfig(), &session.State{PopupDisplayMode: "agents"})
	if err != nil {
		t.Fatal(err)
	}

	model.applySessionRows(sessionLoadResultMsg{
		sessions: []session.Session{
			{ID: "$1", Name: "current", Kind: session.KindLive, IsCurrent: true, ActivePaneID: "%8"},
			{ID: "$2", Name: "other", Kind: session.KindLive},
		},
		status: session.StatusComplete,
	})

	model.selectListRow(popupListRow{sessionIndex: 1, agentIndex: parentAgentIndex})

	model.applyAgentSummaries([]session.AgentSummary{{
		TmuxSessionID: "$1", TmuxSessionName: "current", Total: 1,
		Agents: []session.AgentSession{{RegistryID: "active", PaneID: "%8"}},
	}})

	if got := model.selectedListRow(); got.sessionIndex != 1 || got.isAgent() {
		t.Fatalf("selection after inventory = %#v, want user's other session", got)
	}
}

func TestAgentsModeSelectsCurrentAgentWhenOriginLoadsFirst(t *testing.T) {
	model, err := newModel(defaultCompactTestConfig(), &session.State{PopupDisplayMode: "agents"})
	if err != nil {
		t.Fatal(err)
	}

	model.loadingSessions = true
	model.handleOriginSessions(originSessionsMsg{
		rows: []session.Session{{ID: "$3", Name: "origin", Kind: session.KindLive, Origin: session.OriginBridge}},
	})
	model.applySessionRows(sessionLoadResultMsg{
		sessions: []session.Session{{
			ID: "$1", Name: "current", Kind: session.KindLive, IsCurrent: true, ActivePaneID: "%8",
			Agents: []session.AgentSession{{RegistryID: "active", PaneID: "%8"}},
		}},
		status: session.StatusComplete,
	})

	selected := model.selectedListRow()
	if !selected.isAgent() || !model.filtered[selected.sessionIndex].IsCurrent ||
		model.filtered[selected.sessionIndex].Agents[selected.agentIndex].PaneID != "%8" {
		t.Fatalf("selection after local catalog = %#v, want current agent", selected)
	}
}

func TestLoadSessionsPrunesAgentsWithVanishedPanes(t *testing.T) {
	model := newTestModelWithDeps(Dependencies{
		LoadCatalog: func(context.Context) (session.CatalogResult, error) {
			return session.CatalogResult{
				Sessions: []session.Session{{ID: "$1", Name: testSupportSessionDevName, Kind: session.KindLive}},
				Status:   session.StatusComplete,
			}, nil
		},
		LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) {
			return []session.AgentSummary{{
				TmuxSessionID:   "$1",
				TmuxSessionName: testSupportSessionDevName,
				Total:           2,
				Active:          2,
				Agents: []session.AgentSession{
					{Harness: "omp", PaneID: "%364", Status: session.AgentStatusRunning},
					{Harness: "pi", PaneID: "%1", Status: session.AgentStatusIdle},
				},
			}}, nil
		},
		TmuxPaneLocations: func(context.Context) ([]PaneLocation, error) {
			return []PaneLocation{{ServerSocket: "", SessionID: "$1", SessionName: testSupportSessionDevName, PaneID: "%364"}}, nil
		},
	})

	if err := model.loadSessions(); err != nil {
		t.Fatalf("loadSessions() returned error: %v", err)
	}

	row := model.sessions[0]
	if len(row.Agents) != 1 || row.Agents[0].PaneID != "%364" {
		t.Fatalf("kept agents = %#v, want only the live pane", row.Agents)
	}
}

func TestLoadSessionsKeepsAgentSummariesWhenPaneListingFails(t *testing.T) {
	model := newTestModelWithDeps(Dependencies{
		LoadCatalog: func(context.Context) (session.CatalogResult, error) {
			return session.CatalogResult{
				Sessions: []session.Session{{ID: "$1", Name: testSupportSessionDevName, Kind: session.KindLive}},
				Status:   session.StatusComplete,
			}, nil
		},
		LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) {
			return []session.AgentSummary{{
				TmuxSessionID: "$1",
				Total:         1,
				Agents:        []session.AgentSession{{Harness: "pi", PaneID: "%999"}},
			}}, nil
		},
		TmuxPaneLocations: func(context.Context) ([]PaneLocation, error) {
			return nil, errTmuxUnavailable
		},
	})

	if err := model.loadSessions(); err != nil {
		t.Fatalf("loadSessions() returned error: %v", err)
	}

	row := model.sessions[0]
	if row.AgentTotal != 1 || len(row.Agents) != 1 {
		t.Fatalf("dev agent row = total %d details %#v, want unpruned on pane listing failure", row.AgentTotal, row.Agents)
	}
}

func TestLoadSessionsMergesAgentSummariesIntoLiveRows(t *testing.T) {
	agents := []session.AgentSession{{
		Harness: testSupportHarnessCodex, HarnessSession: testSupportAgentRegistryID, Status: session.AgentStatusRunning,
	}}
	model := newTestModelWithDeps(Dependencies{
		LoadCatalog: func(context.Context) (session.CatalogResult, error) {
			return session.CatalogResult{
				Sessions: []session.Session{
					{ID: "$1", Name: testSupportSessionDevName, Kind: session.KindLive, WindowCount: 2},
					{ID: "$2", Name: "ops", Kind: session.KindLive, WindowCount: 1},
				},
				Status: session.StatusComplete,
			}, nil
		},
		LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) {
			return []session.AgentSummary{
				{TmuxSessionID: "$1", TmuxSessionName: testSupportSessionDevName, Total: 5, Active: 3, Agents: agents},
				{TmuxSessionName: "ops", Total: 1, Active: 1},
			}, nil
		},
	})

	if err := model.loadSessions(); err != nil {
		t.Fatalf("loadSessions() returned error: %v", err)
	}

	byName := map[string]session.Session{}
	for _, row := range model.sessions {
		byName[row.Name] = row
	}

	if got := byName[testSupportSessionDevName]; got.AgentActive != 3 || got.AgentTotal != 5 {
		t.Fatalf("dev agent counts = %d/%d, want 3/5", got.AgentActive, got.AgentTotal)
	}

	if got := byName[testSupportSessionDevName].Agents; len(got) != 1 || got[0].HarnessSession != testSupportAgentRegistryID {
		t.Fatalf("dev agent details = %#v, want mapped agent", got)
	}

	agents[0].HarnessSession = "mutated"
	if got := byName[testSupportSessionDevName].Agents[0].HarnessSession; got != testSupportAgentRegistryID {
		t.Fatalf("dev agent details alias summary input: %q", got)
	}

	if got := byName["ops"]; got.AgentActive != 1 || got.AgentTotal != 1 {
		t.Fatalf("ops agent counts = %d/%d, want 1/1", got.AgentActive, got.AgentTotal)
	}
}

func TestAgentSortByStableName(t *testing.T) {
	now := time.Now()
	row := session.Session{ID: "$1", Name: testSupportSessionDevName}
	summary := session.AgentSummary{
		TmuxSessionID:   "$1",
		TmuxSessionName: testSupportSessionDevName,
		Total:           9,
		Agents: []session.AgentSession{
			{HarnessSession: "idle-old", Status: session.AgentStatusIdle, StatusSince: now.Add(-time.Hour)},
			{HarnessSession: "unknown", Status: session.AgentStatusUnknown, StatusSince: now},
			{HarnessSession: "waiting-new", Status: session.AgentStatusWaiting, StatusSince: now.Add(-time.Minute)},
			{HarnessSession: "working-old", Status: session.AgentStatusRunning, StatusSince: now.Add(-time.Hour)},
			{HarnessSession: "idle-new", Status: session.AgentStatusIdle, StatusSince: now.Add(-time.Minute)},
			{HarnessSession: "waiting-old", Status: session.AgentStatusWaiting, StatusSince: now.Add(-time.Hour)},
			{HarnessSession: "working-new", Status: session.AgentStatusRunning, StatusSince: now.Add(-time.Minute)},
			{HarnessSession: "beta", Status: session.AgentStatusIdle, StatusSince: now.Add(-2 * time.Minute)},
			{HarnessSession: "Alpha", Status: session.AgentStatusIdle, StatusSince: now.Add(-2 * time.Minute)},
		},
	}

	applyAgentSummaryToSession(
		&row,
		map[string]session.AgentSummary{"$1": summary},
		map[string]session.AgentSummary{testSupportSessionDevName: summary},
	)

	got := make([]string, 0, len(row.Agents))
	sortAgentSessions(row.Agents, agentSortName)

	for _, agent := range row.Agents {
		got = append(got, agent.HarnessSession)
	}

	want := []string{
		"Alpha",
		"beta",
		"idle-new",
		"idle-old",
		"unknown",
		"waiting-new",
		"waiting-old",
		"working-new",
		"working-old",
	}
	if !slicesEqual(got, want) {
		t.Fatalf("sorted agents = %#v, want %#v", got, want)
	}
}

func TestLoadSessionsSkipsAgentSummaryWhenTmuxIDNameConflicts(t *testing.T) {
	model := newTestModelWithDeps(Dependencies{
		LoadCatalog: func(context.Context) (session.CatalogResult, error) {
			return session.CatalogResult{
				Sessions: []session.Session{
					{ID: "$17", Name: "cetk", Kind: session.KindLive},
					{ID: "$21", Name: "interfacy", Kind: session.KindLive},
				},
				Status: session.StatusComplete,
			}, nil
		},
		LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) {
			return []session.AgentSummary{
				{TmuxSessionID: "$17", TmuxSessionName: "dotfiles", Total: 3, Active: 1},
				{TmuxSessionID: "$19", TmuxSessionName: "interfacy", Total: 2, Active: 1},
			}, nil
		},
	})

	if err := model.loadSessions(); err != nil {
		t.Fatalf("loadSessions() returned error: %v", err)
	}

	byName := map[string]session.Session{}
	for _, row := range model.sessions {
		byName[row.Name] = row
	}

	if got := byName["cetk"]; got.AgentActive != 0 || got.AgentTotal != 0 {
		t.Fatalf("cetk agent counts = %d/%d, want 0/0", got.AgentActive, got.AgentTotal)
	}

	if got := byName["interfacy"]; got.AgentActive != 0 || got.AgentTotal != 0 {
		t.Fatalf("interfacy agent counts = %d/%d, want 0/0", got.AgentActive, got.AgentTotal)
	}
}

func TestLoadSessionRowsKeepsRowsOnAgentSummaryError(t *testing.T) {
	model := newTestModelWithDeps(Dependencies{
		LoadCatalog: func(context.Context) (session.CatalogResult, error) {
			return session.CatalogResult{
				Sessions: []session.Session{{ID: "$1", Name: testSupportSessionDevName, Kind: session.KindLive}},
				Status:   session.StatusComplete,
			}, nil
		},
		LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) {
			return nil, errSaveFailed
		},
	})

	loaded := loadSessionRows(context.Background(), model.config, model.deps)
	if !errors.Is(loaded.agentSummaryErr, errSaveFailed) {
		t.Fatalf("agent summary error = %v, want %v", loaded.agentSummaryErr, errSaveFailed)
	}

	model.handleSessionLoadResult(loaded)

	if len(model.sessions) != 1 || model.sessions[0].Name != testSupportSessionDevName {
		t.Fatalf("sessions = %#v, want live row despite agent summary error", model.sessions)
	}

	if !errors.Is(model.agentSummaryErr, errSaveFailed) {
		t.Fatalf("agentSummaryErr = %v, want summary error", model.agentSummaryErr)
	}
}

func TestAgentSummaryRefreshUpdatesCountsWithoutReloadingSessions(t *testing.T) {
	catalogLoads := 0
	summaryLoads := 0

	model := loaderTestModel(t, config.DefaultConfig(), Dependencies{
		LoadCatalog: func(context.Context) (session.CatalogResult, error) {
			catalogLoads++

			return session.CatalogResult{
				Sessions: []session.Session{{ID: "$1", Name: testSupportSessionDevName, Kind: session.KindLive}},
				Status:   session.StatusComplete,
			}, nil
		},
		LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) {
			summaryLoads++

			label := "first"
			if summaryLoads > 1 {
				label = "second"
			}

			return []session.AgentSummary{{
				TmuxSessionID: "$1",
				Total:         2,
				Active:        summaryLoads,
				Agents: []session.AgentSession{{
					Harness: testSupportHarnessCodex, HarnessSession: label, Status: session.AgentStatusRunning,
				}},
			}}, nil
		},
	})
	model.mode = modePopup

	if err := model.loadSessions(); err != nil {
		t.Fatalf("loadSessions() returned error: %v", err)
	}

	if catalogLoads != 1 {
		t.Fatalf("catalog loads after initial load = %d, want 1", catalogLoads)
	}

	command := model.loadAgentSummariesCommand()
	if command == nil {
		t.Fatal("loadAgentSummariesCommand() = nil, want agent refresh command")
	}

	rawMsg := command()

	msg, ok := rawMsg.(agentSummariesMsg)
	if !ok {
		t.Fatalf("loadAgentSummariesCommand() msg = %T, want agentSummariesMsg", rawMsg)
	}

	model.handleAgentSummaries(msg)

	if catalogLoads != 1 {
		t.Fatalf("catalog loads after agent refresh = %d, want unchanged", catalogLoads)
	}

	if got := model.sessions[0]; got.AgentActive != 2 || got.AgentTotal != 2 {
		t.Fatalf("agent counts after refresh = %d/%d, want 2/2", got.AgentActive, got.AgentTotal)
	}

	if got := model.sessions[0].Agents; len(got) != 1 || got[0].HarnessSession != "second" {
		t.Fatalf("agent details after refresh = %#v, want second", got)
	}
}

func TestAgentSummaryRevisionRejectsOlderDelivery(t *testing.T) {
	model := newTestModel()
	model.sessions = []session.Session{{
		ID:   "$1",
		Name: testSupportSessionDevName,
		Kind: session.KindLive,
	}}

	model.handleAgentSummaries(agentSummariesMsg{
		revision: 2,
		summaries: []session.AgentSummary{{
			TmuxSessionID: "$1",
			Total:         1,
			Active:        1,
			Agents: []session.AgentSession{{
				RegistryID: "new",
				Status:     session.AgentStatusRunning,
			}},
		}},
	})
	model.handleAgentSummaries(agentSummariesMsg{
		revision: 1,
		summaries: []session.AgentSummary{{
			TmuxSessionID: "$1",
			Total:         1,
			Agents: []session.AgentSession{{
				RegistryID: "stale",
				Status:     session.AgentStatusIdle,
			}},
		}},
	})

	if model.agentSummaryRevision != 2 {
		t.Fatalf("agent summary revision = %d, want 2", model.agentSummaryRevision)
	}

	if got := model.sessions[0]; got.AgentActive != 1 ||
		len(got.Agents) != 1 ||
		got.Agents[0].RegistryID != "new" {
		t.Fatalf("agent state after stale delivery = %#v, want revision 2 state", got)
	}
}

func TestAgentSummaryRefreshErrorRetainsChildDetails(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.sessions = []session.Session{{
		ID:         "$1",
		Name:       testSupportSessionDevName,
		Kind:       session.KindLive,
		AgentTotal: 1,
		Agents: []session.AgentSession{{
			Harness: testSupportHarnessCodex, HarnessSession: testSupportAgentRegistryID, Status: session.AgentStatusRunning,
		}},
	}}
	model.refreshDisplaySessions()
	model.filterSessions()
	model.agentCursor = 0

	model.handleAgentSummaries(agentSummariesMsg{err: errSaveFailed})

	if got := model.filtered[0].Agents; len(got) != 1 || got[0].HarnessSession != testSupportAgentRegistryID || model.agentCursor != 0 {
		t.Fatalf("agent details/selection = %#v/%d, want retained selected child", got, model.agentCursor)
	}

	if !errors.Is(model.agentSummaryErr, errSaveFailed) {
		t.Fatalf("agentSummaryErr = %v, want summary error", model.agentSummaryErr)
	}
}

func TestAgentClockTickCommandFollowsAgentSummaryEnablement(t *testing.T) {
	cfg := config.DefaultConfig()
	model := loaderTestModel(t, cfg, Dependencies{
		LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) {
			return nil, nil
		},
	})

	if model.agentClockTickCommand() == nil {
		t.Fatal("agentClockTickCommand() = nil, want tick while agent summaries are enabled")
	}

	cfg.AgentSessions.Enabled = false

	if got := model.agentClockTickCommand(); got != nil {
		t.Fatalf("agentClockTickCommand() = %v, want nil without agent summaries", got)
	}
}

func TestAgentSummaryRefreshRestoresSelectedChildByStableIdentity(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.sessions = []session.Session{{
		ID:         "$1",
		Name:       testSupportSessionDevName,
		Kind:       session.KindLive,
		AgentTotal: 2,
		Agents: []session.AgentSession{
			{RegistryID: "record-1", Harness: "pi", HarnessSession: "first", PaneID: "%1"},
			{RegistryID: "record-2", Harness: testSupportHarnessCodex, HarnessSession: "selected", PaneID: "%1"},
		},
	}}
	model.refreshDisplaySessions()
	model.filterSessions()
	model.agentCursor = 1

	model.applyAgentSummaries([]session.AgentSummary{{
		TmuxSessionID: "$1",
		Total:         2,
		Agents: []session.AgentSession{
			{RegistryID: "record-2", Harness: testSupportHarnessCodex, HarnessSession: "selected-updated", PaneID: "%1"},
			{RegistryID: "record-1", Harness: "pi", HarnessSession: "first", PaneID: "%1"},
		},
	}})

	if model.cursor != 0 || model.agentCursor != 1 {
		t.Fatalf("restored selection = %d/%d, want sorted selected pane at 0/1", model.cursor, model.agentCursor)
	}

	if got := model.filtered[0].Agents[model.agentCursor]; got.RegistryID != "record-2" ||
		got.HarnessSession != "selected-updated" {
		t.Fatalf("restored selected agent = %#v, want updated record-2", got)
	}
}

func TestAgentSummaryRefreshFallsBackToFirstAgentWhenSelectedAgentDisappears(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.sessions = []session.Session{{
		ID:         "$1",
		Name:       testSupportSessionDevName,
		Kind:       session.KindLive,
		AgentTotal: 1,
		Agents:     []session.AgentSession{{RegistryID: "record-1", Harness: "pi", HarnessSession: "gone", PaneID: "%1"}},
	}}
	model.refreshDisplaySessions()
	model.filterSessions()
	model.agentCursor = 0

	model.applyAgentSummaries([]session.AgentSummary{{
		TmuxSessionID: "$1",
		Total:         1,
		Agents:        []session.AgentSession{{RegistryID: "record-2", Harness: testSupportHarnessCodex, HarnessSession: "new", PaneID: "%1"}},
	}})

	if model.cursor != 0 || model.agentCursor != 0 {
		t.Fatalf("fallback selection = %d/%d, want first replacement agent", model.cursor, model.agentCursor)
	}
}

func TestSeshOnlyWithoutPopupCountsSkipsAgentSummaryLoading(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Settings.DisplayModes = []string{"sesh"}
	cfg.AgentSessions.PopupCounts = false

	summaryLoads := 0

	model := loaderTestModel(t, cfg, Dependencies{
		LoadCatalog: func(context.Context) (session.CatalogResult, error) {
			return session.CatalogResult{
				Sessions: []session.Session{{ID: "$1", Name: testSupportSessionDevName, Kind: session.KindLive}},
				Status:   session.StatusComplete,
			}, nil
		},
		LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) {
			summaryLoads++

			return nil, nil
		},
	})

	if err := model.loadSessions(); err != nil {
		t.Fatalf("loadSessions() error = %v", err)
	}

	if summaryLoads != 0 || model.agentSummaryRefreshEnabled() {
		t.Fatalf("SESH-only summary loading/refresh = %d/%v, want disabled", summaryLoads, model.agentSummaryRefreshEnabled())
	}
}

func TestSeshOnlyCustomAgentFieldsLoadAgentSummaries(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Settings.DisplayModes = []string{"sesh"}
	cfg.AgentSessions.PopupCounts = false
	cfg.Display.SeshFormat = "{session_name}{right}{agent_working}/{agent_total}"

	summaryLoads := 0

	model := loaderTestModel(t, cfg, Dependencies{
		LoadCatalog: func(context.Context) (session.CatalogResult, error) {
			return session.CatalogResult{
				Sessions: []session.Session{{ID: "$1", Name: testSupportSessionDevName, Kind: session.KindLive}},
				Status:   session.StatusComplete,
			}, nil
		},
		LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) {
			summaryLoads++

			return []session.AgentSummary{{TmuxSessionID: "$1", Total: 3, Active: 2}}, nil
		},
	})

	if err := model.loadSessions(); err != nil {
		t.Fatalf("loadSessions() error = %v", err)
	}

	if summaryLoads != 1 {
		t.Fatalf("agent summary loads = %d, want 1", summaryLoads)
	}

	if got := model.sessions[0]; got.AgentTotal != 3 || got.AgentActive != 2 {
		t.Fatalf("custom SESH agent counts = %d/%d, want 2/3", got.AgentActive, got.AgentTotal)
	}

	if !model.agentSummaryRefreshEnabled() {
		t.Fatal("agent summary refresh disabled for a SESH format that uses agent fields")
	}
}

func TestAgentDisplayModeLoadsSummariesWithoutPopupCounts(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Settings.DisplayModes = []string{"agents"}
	cfg.AgentSessions.PopupCounts = false

	summaryLoads := 0

	model := loaderTestModel(t, cfg, Dependencies{
		LoadCatalog: func(context.Context) (session.CatalogResult, error) {
			return session.CatalogResult{
				Sessions: []session.Session{{ID: "$1", Name: testSupportSessionDevName, Kind: session.KindLive}},
				Status:   session.StatusComplete,
			}, nil
		},
		LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) {
			summaryLoads++

			return []session.AgentSummary{{
				TmuxSessionID: "$1",
				Total:         1,
				Active:        1,
				Agents:        []session.AgentSession{{RegistryID: testSupportAgentRegistryID, Harness: "pi", Status: session.AgentStatusRunning}},
			}}, nil
		},
	})

	if err := model.loadSessions(); err != nil {
		t.Fatalf("loadSessions() error = %v", err)
	}

	if summaryLoads != 1 {
		t.Fatalf("agent summary loads = %d, want 1 in AGENTS-only mode", summaryLoads)
	}

	if got := model.sessions[0]; got.AgentTotal != 1 || len(got.Agents) != 1 {
		t.Fatalf("dev agent row = total %d details %#v, want agent inventory", got.AgentTotal, got.Agents)
	}
}

func assertPartialLoadState(t *testing.T, model *Model, loaded sessionLoadResultMsg) {
	t.Helper()

	if !errors.Is(loaded.catalogErr, errTmuxUnavailable) {
		t.Fatalf("loadSessionRows() catalog error = %v, want %v", loaded.catalogErr, errTmuxUnavailable)
	}

	if loaded.status != session.StatusPartial {
		t.Fatalf("loadSessionRows() status = %q, want partial", loaded.status)
	}

	if len(loaded.sessions) != 1 || loaded.sessions[0].Name != "live" {
		t.Fatalf("loadSessionRows() sessions = %#v, want the partial catalog row", loaded.sessions)
	}

	model.handleSessionLoadResult(loaded)

	if model.statusLevel != statusDegraded || !strings.Contains(model.statusMessage, "Session list is incomplete") {
		t.Fatalf(
			"partial catalog status = (%v, %q), want degraded incomplete-catalog line",
			model.statusLevel,
			model.statusMessage,
		)
	}

	if got := model.degradedSources[degradedSourceCatalog]; got == "" {
		t.Fatalf("degraded sources = %#v, want the catalog source recorded", model.degradedSources)
	}

	if len(model.sessions) != 1 || model.sessions[0].Name != "live" {
		t.Fatalf("sessions after partial load = %#v, want the reported row", model.sessions)
	}
}

func TestSessionLoadPreservesNewerRemoteWatchObservations(t *testing.T) {
	for _, testCase := range []struct{ loadMode, observation string }{
		{"initial", "replacement"},
		{"initial", "removal"},
		{"initial", "error"},
		{"refresh", "replacement"},
		{"refresh", "removal"},
		{"refresh", "error"},
	} {
		loadMode, observation := testCase.loadMode, testCase.observation
		t.Run(loadMode+"/"+observation, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Remote.Enabled = true
			cfg.Remote.Connections = []config.RemoteConnectionConfig{{ID: "host", Host: "host"}}
			oldCatalog := remoteCatalogWithSession("old")
			model := loaderTestModel(t, cfg, Dependencies{
				LoadCatalog: func(context.Context) (session.CatalogResult, error) {
					return session.CatalogResult{Sessions: []session.Session{
						{ID: "$local", Name: "local", Kind: session.KindLive},
						{ID: "$origin", Name: "origin", Kind: session.KindLive, Origin: session.OriginBridge},
					}, Status: session.StatusComplete}, nil
				},
				LoadRemoteCatalog: func() (RemoteCatalog, error) { return oldCatalog, nil },
			})

			command := model.loadInitialSessionsCommand()
			if loadMode == "refresh" {
				command = model.refreshSessions()
			}

			loaded := command()

			model.handleRemoteCatalog(remoteCatalogMsg{catalog: remoteCatalogWithSession("new")})

			wantNames := []string{"local", "new", "origin"}

			switch observation {
			case "removal":
				model.handleRemoteCatalog(remoteCatalogMsg{})

				wantNames = []string{"local", "origin"}
			case "error":
				model.handleRemoteCatalog(remoteCatalogMsg{err: errTmuxUnavailable})
			}

			selected := model.selectedSessionIdentity()
			updateTestModel(t, model, loaded)
			gotNames := sessionNamesForTest(model.sessions)
			slices.Sort(gotNames)

			if !slices.Equal(gotNames, wantNames) {
				t.Fatalf("sessions after delayed load = %v, want %v", gotNames, wantNames)
			}

			if observation != "removal" && model.selectedSessionIdentity() != selected {
				t.Fatalf("selection = %#v, want watched row %#v", model.selectedSessionIdentity(), selected)
			}

			if observation == "error" && (!errors.Is(model.remoteCatalogErr, errTmuxUnavailable) || model.degradedSources[degradedSourceRemote] == "") {
				t.Fatal("delayed load cleared the newer remote watch error")
			}
		})
	}
}

func remoteCatalogWithSession(name string) RemoteCatalog {
	return RemoteCatalog{Connections: []RemoteConnection{{
		ID: "host", Connected: true,
		Sessions: []session.Session{{ID: "$remote", Name: name, Kind: session.KindLive}},
	}}}
}

func TestAgentWatchValidatesPanesBeforePublishing(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		panes   []PaneLocation
		paneErr error
		want    int
	}{
		{name: "only owned live pane survives", panes: []PaneLocation{
			{SessionID: "$1", SessionName: testSupportSessionDevName, PaneID: "%1", PanePID: 101, WindowIndex: "0", PaneLeft: "0", PaneTop: "0"},
			{SessionID: "$other", SessionName: "other", PaneID: "%2", PanePID: 102},
		}, want: 1},
		{name: "successful empty inventory", want: 0},
		{name: "unavailable inventory", paneErr: errTmuxUnavailable, want: 3},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			summaries := []session.AgentSummary{{
				TmuxSessionID: "$1", TmuxSessionName: testSupportSessionDevName, Total: 3, Active: 3,
				Agents: []session.AgentSession{
					{RegistryID: "live", PaneID: "%1", PanePID: 101, Status: session.AgentStatusRunning},
					{RegistryID: "moved", PaneID: "%2", PanePID: 102, Status: session.AgentStatusRunning},
					{RegistryID: "gone", PaneID: "%3", PanePID: 103, Status: session.AgentStatusRunning},
				},
			}}
			paneLoads := 0
			model := loaderTestModel(t, config.DefaultConfig(), Dependencies{
				WatchAgentSummaries: func(_ context.Context, publish func(uint64, []session.AgentSummary, error) error) error {
					return publish(1, summaries, nil)
				},
				TmuxPaneLocations: func(context.Context) ([]PaneLocation, error) {
					paneLoads++
					return testCase.panes, testCase.paneErr
				},
			})
			model.sessions = []session.Session{{ID: "$1", Name: testSupportSessionDevName, Kind: session.KindLive}}
			model.startAgentSummaryWatchCommand()()
			message := model.waitAgentSummaryUpdateCommand()()
			loadsBeforeUpdate := paneLoads

			updateTestModel(t, model, message)

			row := model.sessions[0]
			if len(row.Agents) != testCase.want || row.AgentTotal != testCase.want || row.AgentActive != testCase.want {
				t.Fatalf("watched agents/counts = %d/%d/%d, want %d", len(row.Agents), row.AgentTotal, row.AgentActive, testCase.want)
			}

			if paneLoads != 1 || loadsBeforeUpdate != paneLoads {
				t.Fatalf("pane queries before/after Update = %d/%d, want one worker query", loadsBeforeUpdate, paneLoads)
			}

			if len(summaries[0].Agents) != 3 || summaries[0].Agents[0].PositionKnown {
				t.Fatal("watch validation changed broker-owned data")
			}
		})
	}
}

func TestAgentWatchUnavailableBrokerUsesPollingWithoutWarning(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		pollEnabled bool
	}{
		{name: "polling available", pollEnabled: true},
		{name: "polling disabled", pollEnabled: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			if !testCase.pollEnabled {
				cfg.AgentSessions.RefreshInterval = ""
			}

			model := loaderTestModel(t, cfg, Dependencies{
				WatchAgentSummaries: func(context.Context, func(uint64, []session.AgentSummary, error) error) error {
					return agents.ErrRegistryUnavailable
				},
			})
			model.width = 80
			model.height = 20
			model.handleSessionLoadResult(sessionLoadResultMsg{
				sessions: []session.Session{{ID: "$1", Name: testSupportSessionDevName, Kind: session.KindLive}},
				status:   session.StatusComplete,
			})

			command := model.startAgentSummaryWatchCommand()
			if command == nil {
				t.Fatal("agent watch command was not started")
			}

			command()

			select {
			case message := <-model.agentSummaryUpdates:
				updateTestModel(t, model, message)
			default:
			}

			warningVisible := strings.Contains(stripANSI(viewString(model.View())), "⚠") ||
				strings.Contains(stripANSI(viewString(model.View())), "Agent status unavailable")
			if warningVisible == testCase.pollEnabled {
				t.Fatalf("agent warning visible = %t, poll available = %t", warningVisible, testCase.pollEnabled)
			}
		})
	}
}

func TestRemoteWatchErrorFencesOutstandingRead(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Remote.Enabled = true
	cfg.Remote.Connections = []config.RemoteConnectionConfig{{ID: "host", Host: "host"}}
	model := loaderTestModel(t, cfg, Dependencies{
		LoadRemoteCatalog: func() (RemoteCatalog, error) { return remoteCatalogWithSession("cached"), nil },
	})
	loaded := model.loadInitialSessionsCommand()()
	model.handleRemoteCatalog(remoteCatalogMsg{err: errTmuxUnavailable})
	updateTestModel(t, model, loaded)

	if !errors.Is(model.remoteCatalogErr, errTmuxUnavailable) || model.degradedSources[degradedSourceRemote] == "" {
		t.Fatal("outstanding initial read erased the newer watch failure")
	}

	updateTestModel(t, model, model.refreshSessions()())

	if model.remoteCatalogErr != nil || model.degradedSources[degradedSourceRemote] != "" {
		t.Fatal("a new successful read did not recover the failed watch")
	}
}

func TestRemoteWatchDeliversQueuedRemoval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.Remote.Enabled = true
		cfg.Remote.Connections = []config.RemoteConnectionConfig{{ID: "host", Host: "host"}}
		model := loaderTestModel(t, cfg, Dependencies{
			WatchRemoteCatalog: func(_ context.Context, publish func(RemoteCatalog, error)) error {
				publish(remoteCatalogWithSession("remote"), nil)
				publish(RemoteCatalog{}, nil)

				return nil
			},
		})

		command := model.startRemoteCatalogWatchCommand()
		go command()

		synctest.Wait()
		updateTestModel(t, model, model.waitRemoteCatalogUpdateCommand()())

		if len(model.sessions) != 1 {
			t.Fatal("first remote observation was not delivered")
		}

		synctest.Wait()

		if len(model.remoteCatalogUpdates) != 1 {
			t.Fatal("queued remote removal was lost")
		}

		updateTestModel(t, model, model.waitRemoteCatalogUpdateCommand()())

		if len(model.sessions) != 0 {
			t.Fatal("queued removal retained a remote session")
		}
	})
}

func TestAgentWatchDeliversQueuedRemoval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		model := loaderTestModel(t, config.DefaultConfig(), Dependencies{
			WatchAgentSummaries: func(_ context.Context, publish func(uint64, []session.AgentSummary, error) error) error {
				if err := publish(1, []session.AgentSummary{{
					TmuxSessionID: "$1", Total: 1,
					Agents: []session.AgentSession{{RegistryID: "live", PaneID: "%1"}},
				}}, nil); err != nil {
					return err
				}

				return publish(2, nil, nil)
			},
		})
		model.sessions = []session.Session{{ID: "$1", Name: testSupportSessionDevName, Kind: session.KindLive}}

		command := model.startAgentSummaryWatchCommand()
		go command()

		synctest.Wait()
		updateTestModel(t, model, model.waitAgentSummaryUpdateCommand()())

		if len(model.sessions[0].Agents) != 1 {
			t.Fatal("first agent observation was not delivered")
		}

		synctest.Wait()

		if len(model.agentSummaryUpdates) != 1 {
			t.Fatal("queued agent removal was lost")
		}

		updateTestModel(t, model, model.waitAgentSummaryUpdateCommand()())

		if len(model.sessions[0].Agents) != 0 || model.sessions[0].AgentTotal != 0 {
			t.Fatal("queued removal retained an agent or stale count")
		}
	})
}

func TestSessionLoadPreservesNewerAgentWatchObservations(t *testing.T) {
	for _, observation := range []string{"replacement", "removal", "error"} {
		t.Run(observation, func(t *testing.T) {
			model := loaderTestModel(t, config.DefaultConfig(), Dependencies{
				LoadCatalog: func(context.Context) (session.CatalogResult, error) {
					return session.CatalogResult{Status: session.StatusComplete, Sessions: []session.Session{{
						ID: "$1", Name: testSupportSessionDevName, Kind: session.KindLive,
					}}}, nil
				},
				LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) {
					return []session.AgentSummary{{TmuxSessionID: "$1", Total: 1, Agents: []session.AgentSession{{RegistryID: "old"}}}}, nil
				},
			})
			loaded := model.loadInitialSessionsCommand()()
			model.handleAgentSummaries(agentSummariesMsg{
				revision:  1,
				summaries: []session.AgentSummary{{TmuxSessionID: "$1", Total: 1, Agents: []session.AgentSession{{RegistryID: "new"}}}},
			})

			if observation == "removal" {
				model.handleAgentSummaries(agentSummariesMsg{revision: 2})
			}

			if observation == "error" {
				model.handleAgentSummaries(agentSummariesMsg{revision: 2, err: errTmuxUnavailable})
			}

			updateTestModel(t, model, loaded)

			assertAgentWatchObservation(t, model, observation)

			updateTestModel(t, model, model.refreshSessions()())

			if agents := model.sessions[0].Agents; len(agents) != 1 || agents[0].RegistryID != "old" {
				t.Fatalf("fresh load was ignored after earlier watch delivery: %#v", agents)
			}
		})
	}
}

func assertAgentWatchObservation(t *testing.T, model *Model, observation string) {
	t.Helper()

	row := model.sessions[0]
	if observation == "removal" {
		if len(row.Agents) != 0 || row.AgentTotal != 0 {
			t.Fatalf("late load resurrected removed agents: %#v", row.Agents)
		}
	} else if len(row.Agents) != 1 || row.Agents[0].RegistryID != "new" {
		t.Fatalf("late load replaced newer agent: %#v", row.Agents)
	}

	if observation == "error" && (!errors.Is(model.agentSummaryErr, errTmuxUnavailable) || model.degradedSources[degradedSourceAgents] == "") {
		t.Fatal("late load erased the newer agent error")
	}
}

func TestAgentWatchFailureRejectsOlderRevision(t *testing.T) {
	model := newTestModel()
	t.Cleanup(model.Close)
	model.handleAgentSummaries(agentSummariesMsg{revision: 3, err: errTmuxUnavailable})
	model.handleAgentSummaries(agentSummariesMsg{revision: 2})

	if !errors.Is(model.agentSummaryErr, errTmuxUnavailable) || model.degradedSources[degradedSourceAgents] == "" {
		t.Fatal("stale agent revision cleared the newer watch failure")
	}
}

func TestAgentPollPreservesNewerWatchObservations(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		observation string
		pollErr     error
	}{
		{name: "replacement", observation: "replacement"},
		{name: "removal", observation: "removal"},
		{name: "watch error", observation: "error"},
		{name: "stale poll error", observation: "replacement", pollErr: errSaveFailed},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			pollErr := testCase.pollErr
			model := loaderTestModel(t, config.DefaultConfig(), Dependencies{
				LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) {
					return []session.AgentSummary{{
						TmuxSessionID: "$1", Total: 1,
						Agents: []session.AgentSession{{RegistryID: "old"}},
					}}, pollErr
				},
			})
			model.sessions = []session.Session{{ID: "$1", Name: testSupportSessionDevName, Kind: session.KindLive}}
			loaded := model.loadAgentSummariesCommand()()
			model.handleAgentSummaries(agentSummariesMsg{
				revision:  1,
				summaries: []session.AgentSummary{{TmuxSessionID: "$1", Total: 1, Agents: []session.AgentSession{{RegistryID: "new"}}}},
			})

			switch testCase.observation {
			case "removal":
				model.handleAgentSummaries(agentSummariesMsg{revision: 2})
			case "error":
				model.handleAgentSummaries(agentSummariesMsg{revision: 2, err: errTmuxUnavailable})
			}

			updateTestModel(t, model, loaded)
			assertAgentWatchObservation(t, model, testCase.observation)

			if testCase.pollErr != nil && model.agentSummaryErr != nil {
				t.Fatal("stale polling failure replaced the successful watch observation")
			}

			pollErr = nil

			updateTestModel(t, model, model.loadAgentSummariesCommand()())

			if agents := model.sessions[0].Agents; len(agents) != 1 || agents[0].RegistryID != "old" {
				t.Fatalf("new periodic poll was ignored after watch delivery: %#v", agents)
			}
		})
	}
}
