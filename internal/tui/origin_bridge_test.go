package tui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func TestOriginCatalogAddsDistinctRowsWithoutReplacingLocalNames(t *testing.T) {
	deps := testServices(Dependencies{
		LoadCatalog: func(context.Context) (session.CatalogResult, error) {
			return session.CatalogResult{Sessions: []session.Session{{ID: "$1", Name: "work", Kind: session.KindLive, Origin: session.OriginLocal}}}, nil
		},
		LoadOriginSessions: func(context.Context) (string, []session.Session, error) {
			return "Laptop", []session.Session{{
				ID: "$1", Name: "work", Kind: session.KindLive, Origin: session.OriginBridge,
				RuntimeIdentity: session.RuntimeIdentity{ConnectionID: "origin", ConnectionLabel: "Laptop"},
			}}, nil
		},
	})

	result := loadSessionRows(t.Context(), config.DefaultConfig(), deps)
	if result.catalogErr != nil || len(result.sessions) != 2 {
		t.Fatalf("combined rows = %#v, %v", result.sessions, result.catalogErr)
	}

	if result.sessions[0].SemanticIdentity() == result.sessions[1].SemanticIdentity() {
		t.Fatal("origin and local names collided")
	}
}

func TestInitialLocalRowsAppearBeforeOriginDiscoveryCompletes(t *testing.T) {
	releaseOrigin := make(chan struct{})
	model := loaderTestModel(t, config.DefaultConfig(), Dependencies{
		LoadCatalog: func(context.Context) (session.CatalogResult, error) {
			return session.CatalogResult{Sessions: []session.Session{{ID: "$1", Name: "local", Kind: session.KindLive}}}, nil
		},
		LoadOriginSessions: func(ctx context.Context) (string, []session.Session, error) {
			select {
			case <-releaseOrigin:
				return "origin", []session.Session{{ID: "$2", Name: "origin", Kind: session.KindLive, Origin: session.OriginBridge}}, nil
			case <-ctx.Done():
				return "", nil, ctx.Err()
			}
		},
	})

	originResult := make(chan tea.Msg, 1)
	go func() { originResult <- model.loadOriginSessionsCommand()() }()

	initial, ok := model.loadInitialSessionsCommand()().(sessionLoadResultMsg)
	if !ok {
		t.Fatal("initial session command returned no catalog")
	}

	model.handleSessionLoadResult(initial)

	if len(model.sessions) != 1 || model.sessions[0].Name != "local" {
		t.Fatalf("initial sessions = %#v, want local row", model.sessions)
	}

	close(releaseOrigin)

	select {
	case msg := <-originResult:
		origin, ok := msg.(originSessionsMsg)
		if !ok {
			t.Fatal("origin command returned no catalog")
		}

		model.handleOriginSessions(origin)
	case <-time.After(time.Second):
		t.Fatal("origin discovery did not finish")
	}

	if len(model.sessions) != 2 || model.sessions[0].Name == model.sessions[1].Name {
		t.Fatalf("completed sessions = %#v, want local and origin rows", model.sessions)
	}
}

func TestFailedOriginRefreshKeepsObservedOriginRows(t *testing.T) {
	model := newTestModel()
	origin := session.Session{ID: "$2", Name: "origin", Kind: session.KindLive, Origin: session.OriginBridge}
	model.handleOriginSessions(originSessionsMsg{rows: []session.Session{origin}, observation: 0, err: nil})

	model.applySessionRows(sessionLoadResultMsg{
		sessions:      []session.Session{{ID: "$1", Name: "local", Kind: session.KindLive}},
		originQueried: true,
		originErr:     errTmuxUnavailable,
	})

	if len(model.sessions) != 2 {
		t.Fatalf("failed origin refresh removed a row: %#v", model.sessions)
	}
}

func TestOriginSwitchLogsOnlyItsPrivateFailureBoundary(t *testing.T) {
	var output bytes.Buffer

	model := loggingTestModel(t, &output)
	target := session.Session{ID: "$secret", Name: loggingTestPrivacySentinel, Origin: session.OriginBridge, Kind: session.KindLive}
	deps := dependenciesForTest(model)
	deps.RequestOriginSwitch = func(context.Context, session.Session) error { return errLoggingTestActionFailure }
	model.sessions = []session.Session{target}
	model.filtered = model.sessions

	leaked := loggingTestWithCapturedStdStreams(t, func() {
		trace := pressTestKey(t, model, keyCode(tea.KeyEnter))
		if len(trace.results) != 1 || trace.quit || model.DeferredOriginDetach() {
			t.Fatalf("failed origin switch trace = %+v, detach %t", trace, model.DeferredOriginDetach())
		}
	})
	if leaked != "" || strings.Contains(output.String(), loggingTestPrivacySentinel) {
		t.Fatal("origin boundary leaked into streams or diagnostic fields")
	}

	records := loggingTestRecords(t, &output)
	if len(records) != 1 {
		t.Fatalf("origin outcome record count = %d, want 1", len(records))
	}

	assertLoggingTestRecord(t, records[0], tuiOperationEnterLiveSession, "error", "failed", "tui operation failed")
}

func TestOriginDiscoveryDeadlinePreservesLocalCatalog(t *testing.T) {
	var expired error

	deps := testServices(Dependencies{
		LoadCatalog: func(context.Context) (session.CatalogResult, error) {
			return session.CatalogResult{Sessions: []session.Session{{ID: "$1", Name: "local"}}}, nil
		},
		LoadOriginSessions: func(ctx context.Context) (string, []session.Session, error) {
			deadline, present := ctx.Deadline()
			if !present || time.Until(deadline) > originDiscoveryTimeout {
				t.Error("optional origin discovery was not bounded")
			}

			<-ctx.Done()
			expired = ctx.Err()

			return "", nil, ctx.Err()
		},
	})

	result := loadSessionRows(t.Context(), config.DefaultConfig(), deps)
	if !errors.Is(expired, context.DeadlineExceeded) || result.catalogErr != nil || len(result.sessions) != 1 || result.sessions[0].Name != "local" {
		t.Fatalf("origin timeout affected local catalog: %#v, %v", result, expired)
	}
}

func TestOriginSelectionRequestsExactIdentityBeforeDetach(t *testing.T) {
	t.Run("accepted", func(t *testing.T) { checkOriginSelectionOutcome(t, false) })
	t.Run("refused", func(t *testing.T) { checkOriginSelectionOutcome(t, true) })
}

func checkOriginSelectionOutcome(t *testing.T, refused bool) {
	t.Helper()

	target := session.Session{
		ID: "$7", Name: "notes", Kind: session.KindLive, Origin: session.OriginBridge,
		RuntimeIdentity: session.RuntimeIdentity{ConnectionID: "origin", TmuxServerPID: 123},
	}
	calls := 0
	model := newTestModelWithDeps(Dependencies{
		RequestOriginSwitch: func(_ context.Context, selected session.Session) error {
			calls++

			if selected.ID != target.ID || selected.Name != target.Name ||
				selected.RuntimeIdentity.TmuxServerPID != target.RuntimeIdentity.TmuxServerPID {
				t.Fatal("origin selection discarded observed identity")
			}

			if refused {
				return errTmuxUnavailable
			}

			return nil
		},
	})
	t.Cleanup(model.Close)

	model.sessions = []session.Session{target}
	model.filtered = model.sessions

	_, command := model.Update(keyCode(tea.KeyEnter))

	if calls != 0 {
		t.Fatal("origin request blocked the model update")
	}

	trace := runActionTrace(t, model, command)
	if calls != 1 || model.DeferredOriginDetach() == refused || trace.quit == refused {
		t.Fatalf("calls = %d, detach = %t, quit = %t", calls, model.DeferredOriginDetach(), trace.quit)
	}

	if refused && model.statusLevel != statusError {
		t.Fatal("rejected request did not report an error")
	}
}

func TestRemoteEntryDefersSSHUntilPickerCloses(t *testing.T) {
	model := newTestModel()
	t.Cleanup(model.Close)

	target := session.Session{ID: "$9", Name: "remote", Kind: session.KindLive, Origin: session.OriginSSH}
	model.deps.RemoteAttach = &testServiceAdapter{deps: &Dependencies{
		AttachRemoteSession: func(context.Context, session.Session, string) (tea.ExecCommand, error) {
			t.Fatal("SSH began before the picker released the terminal")
			return nil, errTmuxUnavailable
		},
	}}

	_, command := model.handleRemoteEnter(target, popupListRow{})()
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatal("remote entry did not release the picker")
	}

	selected, _, present := model.DeferredRemoteAttach()
	if !present || selected.ID != target.ID {
		t.Fatal("remote entry lost its selected identity")
	}
}

func TestOriginRowsKeepTheirOwnAgentCountsAndPositions(t *testing.T) {
	model := newTestModel()
	t.Cleanup(model.Close)
	model.sessions = []session.Session{{
		ID: "$1", Name: "work", Origin: session.OriginBridge, Kind: session.KindLive,
		AgentTotal: 3, AgentActive: 2, AgentWaiting: 1,
		Agents: []session.AgentSession{{PaneID: "%1", PaneLeft: 40}},
	}}
	model.applyAgentSummaries([]session.AgentSummary{{TmuxSessionID: "$1", Total: 9, Active: 9}})
	model.applyAgentPositions(agentPositionsMsg{ready: true, panes: []PaneLocation{{PaneID: "%1", PaneLeft: "2"}}})

	row := model.sessions[0]
	if row.AgentTotal != 3 || row.AgentActive != 2 || row.AgentWaiting != 1 || len(row.Agents) != 1 || row.Agents[0].PaneLeft != 40 {
		t.Fatalf("origin state was overwritten by local agents: %#v", row)
	}

	if views := model.recordActiveAgentViews(); len(views) != 0 {
		t.Fatal("origin agents were recorded as active local views")
	}
}
