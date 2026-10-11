//go:build (linux || darwin) && integration

package session_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/integration/tmuxserver"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const (
	testClientTimeout = 15 * time.Second
	testClientPoll    = 20 * time.Millisecond
)

type workflowFixture struct {
	server      *gotmux.Server
	historyPath string
	leaseRoot   string
}

func newWorkflowFixture(t *testing.T) *workflowFixture {
	t.Helper()

	return &workflowFixture{
		server:      tmuxtest.NewServer(t),
		historyPath: testHistoryPath(t),
		leaseRoot:   filepath.Join(t.TempDir(), "leases"),
	}
}

func testHistoryPath(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "history")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	return filepath.Join(dir, "history.json")
}

func (f *workflowFixture) workflow(getenv func(string) string, repo *store.Repository, hooks config.HookConfig) *session.Workflow {
	return session.NewWorkflowWithOptions(session.WorkflowOptions{
		Server:      f.server,
		Repository:  repo,
		LeaseRoot:   f.leaseRoot,
		HistoryPath: f.historyPath,
		Hooks:       hooks,
		HookRunner:  nil,
		Getenv:      getenv,
		Streams:     gotmux.Streams{},
		Source:      "test",
	})
}

func (f *workflowFixture) endpointScope(t *testing.T) string {
	t.Helper()

	scope, err := model.EndpointScope(f.server.Endpoint().SocketPath)
	if err != nil {
		t.Fatalf("deriving fixture endpoint scope: %v", err)
	}

	return scope
}

func (f *workflowFixture) fixtureSession(t *testing.T) gotmux.SessionInfo {
	t.Helper()

	sessions, err := f.server.Sessions(t.Context())
	if err != nil || len(sessions) == 0 {
		t.Fatalf("fixture sessions: %v", err)
	}

	for _, sess := range sessions {
		if sess.Name == "fixture" {
			return sess
		}
	}

	t.Fatalf("fixture session missing from %d sessions", len(sessions))

	return gotmux.SessionInfo{}
}

func (f *workflowFixture) newLiveSession(t *testing.T, name string) gotmux.SessionInfo {
	t.Helper()

	sess, err := f.server.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name:    name,
		Program: gotmux.Exec("/bin/sh"),
		Start:   gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("creating session %s: %v", name, err)
	}

	info, err := sess.Info(t.Context())
	if err != nil {
		t.Fatalf("reading session %s: %v", name, err)
	}

	return info
}

func (f *workflowFixture) firstPane(t *testing.T, sess gotmux.SessionInfo) gotmux.PaneInfo {
	t.Helper()

	panes, err := sess.Handle().Panes(t.Context())
	if err != nil || len(panes) == 0 {
		t.Fatalf("panes for session %s: %v", sess.ID, err)
	}

	return panes[0]
}

func (f *workflowFixture) ambientEnvironment(t *testing.T, sess gotmux.SessionInfo) func(string) string {
	t.Helper()

	probe, err := f.server.Probe(t.Context())
	if err != nil {
		t.Fatalf("probing fixture server: %v", err)
	}

	pane := f.firstPane(t, sess)

	values := map[string]string{
		"TMUX":      f.server.Endpoint().SocketPath + "," + strconv.Itoa(probe.Identity.PID) + "," + strings.TrimPrefix(string(sess.ID), "$"),
		"TMUX_PANE": string(pane.ID),
	}

	return func(key string) string { return values[key] }
}

func (f *workflowFixture) attachClient(t *testing.T, sessionID gotmux.SessionID) string {
	t.Helper()

	handle, err := f.server.Session(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("session handle: %v", err)
	}

	_, slave := openTestPTY(t)

	attachCtx, cancelAttach := context.WithCancel(context.WithoutCancel(t.Context()))

	done := make(chan error, 1)

	go func() {
		done <- handle.Attach(attachCtx, gotmux.Streams{In: slave, Out: slave, Err: slave}, gotmux.AttachOptions{
			ReadOnly:            false,
			PreserveEnvironment: true,
			Detach:              gotmux.DetachModeNone,
			Dir:                 "",
			Flags:               nil,
		})
	}()

	t.Cleanup(func() {
		cancelAttach()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Logf("attach client for %s did not exit in time", sessionID)
		}
	})

	return f.waitForClient(t, sessionID)
}

func (f *workflowFixture) waitForClient(t *testing.T, sessionID gotmux.SessionID) string {
	t.Helper()

	deadline := time.Now().Add(testClientTimeout)

	for time.Now().Before(deadline) {
		for _, client := range f.clients(t) {
			displayed, ok := client.SessionID.Get()
			if ok && displayed == sessionID {
				return string(client.Name)
			}
		}

		time.Sleep(testClientPoll)
	}

	t.Fatalf("no client displayed session %s within %s", sessionID, testClientTimeout)

	return ""
}

func (f *workflowFixture) clients(t *testing.T) []gotmux.ClientInfo {
	t.Helper()

	clients, err := f.server.Clients(t.Context())
	if err != nil {
		t.Fatalf("listing clients: %v", err)
	}

	visible := make([]gotmux.ClientInfo, 0, len(clients))

	for _, client := range clients {
		if !client.Control {
			visible = append(visible, client)
		}
	}

	return visible
}

func (f *workflowFixture) clientSession(t *testing.T, name string) gotmux.SessionID {
	t.Helper()

	for _, client := range f.clients(t) {
		if string(client.Name) != name {
			continue
		}

		displayed, ok := client.SessionID.Get()
		if !ok {
			t.Fatalf("client %s reported no session", name)
		}

		return displayed
	}

	t.Fatalf("client %s is not attached", name)

	return ""
}

func openTestRepository(t *testing.T) *store.Repository {
	t.Helper()

	repo, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "repository"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	})

	return repo
}

func commitSavedGeneration(t *testing.T, repo *store.Repository, endpointScope string, names ...string) store.Generation {
	t.Helper()

	attempt, err := repo.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	manifest := savedManifest(names...)
	manifest.Source.EndpointScope = endpointScope

	generation, err := repo.Commit(t.Context(), attempt.ID, manifest, map[string]io.Reader{})
	if err != nil {
		t.Fatal(err)
	}

	return generation
}

func savedManifest(names ...string) model.Manifest {
	sessions := make([]model.Session, 0, len(names))
	links := make([]model.Link, 0, len(names))

	for index, name := range names {
		id := "s" + strconv.Itoa(index)
		sessions = append(sessions, model.Session{ID: id, Name: model.Bytes(name), Directory: model.Bytes("/tmp")})
		links = append(links, model.Link{Session: id, Window: "w0", Index: 0, Active: true})
	}

	manifest := model.Manifest{
		Format:   "sesh",
		Version:  model.Schema,
		Started:  10,
		Finished: 20,
		Source:   model.Source{TmuxVersion: "3.8", LibraryVersion: "v0.3.0", Consistency: "observed"},
		Sessions: sessions,
		Windows: []model.Window{{
			ID: "w0", Name: model.Bytes("main"), Width: 80, Height: 24,
			Layout: model.Layout{Width: 80, Height: 24, Pane: "p0"},
		}},
		Links: links,
		Panes: []model.Pane{{
			ID: "p0", Window: "w0", Directory: model.Bytes("/tmp"), Width: 80, Height: 24, Active: true,
			Terminal: model.Terminal{Artifact: ""},
			Launch:   model.Launch{Recovery: "unverified"},
		}},
	}

	for _, domain := range []string{"S01", "S02", "S03", "S04", "S05", "S06", "S07", "S08", "S09", "S10"} {
		manifest.Evidence = append(manifest.Evidence, model.Evidence{Domain: domain, Status: "unverified", Reason: "not_captured"})
	}

	return manifest
}

func savedSessionNames(t *testing.T, repo *store.Repository, generation string) []string {
	t.Helper()

	manifest, err := repo.Read(t.Context(), generation)
	if err != nil {
		t.Fatalf("reading generation %s: %v", generation, err)
	}

	names := make([]string, 0, len(manifest.Sessions))
	for _, stored := range manifest.Sessions {
		names = append(names, string(stored.Name))
	}

	return names
}

func currentSavedSessionNames(t *testing.T, repo *store.Repository) []string {
	t.Helper()

	catalog, err := repo.Inspect(t.Context())
	if err != nil {
		t.Fatalf("inspecting repository: %v", err)
	}

	if catalog.Current == "" {
		t.Fatal("repository has no current generation")
	}

	return savedSessionNames(t, repo, catalog.Current)
}

func generationsContaining(t *testing.T, repo *store.Repository, name string) []string {
	t.Helper()

	catalog, err := repo.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	matching := make([]string, 0, len(catalog.Generations))

	for _, generation := range catalog.Generations {
		if slices.Contains(savedSessionNames(t, repo, generation.ID), name) {
			matching = append(matching, generation.ID)
		}
	}

	return matching
}

func generationRetired(t *testing.T, repo *store.Repository, generation string) bool {
	t.Helper()

	catalog, err := repo.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, candidate := range catalog.Generations {
		if candidate.ID == generation {
			return false
		}
	}

	return true
}

func (f *workflowFixture) historyIdentity(name string) session.Identity {
	return session.Identity{
		Origin:           "local",
		ConnectionID:     "",
		Socket:           f.server.Endpoint().SocketPath,
		RepositorySource: "",
		Name:             name,
	}
}

func (f *workflowFixture) recordedSwitch(t *testing.T, name string) int64 {
	t.Helper()

	state, err := session.LoadState(f.historyPath)
	if err != nil {
		t.Fatalf("loading history: %v", err)
	}

	return state.GetLastSwitchIdentity(f.historyIdentity(name))
}

func TestWorkflow_LiveSessionLifecycle(t *testing.T) {
	fixture := newWorkflowFixture(t)
	wf := fixture.workflow(nil, nil, config.HookConfig{})

	sess, err := wf.CreateSession(t.Context(), "alpha-session", "/tmp")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	if sess.ID() == "" {
		t.Fatal("expected non-empty session ID")
	}

	found, err := fixture.server.FindSession(t.Context(), "alpha-session")
	if err != nil {
		t.Fatalf("FindSession failed: %v", err)
	}

	info, err := found.Info(t.Context())
	if err != nil {
		t.Fatalf("found.Info failed: %v", err)
	}

	if info.Name != "alpha-session" {
		t.Fatalf("expected session name alpha-session, got %q", info.Name)
	}

	if _, err := wf.CreateSession(t.Context(), "alpha-session", "/tmp"); !errors.Is(err, session.ErrSessionAlreadyExists) {
		t.Fatalf("expected ErrSessionAlreadyExists, got %v", err)
	}

	if err := wf.RenameSession(t.Context(), "alpha-session", "beta-session"); err != nil {
		t.Fatalf("RenameSession failed: %v", err)
	}

	if _, err := fixture.server.FindSession(t.Context(), "alpha-session"); !errors.Is(err, gotmux.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for old session, got %v", err)
	}

	if _, err := fixture.server.FindSession(t.Context(), "beta-session"); err != nil {
		t.Fatalf("FindSession for renamed session failed: %v", err)
	}

	if err := wf.KillSession(t.Context(), "beta-session", false); err != nil {
		t.Fatalf("KillSession failed: %v", err)
	}

	if _, err := fixture.server.FindSession(t.Context(), "beta-session"); !errors.Is(err, gotmux.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for killed session, got %v", err)
	}
}

func TestWorkflow_RenameTargetRejectsReplacementIdentity(t *testing.T) {
	fixture := newWorkflowFixture(t)
	wf := fixture.workflow(nil, nil, config.HookConfig{})

	err := wf.RenameSessionTarget(t.Context(), "fixture", "renamed", "$replacement")
	if !errors.Is(err, session.ErrSessionReplaced) {
		t.Fatalf("RenameSessionTarget error = %v, want ErrSessionReplaced", err)
	}

	if _, err := fixture.server.FindSession(t.Context(), "fixture"); err != nil {
		t.Fatalf("replacement guard changed selected session: %v", err)
	}

	if _, err := fixture.server.FindSession(t.Context(), "renamed"); !errors.Is(err, gotmux.ErrNotFound) {
		t.Fatalf("replacement guard created rename target: %v", err)
	}
}

func TestWorkflow_KillTargetRejectsReplacementIdentity(t *testing.T) {
	fixture := newWorkflowFixture(t)
	wf := fixture.workflow(nil, nil, config.HookConfig{})

	err := wf.KillSessionTarget(t.Context(), "fixture", "$replacement", false)
	if !errors.Is(err, session.ErrSessionReplaced) {
		t.Fatalf("KillSessionTarget error = %v, want ErrSessionReplaced", err)
	}

	if _, err := fixture.server.FindSession(t.Context(), "fixture"); err != nil {
		t.Fatalf("replacement guard killed selected session: %v", err)
	}
}

func TestWorkflow_EnterOrCreateInsideTmuxCreatesAndSwitchesVerifiedClient(t *testing.T) {
	fixture := newWorkflowFixture(t)

	origin := fixture.fixtureSession(t)
	client := fixture.attachClient(t, origin.ID)

	wf := fixture.workflow(fixture.ambientEnvironment(t, origin), nil, config.HookConfig{})

	outcome, err := wf.EnterOrCreateSession(t.Context(), "created-session", "/tmp")
	if err != nil {
		t.Fatalf("EnterOrCreateSession failed: %v", err)
	}

	if !outcome.Created || outcome.Action != session.EnterSwitch || !outcome.Started {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}

	if outcome.Client != client {
		t.Fatalf("switch targeted client %q, want invoking client %q", outcome.Client, client)
	}

	if displayed := fixture.clientSession(t, client); string(displayed) != outcome.SessionID {
		t.Fatalf("client displays %s, want created session %s", displayed, outcome.SessionID)
	}

	if fixture.recordedSwitch(t, "created-session") == 0 {
		t.Fatal("verified switch was not recorded in history")
	}
}

func TestWorkflow_EnterExistingSessionInsideTmuxSwitchesWithoutCreating(t *testing.T) {
	fixture := newWorkflowFixture(t)

	origin := fixture.fixtureSession(t)
	target := fixture.newLiveSession(t, "existing-session")
	client := fixture.attachClient(t, origin.ID)

	wf := fixture.workflow(fixture.ambientEnvironment(t, origin), nil, config.HookConfig{})

	outcome, err := wf.EnterOrCreateSession(t.Context(), "existing-session", "/tmp")
	if err != nil {
		t.Fatalf("EnterOrCreateSession failed: %v", err)
	}

	if outcome.Created {
		t.Fatal("existing session reported as created")
	}

	if displayed := fixture.clientSession(t, client); displayed != target.ID {
		t.Fatalf("client displays %s, want %s", displayed, target.ID)
	}
}

func TestWorkflow_EnterExistingSessionOutsideTmuxAttaches(t *testing.T) {
	fixture := newWorkflowFixture(t)

	target := fixture.fixtureSession(t)

	master, slave := openTestPTY(t)
	_ = master

	wf := session.NewWorkflowWithOptions(session.WorkflowOptions{
		Server:      fixture.server,
		Repository:  nil,
		HistoryPath: fixture.historyPath,
		Hooks:       config.HookConfig{},
		HookRunner:  nil,
		Getenv:      func(string) string { return "" },
		Streams:     gotmux.Streams{In: slave, Out: slave, Err: slave},
		Source:      "test",
	})

	attachCtx, cancelAttach := context.WithCancel(t.Context())
	defer cancelAttach()

	results := make(chan session.EnterOutcome, 1)
	failures := make(chan error, 1)

	go func() {
		outcome, err := wf.EnterSession(attachCtx, "fixture")
		if err != nil {
			failures <- err

			return
		}

		results <- outcome
	}()

	client := fixture.waitForClient(t, target.ID)

	cancelAttach()

	select {
	case err := <-failures:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("attach failed with %v, want cancellation", err)
		}
	case outcome := <-results:
		t.Fatalf("attach returned %+v before the client detached", outcome)
	case <-time.After(testClientTimeout):
		t.Fatal("attach did not finish after cancellation")
	}

	waitForClientGone(t, fixture, target.ID)

	if client == "" {
		t.Fatal("attach did not start a client")
	}
}

func waitForClientGone(t *testing.T, fixture *workflowFixture, sessionID gotmux.SessionID) {
	t.Helper()

	deadline := time.Now().Add(testClientTimeout)

	for time.Now().Before(deadline) {
		found := false

		for _, client := range fixture.clients(t) {
			if displayed, ok := client.SessionID.Get(); ok && displayed == sessionID {
				found = true
			}
		}

		if !found {
			return
		}

		time.Sleep(testClientPoll)
	}

	t.Fatalf("client stayed attached to session %s after cancellation", sessionID)
}

func TestWorkflow_SwitchRefusesWithoutInvokingClient(t *testing.T) {
	fixture := newWorkflowFixture(t)

	origin := fixture.fixtureSession(t)
	target := fixture.newLiveSession(t, "switch-target")

	wf := fixture.workflow(fixture.ambientEnvironment(t, origin), nil, config.HookConfig{})

	if _, err := wf.SwitchSessionOutcome(t.Context(), "switch-target"); !errors.Is(err, session.ErrInvokingClientUnavailable) {
		t.Fatalf("SwitchSessionOutcome error = %v, want ErrInvokingClientUnavailable", err)
	}

	if _, err := fixture.server.FindSession(t.Context(), target.Name); err != nil {
		t.Fatalf("target session changed: %v", err)
	}
}

func TestWorkflow_SwitchRefusesOutsideTmux(t *testing.T) {
	fixture := newWorkflowFixture(t)

	fixture.newLiveSession(t, "switch-target")

	clients := fixture.clients(t)
	if len(clients) != 0 {
		t.Fatalf("fixture must have no clients, got %d", len(clients))
	}

	wf := fixture.workflow(func(string) string { return "" }, nil, config.HookConfig{})

	if _, err := wf.SwitchSessionOutcome(t.Context(), "switch-target"); !errors.Is(err, session.ErrNotInsideTmux) {
		t.Fatalf("SwitchSessionOutcome error = %v, want ErrNotInsideTmux", err)
	}
}

func TestWorkflow_SwitchRefusesAmbiguousInvokingClients(t *testing.T) {
	fixture := newWorkflowFixture(t)

	origin := fixture.fixtureSession(t)
	fixture.newLiveSession(t, "switch-target")

	first := fixture.attachClient(t, origin.ID)
	second := fixture.attachClient(t, origin.ID)

	wf := fixture.workflow(fixture.ambientEnvironment(t, origin), nil, config.HookConfig{})

	if _, err := wf.SwitchSessionOutcome(t.Context(), "switch-target"); !errors.Is(err, session.ErrInvokingClientAmbiguous) {
		t.Fatalf("SwitchSessionOutcome error = %v, want ErrInvokingClientAmbiguous", err)
	}

	for _, client := range []string{first, second} {
		if displayed := fixture.clientSession(t, client); displayed != origin.ID {
			t.Fatalf("client %s moved to %s despite refused ambiguity", client, displayed)
		}
	}
}

func TestWorkflow_SwitchMovesOnlyInvokingClient(t *testing.T) {
	fixture := newWorkflowFixture(t)

	origin := fixture.fixtureSession(t)
	target := fixture.newLiveSession(t, "switch-target")
	other := fixture.newLiveSession(t, "other-session")

	invoking := fixture.attachClient(t, origin.ID)
	bystander := fixture.attachClient(t, other.ID)

	wf := fixture.workflow(fixture.ambientEnvironment(t, origin), nil, config.HookConfig{})

	outcome, err := wf.SwitchSessionOutcome(t.Context(), "switch-target")
	if err != nil {
		t.Fatalf("SwitchSessionOutcome failed: %v", err)
	}

	if outcome.Client != invoking {
		t.Fatalf("switch targeted %q, want invoking client %q", outcome.Client, invoking)
	}

	if displayed := fixture.clientSession(t, invoking); displayed != target.ID {
		t.Fatalf("invoking client displays %s, want %s", displayed, target.ID)
	}

	if displayed := fixture.clientSession(t, bystander); displayed != other.ID {
		t.Fatalf("bystander client displays %s, want %s", displayed, other.ID)
	}
}

func TestWorkflow_SwitchWithDeferredHistoryLeavesRecordingToCaller(t *testing.T) {
	fixture := newWorkflowFixture(t)

	origin := fixture.fixtureSession(t)
	target := fixture.newLiveSession(t, "deferred-target")
	client := fixture.attachClient(t, origin.ID)

	wf := session.NewWorkflowWithOptions(session.WorkflowOptions{
		Server:             fixture.server,
		HistoryPath:        fixture.historyPath,
		Getenv:             fixture.ambientEnvironment(t, origin),
		Source:             "test",
		DeferSwitchHistory: true,
	})

	if _, err := wf.SwitchSessionOutcome(t.Context(), "deferred-target"); err != nil {
		t.Fatalf("SwitchSessionOutcome failed: %v", err)
	}

	if displayed := fixture.clientSession(t, client); displayed != target.ID {
		t.Fatalf("client displays %s, want %s", displayed, target.ID)
	}

	if recorded := fixture.recordedSwitch(t, "deferred-target"); recorded != 0 {
		t.Fatalf("deferred switch recorded history at %d; the caller owns recording", recorded)
	}

	identity, ok := wf.SwitchHistoryIdentity("deferred-target")
	if !ok || identity != fixture.historyIdentity("deferred-target") {
		t.Fatalf("switch history identity = %+v, %t; want %+v", identity, ok, fixture.historyIdentity("deferred-target"))
	}
}

func TestWorkflow_SwitchRefusesUnverifiedTargetSession(t *testing.T) {
	fixture := newWorkflowFixture(t)

	origin := fixture.fixtureSession(t)
	client := fixture.attachClient(t, origin.ID)

	wf := fixture.workflow(fixture.ambientEnvironment(t, origin), nil, config.HookConfig{})

	if _, err := wf.SwitchSessionOutcome(t.Context(), "missing-session"); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("SwitchSessionOutcome error = %v, want ErrSessionNotFound", err)
	}

	if displayed := fixture.clientSession(t, client); displayed != origin.ID {
		t.Fatalf("client moved to %s for a missing target", displayed)
	}
}

func TestWorkflow_SwitchCancellationPreventsMutation(t *testing.T) {
	fixture := newWorkflowFixture(t)

	origin := fixture.fixtureSession(t)
	fixture.newLiveSession(t, "switch-target")
	client := fixture.attachClient(t, origin.ID)

	wf := fixture.workflow(fixture.ambientEnvironment(t, origin), nil, config.HookConfig{})

	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := wf.SwitchSessionOutcome(canceled, "switch-target"); err == nil {
		t.Fatal("canceled switch reported success")
	}

	if displayed := fixture.clientSession(t, client); displayed != origin.ID {
		t.Fatalf("client moved to %s despite cancellation", displayed)
	}

	if fixture.recordedSwitch(t, "switch-target") != 0 {
		t.Fatal("canceled switch recorded history")
	}
}

func TestWorkflow_SwitchPreHookFailurePreventsSwitch(t *testing.T) {
	fixture := newWorkflowFixture(t)

	origin := fixture.fixtureSession(t)
	fixture.newLiveSession(t, "switch-target")
	client := fixture.attachClient(t, origin.ID)

	hooks := config.HookConfig{SessionSwitchPre: []config.HookCommand{{ShellCommand: "true", Timeout: "0s"}}}

	wf := fixture.workflow(fixture.ambientEnvironment(t, origin), nil, hooks)

	if _, err := wf.SwitchSessionOutcome(t.Context(), "switch-target"); !errors.Is(err, session.ErrHookTimeoutInvalid) {
		t.Fatalf("SwitchSessionOutcome error = %v, want hook timeout failure", err)
	}

	if displayed := fixture.clientSession(t, client); displayed != origin.ID {
		t.Fatalf("client moved to %s despite failed pre-hook boundary", displayed)
	}
}

func TestWorkflow_SwitchHooksRunAroundVerifiedSwitch(t *testing.T) {
	fixture := newWorkflowFixture(t)

	marker := filepath.Join(t.TempDir(), "hooks.log")
	t.Setenv("SESH_TEST_HOOK_MARKER", marker)

	origin := fixture.fixtureSession(t)
	fixture.newLiveSession(t, "switch-target")
	fixture.attachClient(t, origin.ID)

	hooks := config.HookConfig{
		SessionSwitchPre:  []config.HookCommand{{ShellCommand: "printf 'pre\\n' >> \"$SESH_TEST_HOOK_MARKER\""}},
		SessionSwitchPost: []config.HookCommand{{ShellCommand: "printf 'post\\n' >> \"$SESH_TEST_HOOK_MARKER\""}},
	}

	wf := fixture.workflow(fixture.ambientEnvironment(t, origin), nil, hooks)

	outcome, err := wf.SwitchSessionOutcome(t.Context(), "switch-target")
	if err != nil {
		t.Fatalf("SwitchSessionOutcome failed: %v", err)
	}

	if outcome.Hooks.Attempted != 2 || outcome.Hooks.Completed != 2 || outcome.Hooks.Failed != 0 {
		t.Fatalf("unexpected hook summary: %+v", outcome.Hooks)
	}

	recorded, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}

	if string(recorded) != "pre\npost\n" {
		t.Fatalf("hook order = %q, want pre before post", recorded)
	}
}

func TestWorkflow_SwitchForClientHookReceivesOriginSessionName(t *testing.T) {
	fixture := newWorkflowFixture(t)

	marker := filepath.Join(t.TempDir(), "hook-origin")
	t.Setenv("SESH_TEST_HOOK_MARKER", marker)

	origin := fixture.fixtureSession(t)
	fixture.newLiveSession(t, "switch-target")
	client := fixture.attachClient(t, origin.ID)

	hooks := config.HookConfig{SessionSwitchPre: []config.HookCommand{{
		ShellCommand: `printf '%s:%s' "$SESH_PREV_SESSION" "$SESH_PREV_SESSION_ID" > "$SESH_TEST_HOOK_MARKER"`,
	}}}
	wf := fixture.workflow(nil, nil, hooks)

	outcome, err := wf.SwitchSessionOutcomeForClient(t.Context(), "switch-target", client, string(origin.ID))
	if err != nil {
		t.Fatalf("SwitchSessionOutcomeForClient failed: %v", err)
	}

	if outcome.Client != client {
		t.Fatalf("switched client = %q, want %q", outcome.Client, client)
	}

	recorded, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := string(recorded), origin.Name+":"+string(origin.ID); got != want {
		t.Fatalf("previous session hook fields = %q, want %q", got, want)
	}
}

func TestWorkflow_SwitchPostHookFailureKeepsVerifiedSwitch(t *testing.T) {
	fixture := newWorkflowFixture(t)

	origin := fixture.fixtureSession(t)
	target := fixture.newLiveSession(t, "switch-target")
	client := fixture.attachClient(t, origin.ID)

	hooks := config.HookConfig{SessionSwitchPost: []config.HookCommand{{ShellCommand: "true", Timeout: "0s"}}}

	wf := fixture.workflow(fixture.ambientEnvironment(t, origin), nil, hooks)

	outcome, err := wf.SwitchSessionOutcome(t.Context(), "switch-target")
	if !errors.Is(err, session.ErrHookTimeoutInvalid) {
		t.Fatalf("SwitchSessionOutcome error = %v, want hook boundary failure", err)
	}

	if !outcome.Started {
		t.Fatal("verified switch was not reported as started")
	}

	if displayed := fixture.clientSession(t, client); displayed != target.ID {
		t.Fatalf("client displays %s, want switched session %s", displayed, target.ID)
	}
}

func TestWorkflow_AttachRequiresTerminalStreams(t *testing.T) {
	fixture := newWorkflowFixture(t)

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})

	wf := session.NewWorkflowWithOptions(session.WorkflowOptions{
		Server:      fixture.server,
		Repository:  nil,
		HistoryPath: fixture.historyPath,
		Hooks:       config.HookConfig{},
		HookRunner:  nil,
		Getenv:      func(string) string { return "" },
		Streams:     gotmux.Streams{In: reader, Out: writer, Err: writer},
		Source:      "test",
	})

	if _, err := wf.AttachSession(t.Context(), "fixture"); !errors.Is(err, session.ErrTerminalRequired) {
		t.Fatalf("AttachSession error = %v, want ErrTerminalRequired", err)
	}

	if clients := fixture.clients(t); len(clients) != 0 {
		t.Fatalf("attach reported no terminal but %d clients appeared", len(clients))
	}

	if _, err := fixture.server.FindSession(t.Context(), "fixture"); err != nil {
		t.Fatalf("target session changed: %v", err)
	}
}

func TestWorkflow_AttachPreHookFailurePreventsAttach(t *testing.T) {
	fixture := newWorkflowFixture(t)

	_, slave := openTestPTY(t)

	hooks := config.HookConfig{SessionAttachPre: []config.HookCommand{{ShellCommand: "true", Timeout: "0s"}}}

	wf := session.NewWorkflowWithOptions(session.WorkflowOptions{
		Server:      fixture.server,
		Repository:  nil,
		HistoryPath: fixture.historyPath,
		Hooks:       hooks,
		HookRunner:  nil,
		Getenv:      func(string) string { return "" },
		Streams:     gotmux.Streams{In: slave, Out: slave, Err: slave},
		Source:      "test",
	})

	if _, err := wf.AttachSession(t.Context(), "fixture"); !errors.Is(err, session.ErrHookTimeoutInvalid) {
		t.Fatalf("AttachSession error = %v, want hook boundary failure", err)
	}

	if clients := fixture.clients(t); len(clients) != 0 {
		t.Fatalf("attach ran despite failed pre-hook boundary: %d clients", len(clients))
	}

	if _, err := fixture.server.FindSession(t.Context(), "fixture"); err != nil {
		t.Fatalf("target session changed: %v", err)
	}
}

func TestWorkflow_AttachPostHookRunsAfterClientStart(t *testing.T) {
	fixture := newWorkflowFixture(t)

	target := fixture.fixtureSession(t)

	marker := filepath.Join(t.TempDir(), "attach-post")
	t.Setenv("SESH_TEST_HOOK_MARKER", marker)

	_, slave := openTestPTY(t)

	hooks := config.HookConfig{
		SessionAttachPost: []config.HookCommand{{ShellCommand: "printf 'post\\n' >> \"$SESH_TEST_HOOK_MARKER\""}},
	}

	wf := session.NewWorkflowWithOptions(session.WorkflowOptions{
		Server:      fixture.server,
		Repository:  nil,
		HistoryPath: fixture.historyPath,
		Hooks:       hooks,
		HookRunner:  nil,
		Getenv:      func(string) string { return "" },
		Streams:     gotmux.Streams{In: slave, Out: slave, Err: slave},
		Source:      "test",
	})

	attachCtx, cancelAttach := context.WithCancel(t.Context())
	defer cancelAttach()

	done := make(chan error, 1)

	go func() {
		_, err := wf.AttachSession(attachCtx, "fixture")
		done <- err
	}()

	fixture.waitForClient(t, target.ID)

	deadline := time.Now().Add(testClientTimeout)

	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}

		select {
		case err := <-done:
			t.Fatalf("attach returned %v before the post hook completed", err)
		default:
		}

		time.Sleep(testClientPoll)
	}

	if _, err := os.Stat(marker); err != nil {
		t.Fatal("attach post hook did not run while the client was attached")
	}

	cancelAttach()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("attach returned %v, want cancellation", err)
		}
	case <-time.After(testClientTimeout):
		t.Fatal("attach did not finish after cancellation")
	}
}

func TestWorkflow_DeleteRefusesPendingSaveBeforeMutation(t *testing.T) {
	fixture := newWorkflowFixture(t)
	repo := openTestRepository(t)

	if _, err := repo.Begin(t.Context()); err != nil {
		t.Fatal(err)
	}

	target := fixture.fixtureSession(t)

	wf := fixture.workflow(nil, repo, config.HookConfig{})

	_, _, err := wf.DeleteSession(t.Context(), "fixture", false)
	if !errors.Is(err, store.ErrRecoveryRequired) {
		t.Fatalf("DeleteSession error = %v, want ErrRecoveryRequired", err)
	}

	if _, err := fixture.server.FindSession(t.Context(), target.Name); err != nil {
		t.Fatalf("live session was destroyed by a refused deletion: %v", err)
	}
}

func TestWorkflow_DeleteRemovesEverySavedGeneration(t *testing.T) {
	fixture := newWorkflowFixture(t)
	repo := openTestRepository(t)

	fixture.newLiveSession(t, "keep-alive")

	first := commitSavedGeneration(t, repo, fixture.endpointScope(t), "fixture", "unrelated")
	second := commitSavedGeneration(t, repo, fixture.endpointScope(t), "fixture")
	third := commitSavedGeneration(t, repo, fixture.endpointScope(t), "fixture", "unrelated")

	wf := fixture.workflow(nil, repo, config.HookConfig{})

	outcome, err := wf.DeleteSessionEverywhere(t.Context(), "fixture", false)
	if err != nil {
		t.Fatalf("DeleteSessionEverywhere failed: %v", err)
	}

	if !outcome.LiveKilled || !outcome.SavedDeleted {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}

	if outcome.SavedGenerations != 3 {
		t.Fatalf("rewritten generations = %d, want 3", outcome.SavedGenerations)
	}

	if _, err := fixture.server.FindSession(t.Context(), "fixture"); !errors.Is(err, gotmux.ErrNotFound) {
		t.Fatalf("live session survived deletion: %v", err)
	}

	for _, generation := range []store.Generation{first, second, third} {
		if !generationRetired(t, repo, generation.ID) {
			t.Fatalf("source generation %s was not retired", generation.ID)
		}
	}

	if remaining := generationsContaining(t, repo, "fixture"); len(remaining) != 0 {
		t.Fatalf("generations still contain the deleted session: %v", remaining)
	}

	if remaining := generationsContaining(t, repo, "unrelated"); len(remaining) == 0 {
		t.Fatal("unrelated saved session was removed from every generation")
	}

	if names := currentSavedSessionNames(t, repo); !slices.Contains(names, "unrelated") {
		t.Fatalf("unrelated saved session was removed: %v", names)
	}
}

func TestWorkflow_DeleteOnlyRemovesSavedGenerationsForSelectedEndpoint(t *testing.T) {
	fixture := newWorkflowFixture(t)
	repo := openTestRepository(t)
	fixture.newLiveSession(t, "keep-alive")

	otherScope, err := model.EndpointScope(filepath.Join(t.TempDir(), "other.sock"))
	if err != nil {
		t.Fatalf("deriving other endpoint scope: %v", err)
	}

	other := commitSavedGeneration(t, repo, otherScope, "fixture")
	matching := commitSavedGeneration(t, repo, fixture.endpointScope(t), "fixture")
	wf := fixture.workflow(nil, repo, config.HookConfig{})

	outcome, err := wf.DeleteSessionEverywhere(t.Context(), "fixture", false)
	if err != nil {
		t.Fatalf("DeleteSessionEverywhere failed: %v", err)
	}

	if !outcome.LiveKilled || !outcome.SavedDeleted || outcome.SavedGenerations != 1 {
		t.Fatalf("unexpected endpoint-scoped outcome: %+v", outcome)
	}

	if generationRetired(t, repo, other.ID) {
		t.Fatal("saved generation from another endpoint was retired")
	}

	if names := savedSessionNames(t, repo, other.ID); !slices.Contains(names, "fixture") {
		t.Fatalf("saved generation from another endpoint was modified: %v", names)
	}

	if !generationRetired(t, repo, matching.ID) {
		t.Fatal("matching saved generation was not retired")
	}
}

func TestWorkflow_DeleteRefusesUnscopedSavedSessionBeforeMutation(t *testing.T) {
	fixture := newWorkflowFixture(t)
	repo := openTestRepository(t)
	gen := commitSavedGeneration(t, repo, "", "fixture")

	var output bytes.Buffer

	ctx := zerolog.New(&output).WithContext(t.Context())
	wf := fixture.workflow(nil, repo, config.HookConfig{})

	outcome, err := wf.DeleteSessionEverywhere(ctx, "fixture", false)
	if !errors.Is(err, session.ErrSavedSessionScopeConflict) {
		t.Fatalf("DeleteSessionEverywhere error = %v, want ErrSavedSessionScopeConflict", err)
	}

	if outcome.LiveKilled || outcome.SavedDeleted || !outcome.SavedFound {
		t.Fatalf("unscoped deletion reported unexpected work: %+v", outcome)
	}

	if _, err := fixture.server.FindSession(t.Context(), "fixture"); err != nil {
		t.Fatalf("live session changed after unscoped refusal: %v", err)
	}

	if names := savedSessionNames(t, repo, gen.ID); !slices.Contains(names, "fixture") {
		t.Fatalf("unscoped saved generation was modified: %v", names)
	}

	assertSavedSessionScopeConflictLog(t, &output, gen.ID)
}

func TestWorkflow_DeleteSavedTargetReturnsDurableReceiptAndPreservesLiveSession(t *testing.T) {
	fixture := newWorkflowFixture(t)
	repo := openTestRepository(t)
	generation := commitSavedGeneration(t, repo, fixture.endpointScope(t), "fixture", "archive")
	live := fixture.fixtureSession(t)
	wf := fixture.workflow(nil, repo, config.HookConfig{})

	var output bytes.Buffer

	ctx := zerolog.New(&output).WithContext(t.Context())

	rewriteID, err := wf.DeleteSavedSessionTarget(ctx, generation.ID, "s0")
	if err != nil {
		t.Fatalf("DeleteSavedSessionTarget failed: %v", err)
	}

	if rewriteID == "" {
		t.Fatal("DeleteSavedSessionTarget returned an empty durable receipt")
	}

	reconciled, err := repo.ReconcileRewrite(t.Context(), rewriteID)
	if err != nil {
		t.Fatalf("reconciling saved deletion receipt: %v", err)
	}

	if reconciled.ID != rewriteID || !slices.Equal(reconciled.Removed, []string{"s0"}) {
		t.Fatalf("reconciled rewrite = %+v", reconciled)
	}

	current, err := fixture.server.FindSession(t.Context(), "fixture")
	if err != nil {
		t.Fatalf("same-name live session was removed: %v", err)
	}

	if current.ID() != live.ID {
		t.Fatalf("live identity changed: got %s, want %s", current.ID(), live.ID)
	}

	if names := currentSavedSessionNames(t, repo); slices.Contains(names, "fixture") || !slices.Contains(names, "archive") {
		t.Fatalf("saved survivor names = %v", names)
	}

	assertSavedDeleteSuccessLog(t, &output, generation.ID, rewriteID)
}

func TestWorkflow_DeleteSavedTargetPreservesSameNameLiveReplacement(t *testing.T) {
	fixture := newWorkflowFixture(t)
	repo := openTestRepository(t)
	generation := commitSavedGeneration(t, repo, fixture.endpointScope(t), "fixture")
	fixture.newLiveSession(t, "keep-alive")

	original, err := fixture.server.FindSession(t.Context(), "fixture")
	if err != nil {
		t.Fatal(err)
	}

	if err := original.Kill(t.Context()); err != nil {
		t.Fatalf("killing original live session: %v", err)
	}

	replacement, err := fixture.server.NewSession(t.Context(), gotmux.NewSessionOptions{Name: "fixture"})
	if err != nil {
		t.Fatalf("creating replacement live session: %v", err)
	}

	wf := fixture.workflow(nil, repo, config.HookConfig{})
	if _, err := wf.DeleteSavedSessionTarget(t.Context(), generation.ID, "s0"); err != nil {
		t.Fatalf("DeleteSavedSessionTarget failed: %v", err)
	}

	current, err := fixture.server.FindSession(t.Context(), "fixture")
	if err != nil {
		t.Fatalf("same-name replacement was removed: %v", err)
	}

	if current.ID() != replacement.ID() {
		t.Fatalf("replacement identity changed: got %s, want %s", current.ID(), replacement.ID())
	}
}

func TestWorkflow_DeleteSavedTargetRejectsWrongSessionAndEndpoint(t *testing.T) {
	fixture := newWorkflowFixture(t)
	repo := openTestRepository(t)
	matching := commitSavedGeneration(t, repo, fixture.endpointScope(t), "fixture")

	otherScope, err := model.EndpointScope(filepath.Join(t.TempDir(), "other.sock"))
	if err != nil {
		t.Fatal(err)
	}

	other := commitSavedGeneration(t, repo, otherScope, "fixture")
	wf := fixture.workflow(nil, repo, config.HookConfig{})

	if receipt, err := wf.DeleteSavedSessionTarget(t.Context(), matching.ID, "missing"); receipt != "" || !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("wrong saved identity = %q, %v", receipt, err)
	}

	if receipt, err := wf.DeleteSavedSessionTarget(t.Context(), other.ID, "s0"); receipt != "" || !errors.Is(err, session.ErrSavedSessionScopeConflict) {
		t.Fatalf("other endpoint identity = %q, %v", receipt, err)
	}

	if names := savedSessionNames(t, repo, matching.ID); !slices.Contains(names, "fixture") {
		t.Fatalf("wrong identity modified matching generation: %v", names)
	}

	if names := savedSessionNames(t, repo, other.ID); !slices.Contains(names, "fixture") {
		t.Fatalf("scope mismatch modified other generation: %v", names)
	}

	if _, err := fixture.server.FindSession(t.Context(), "fixture"); err != nil {
		t.Fatalf("saved-only refusals changed live session: %v", err)
	}
}

func assertSavedSessionScopeConflictLog(t *testing.T, output *bytes.Buffer, generationID string) {
	t.Helper()

	records := decodeWorkflowLogRecords(t, output)
	if len(records) != 1 {
		t.Fatalf("record count = %d, want 1: %v", len(records), records)
	}

	record := records[0]
	if record["component"] != "session" || record["operation"] != "delete" ||
		record["status"] != "failed" || record["level"] != "error" ||
		record["reason"] != "saved_session_scope_conflict" {
		t.Fatalf("scope-conflict record = %#v", record)
	}

	if strings.Contains(output.String(), "fixture") || strings.Contains(output.String(), generationID) {
		t.Fatalf("scope-conflict log leaked saved identities: %s", output.String())
	}
}

func assertSavedDeleteSuccessLog(t *testing.T, output *bytes.Buffer, privateValues ...string) {
	t.Helper()

	records := decodeWorkflowLogRecords(t, output)
	if len(records) != 1 {
		t.Fatalf("record count = %d, want 1: %v", len(records), records)
	}

	record := records[0]
	if record["component"] != "session" || record["operation"] != "delete_saved" ||
		record["status"] != "ok" || record["level"] != "info" {
		t.Fatalf("saved-delete record = %#v", record)
	}

	for _, privateValue := range append(privateValues, "fixture", "archive", "s0") {
		if strings.Contains(output.String(), privateValue) {
			t.Fatalf("saved-delete log leaked private value: %s", output.String())
		}
	}
}

func TestWorkflow_DeletePreservesProtectedGeneration(t *testing.T) {
	fixture := newWorkflowFixture(t)
	repo := openTestRepository(t)

	generation := commitSavedGeneration(t, repo, fixture.endpointScope(t), "fixture")

	if _, err := repo.Pin(t.Context(), generation.ID, model.Bytes("restore-target"), nil); err != nil {
		t.Fatalf("pinning generation: %v", err)
	}

	target := fixture.fixtureSession(t)

	wf := fixture.workflow(nil, repo, config.HookConfig{})

	outcome, err := wf.DeleteSessionEverywhere(t.Context(), "fixture", false)
	if !errors.Is(err, session.ErrSavedGenerationsProtected) {
		t.Fatalf("DeleteSessionEverywhere error = %v, want ErrSavedGenerationsProtected", err)
	}

	if outcome.LiveKilled || outcome.SavedDeleted {
		t.Fatalf("protected deletion reported work: %+v", outcome)
	}

	if _, err := fixture.server.FindSession(t.Context(), target.Name); err != nil {
		t.Fatalf("live session was destroyed despite protected generations: %v", err)
	}

	if names := savedSessionNames(t, repo, generation.ID); !slices.Contains(names, "fixture") {
		t.Fatalf("pinned generation was modified: %v", names)
	}
}

func TestWorkflow_DeleteRefusesAttachedSessionUnlessForced(t *testing.T) {
	fixture := newWorkflowFixture(t)
	repo := openTestRepository(t)

	origin := fixture.fixtureSession(t)
	fixture.newLiveSession(t, "unrelated-session")

	commitSavedGeneration(t, repo, fixture.endpointScope(t), "fixture")

	fixture.attachClient(t, origin.ID)

	wf := fixture.workflow(nil, repo, config.HookConfig{})

	if _, err := wf.DeleteSessionEverywhere(t.Context(), "fixture", false); !errors.Is(err, session.ErrSessionAttached) {
		t.Fatalf("DeleteSessionEverywhere error = %v, want ErrSessionAttached", err)
	}

	if _, err := fixture.server.FindSession(t.Context(), "fixture"); err != nil {
		t.Fatalf("attached session was killed without force: %v", err)
	}

	if names := currentSavedSessionNames(t, repo); !slices.Contains(names, "fixture") {
		t.Fatalf("saved session was removed without force: %v", names)
	}

	outcome, err := wf.DeleteSessionEverywhere(t.Context(), "fixture", true)
	if err != nil {
		t.Fatalf("forced DeleteSessionEverywhere failed: %v", err)
	}

	if !outcome.LiveKilled || !outcome.SavedDeleted {
		t.Fatalf("forced deletion outcome: %+v", outcome)
	}

	if _, err := fixture.server.FindSession(t.Context(), "unrelated-session"); err != nil {
		t.Fatalf("unrelated live session was destroyed: %v", err)
	}
}

func TestWorkflow_DeleteSavedOnlySession(t *testing.T) {
	fixture := newWorkflowFixture(t)
	repo := openTestRepository(t)

	commitSavedGeneration(t, repo, fixture.endpointScope(t), "archive", "fixture")

	wf := fixture.workflow(nil, repo, config.HookConfig{})

	outcome, err := wf.DeleteSessionEverywhere(t.Context(), "archive", false)
	if err != nil {
		t.Fatalf("DeleteSessionEverywhere failed: %v", err)
	}

	if outcome.LivePresent || outcome.LiveKilled || !outcome.SavedDeleted {
		t.Fatalf("unexpected saved-only outcome: %+v", outcome)
	}

	if names := currentSavedSessionNames(t, repo); slices.Contains(names, "archive") {
		t.Fatalf("saved-only session survived deletion: %v", names)
	}

	if names := currentSavedSessionNames(t, repo); !slices.Contains(names, "fixture") {
		t.Fatalf("unrelated saved session was removed: %v", names)
	}
}

func TestWorkflow_DeleteMissingSession(t *testing.T) {
	fixture := newWorkflowFixture(t)
	repo := openTestRepository(t)

	wf := fixture.workflow(nil, repo, config.HookConfig{})

	if _, err := wf.DeleteSessionEverywhere(t.Context(), "missing-session", false); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("DeleteSessionEverywhere error = %v, want ErrSessionNotFound", err)
	}
}

func TestWorkflow_DeleteCleansHistoryAfterCompleteDeletion(t *testing.T) {
	fixture := newWorkflowFixture(t)

	target := fixture.fixtureSession(t)

	origin := fixture.attachClient(t, target.ID)

	wf := fixture.workflow(fixture.ambientEnvironment(t, target), nil, config.HookConfig{})

	fixture.newLiveSession(t, "history-target")

	if _, err := wf.SwitchSessionOutcome(t.Context(), "history-target"); err != nil {
		t.Fatalf("SwitchSessionOutcome failed: %v", err)
	}

	if fixture.recordedSwitch(t, "history-target") == 0 {
		t.Fatal("switch was not recorded")
	}

	if _, err := wf.DeleteSessionEverywhere(t.Context(), "history-target", true); err != nil {
		t.Fatalf("DeleteSessionEverywhere failed: %v", err)
	}

	if fixture.recordedSwitch(t, "history-target") != 0 {
		t.Fatal("history entry survived complete deletion")
	}

	if _, err := fixture.server.FindSession(t.Context(), "history-target"); !errors.Is(err, gotmux.ErrNotFound) {
		t.Fatalf("deleted session survived: %v", err)
	}

	if _, err := fixture.server.FindSession(t.Context(), "fixture"); err != nil {
		t.Fatalf("invoking session changed: %v", err)
	}

	if origin == "" {
		t.Fatal("fixture client was not attached")
	}
}

func TestWorkflow_RenamePropagatesHistoryFailure(t *testing.T) {
	fixture := newWorkflowFixture(t)

	fixture.newLiveSession(t, "rename-source")

	unsafe := filepath.Join(t.TempDir(), "unsafe-history")
	if err := os.Mkdir(unsafe, 0o755); err != nil {
		t.Fatal(err)
	}

	wf := session.NewWorkflowWithOptions(session.WorkflowOptions{
		Server:      fixture.server,
		Repository:  nil,
		HistoryPath: filepath.Join(unsafe, "history.json"),
		Hooks:       config.HookConfig{},
		HookRunner:  nil,
		Getenv:      nil,
		Streams:     gotmux.Streams{},
		Source:      "test",
	})

	outcome, err := wf.RenameSessionOutcome(t.Context(), "rename-source", "rename-target")
	if err == nil {
		t.Fatal("RenameSessionOutcome succeeded with an unusable history path")
	}

	if !outcome.Renamed {
		t.Fatalf("server rename was not reported: %+v", outcome)
	}

	if _, err := fixture.server.FindSession(t.Context(), "rename-target"); err != nil {
		t.Fatalf("server rename did not happen: %v", err)
	}

	if !strings.Contains(err.Error(), "history") {
		t.Fatalf("error %v does not identify the history boundary", err)
	}
}

func decodeWorkflowLogRecords(t *testing.T, output *bytes.Buffer) []map[string]any {
	t.Helper()

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")

	records := make([]map[string]any, 0, len(lines))

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		var record map[string]any

		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decoding log line %q: %v", line, err)
		}

		records = append(records, record)
	}

	return records
}

func TestWorkflow_LoggingRecordsOneOutcomePerOperation(t *testing.T) {
	fixture := newWorkflowFixture(t)

	origin := fixture.fixtureSession(t)
	fixture.newLiveSession(t, "switch-target")

	client := fixture.attachClient(t, origin.ID)
	if client == "" {
		t.Fatal("fixture client was not attached")
	}

	var output bytes.Buffer

	ctx := zerolog.New(&output).WithContext(t.Context())

	wf := fixture.workflow(fixture.ambientEnvironment(t, origin), nil, config.HookConfig{})

	if _, err := wf.SwitchSessionOutcome(ctx, "switch-target"); err != nil {
		t.Fatalf("SwitchSessionOutcome failed: %v", err)
	}

	records := decodeWorkflowLogRecords(t, &output)
	if len(records) != 1 {
		t.Fatalf("record count = %d, want 1: %v", len(records), records)
	}

	record := records[0]
	if record["component"] != "session" || record["operation"] != "switch" ||
		record["status"] != "ok" || record["level"] != "info" {
		t.Fatalf("switch record = %#v", record)
	}

	if strings.Contains(output.String(), "switch-target") || strings.Contains(output.String(), "fixture") {
		t.Fatalf("switch log leaked session identities: %s", output.String())
	}
}

func TestWorkflow_LoggingRecordsOneFailureWithoutIdentities(t *testing.T) {
	fixture := newWorkflowFixture(t)

	origin := fixture.fixtureSession(t)
	fixture.newLiveSession(t, "switch-target")

	var output bytes.Buffer

	ctx := zerolog.New(&output).WithContext(t.Context())

	wf := fixture.workflow(fixture.ambientEnvironment(t, origin), nil, config.HookConfig{})

	_, err := wf.SwitchSessionOutcome(ctx, "switch-target")
	if !errors.Is(err, session.ErrInvokingClientUnavailable) {
		t.Fatalf("SwitchSessionOutcome error = %v, want ErrInvokingClientUnavailable", err)
	}

	assertLoggedOutcomePreservesCode(t, err, "client_unavailable")

	records := decodeWorkflowLogRecords(t, &output)
	if len(records) != 1 {
		t.Fatalf("record count = %d, want 1: %v", len(records), records)
	}

	record := records[0]
	if record["component"] != "session" || record["operation"] != "switch" ||
		record["status"] != "failed" || record["level"] != "error" ||
		record["reason"] != "client_unavailable" {
		t.Fatalf("failure record = %#v", record)
	}

	if strings.Contains(output.String(), "switch-target") || strings.Contains(output.String(), "fixture") {
		t.Fatalf("failure log leaked session identities: %s", output.String())
	}
}

func assertLoggedOutcomePreservesCode(t *testing.T, err error, code string) {
	t.Helper()

	marker, ok := err.(interface{ OutcomeLogged() bool })
	if !ok || !marker.OutcomeLogged() {
		t.Fatalf("workflow error does not report owned logging: %T", err)
	}

	structured, ok := oops.AsOops(err)
	if !ok || structured.Code() != code {
		t.Fatalf("workflow error lost structured code %q: %#v", code, structured)
	}
}

func TestWorkflow_LoggingKeepsCreateInsideEnterSilent(t *testing.T) {
	fixture := newWorkflowFixture(t)

	origin := fixture.fixtureSession(t)

	fixture.attachClient(t, origin.ID)

	var output bytes.Buffer

	ctx := zerolog.New(&output).WithContext(t.Context())

	wf := fixture.workflow(fixture.ambientEnvironment(t, origin), nil, config.HookConfig{})

	outcome, err := wf.EnterOrCreateSession(ctx, "created-session", "/tmp")
	if err != nil {
		t.Fatalf("EnterOrCreateSession failed: %v", err)
	}

	if !outcome.Created {
		t.Fatal("session was not created")
	}

	records := decodeWorkflowLogRecords(t, &output)
	if len(records) != 1 {
		t.Fatalf("record count = %d, want one entry outcome: %v", len(records), records)
	}

	record := records[0]
	if record["operation"] != "switch" || record["status"] != "ok" {
		t.Fatalf("entry record = %#v", record)
	}

	if strings.Contains(output.String(), "created-session") {
		t.Fatalf("entry log leaked the created session name: %s", output.String())
	}
}

func TestWorkflow_DeleteEndpointLeaseExcludesConcurrentOperation(t *testing.T) {
	fixture := newWorkflowFixture(t)
	repo := openTestRepository(t)

	leaseRoot := filepath.Join(t.TempDir(), "leases")

	heldLease, err := tmuxserver.AcquireLease(t.Context(), leaseRoot, fixture.server.Endpoint().SocketPath)
	if err != nil {
		t.Fatalf("acquiring helper lease: %v", err)
	}

	t.Cleanup(func() {
		_ = heldLease.Close()
	})

	wf := session.NewWorkflowWithOptions(session.WorkflowOptions{
		Server:      fixture.server,
		Repository:  repo,
		LeaseRoot:   leaseRoot,
		HistoryPath: fixture.historyPath,
		Hooks:       config.HookConfig{},
		HookRunner:  nil,
		Getenv:      nil,
		Streams:     gotmux.Streams{In: nil, Out: nil, Err: nil},
		Source:      "test",
	})

	timeoutCtx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	_, err = wf.DeleteSessionEverywhere(timeoutCtx, "fixture", false)
	if err == nil {
		t.Fatal("DeleteSessionEverywhere succeeded while endpoint lease was held")
	}

	if _, findErr := fixture.server.FindSession(t.Context(), "fixture"); findErr != nil {
		t.Fatalf("live session was killed despite lease failure: %v", findErr)
	}
}

func TestWorkflow_DeleteSavedStateRequiresSharedEndpointLease(t *testing.T) {
	fixture := newWorkflowFixture(t)
	repo := openTestRepository(t)
	gen := commitSavedGeneration(t, repo, fixture.endpointScope(t), "fixture")

	wf := session.NewWorkflowWithOptions(session.WorkflowOptions{
		Server:      fixture.server,
		Repository:  repo,
		LeaseRoot:   "",
		HistoryPath: fixture.historyPath,
		Hooks:       config.HookConfig{},
		Source:      "test",
	})

	outcome, err := wf.DeleteSessionEverywhere(t.Context(), "fixture", false)
	if !errors.Is(err, tmuxserver.ErrLeaseRoot) {
		t.Fatalf("DeleteSessionEverywhere error = %v, want ErrLeaseRoot", err)
	}

	if outcome.LiveKilled || outcome.SavedDeleted {
		t.Fatalf("unleased deletion reported work: %+v", outcome)
	}

	if _, err := fixture.server.FindSession(t.Context(), "fixture"); err != nil {
		t.Fatalf("live session changed after refused unleased deletion: %v", err)
	}

	if _, err := repo.Read(t.Context(), gen.ID); err != nil {
		t.Fatalf("saved generation changed after refused unleased deletion: %v", err)
	}
}

func TestWorkflow_DeleteExpectedSessionIdentityRejectsReplacementBeforeSavedMutation(t *testing.T) {
	fixture := newWorkflowFixture(t)
	repo := openTestRepository(t)
	gen := commitSavedGeneration(t, repo, fixture.endpointScope(t), "fixture")
	wf := fixture.workflow(nil, repo, config.HookConfig{})

	outcome, err := wf.DeleteSessionTargetEverywhere(t.Context(), "fixture", "$replacement", false)
	if !errors.Is(err, session.ErrSessionReplaced) {
		t.Fatalf("DeleteSessionTargetEverywhere error = %v, want ErrSessionReplaced", err)
	}

	if outcome.LiveKilled || outcome.SavedDeleted || outcome.SavedFound {
		t.Fatalf("stale target caused mutation or saved planning: %+v", outcome)
	}

	if _, err := fixture.server.FindSession(t.Context(), "fixture"); err != nil {
		t.Fatalf("replacement guard changed live session: %v", err)
	}

	if _, err := repo.Read(t.Context(), gen.ID); err != nil {
		t.Fatalf("replacement guard changed saved generation: %v", err)
	}
}

func TestWorkflow_DeleteRevalidatesAttachedClientBeforeLiveKill(t *testing.T) {
	fixture := newWorkflowFixture(t)
	repo := openTestRepository(t)

	target := fixture.fixtureSession(t)
	fixture.newLiveSession(t, "keep-alive")

	commitSavedGeneration(t, repo, fixture.endpointScope(t), "fixture")

	fixture.attachClient(t, target.ID)

	wf := session.NewWorkflowWithOptions(session.WorkflowOptions{
		Server:      fixture.server,
		Repository:  repo,
		LeaseRoot:   fixture.leaseRoot,
		HistoryPath: fixture.historyPath,
		Hooks:       config.HookConfig{},
		HookRunner:  nil,
		Getenv:      nil,
		Streams:     gotmux.Streams{In: nil, Out: nil, Err: nil},
		Source:      "test",
	})

	outcome, err := wf.DeleteSessionEverywhere(t.Context(), "fixture", false)
	if !errors.Is(err, session.ErrSessionAttached) {
		t.Fatalf("DeleteSessionEverywhere error = %v, want ErrSessionAttached", err)
	}

	if outcome.LiveKilled {
		t.Fatal("live session was killed despite attached client")
	}

	if _, findErr := fixture.server.FindSession(t.Context(), "fixture"); findErr != nil {
		t.Fatalf("live session was destroyed: %v", findErr)
	}
}

func TestWorkflow_DeleteCancellationBeforeLeasePreservesLiveAndSavedState(t *testing.T) {
	fixture := newWorkflowFixture(t)
	repo := openTestRepository(t)

	gen := commitSavedGeneration(t, repo, fixture.endpointScope(t), "fixture")

	if _, err := repo.Pin(t.Context(), gen.ID, model.Bytes("restore-target"), nil); err != nil {
		t.Fatal(err)
	}

	wf := session.NewWorkflowWithOptions(session.WorkflowOptions{
		Server:      fixture.server,
		Repository:  repo,
		LeaseRoot:   fixture.leaseRoot,
		HistoryPath: fixture.historyPath,
		Hooks:       config.HookConfig{},
		HookRunner:  nil,
		Getenv:      nil,
		Streams:     gotmux.Streams{In: nil, Out: nil, Err: nil},
		Source:      "test",
	})

	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()

	outcome, err := wf.DeleteSessionEverywhere(canceledCtx, "fixture", false)
	if err == nil {
		t.Fatal("DeleteSessionEverywhere succeeded on a protected generation with canceled context")
	}

	if outcome.LiveKilled || outcome.SavedDeleted {
		t.Fatalf("work was reported for a refused deletion: %+v", outcome)
	}

	if _, findErr := fixture.server.FindSession(t.Context(), "fixture"); findErr != nil {
		t.Fatalf("live session was killed despite protected source: %v", findErr)
	}
}
