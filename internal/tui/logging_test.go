package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/rs/zerolog"
	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/fields"
	"github.com/zigai/sesh/internal/session"
)

const loggingTestPrivacySentinel = "TOP_SECRET_TUI_CONTEXT_VALUE"

var (
	errLoggingTestActionFailure    = errors.New(loggingTestPrivacySentinel)
	errLoggingTestCatalogFailure   = errors.New("TOP_SECRET_CATALOG_LOAD_ERROR")
	errLoggingTestAgentFailure     = errors.New("TOP_SECRET_AGENT_LOAD_ERROR")
	errLoggingTestRemoteFailure    = errors.New("TOP_SECRET_REMOTE_LOAD_ERROR")
	errLoggingTestWorkingDirectory = errors.New("TOP_SECRET_CWD_ERROR")
	errLoggingTestStateSaveFailure = errors.New("TOP_SECRET_STATE_ERROR")
	errLoggingTestSecretDetails    = errors.New("secret details")
	errLoggingTestRandomFailure    = errors.New("random failure")
)

type loggingTestContextKey struct{}

func loggingTestContext(output *bytes.Buffer) context.Context {
	logger := zerolog.New(output).Level(zerolog.InfoLevel).With().
		Str("command", "tui.test").
		Str("version", "test-version").
		Str("commit", "test-commit").
		Int("pid", 4242).
		Logger()

	return logger.WithContext(context.WithValue(context.Background(), loggingTestContextKey{}, loggingTestPrivacySentinel))
}

func loggingTestRecords(t *testing.T, output *bytes.Buffer) []map[string]any {
	t.Helper()

	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	records := make([]map[string]any, 0)

	for {
		var record map[string]any
		if err := decoder.Decode(&record); errors.Is(err, io.EOF) {
			return records
		} else if err != nil {
			t.Fatalf("decode TUI log record: %v", err)
		}

		if record["component"] == "tui" {
			records = append(records, record)
		}
	}
}

func loggingTestModel(t *testing.T, output *bytes.Buffer) *Model {
	t.Helper()

	deps := Dependencies{
		LoadCatalog: func(context.Context) (session.CatalogResult, error) {
			return session.CatalogResult{Status: session.StatusComplete}, nil
		},
		TmuxSessionExists:  func(context.Context, string) (bool, error) { return false, nil },
		TmuxCreateSession:  func(context.Context, string, string) error { return nil },
		TmuxSwitchSession:  func(context.Context, string) error { return nil },
		TmuxActivatePane:   func(context.Context, string, string) error { return nil },
		TmuxRenameSession:  func(context.Context, string, string) error { return nil },
		TmuxKillSession:    func(context.Context, string) error { return nil },
		TmuxIsInsideTmux:   func() bool { return true },
		LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) { return nil, nil },
		DeleteSavedSession: func(context.Context, string) (int, error) { return 1, nil },
		RestoreSavedSession: func(context.Context, string) error {
			return nil
		},
		GetWorkingDirectory: func() (string, error) { return "/test", nil },
		SaveModelState:      func(*session.State) error { return nil },
	}

	model, err := newModelWithTestContext(
		loggingTestContext(output),
		config.DefaultConfig(),
		testState(),
		deps,
	)
	if err != nil {
		t.Fatalf("newModelWithTestContext() error = %v", err)
	}

	t.Cleanup(model.Close)

	return model
}

func TestCustomFieldFailureLoggingIsPrivateAndIsolated(t *testing.T) {
	var logs bytes.Buffer

	model := customFieldTestModel(t, "{session_name} {custom.probe}", config.FieldCommands{
		"probe": shellFieldCommand("printf private-stdout; printf private-stderr >&2; exit 3"),
	})
	model.logger = *zerolog.Ctx(loggingTestContext(&logs))
	row := liveTestSession("$1", "private-session")
	model.SetInitialSessions([]session.Session{row})
	results := collectCustomFieldResults(model.syncCustomFields())

	captured := loggingTestWithCapturedStdStreams(t, func() {
		if _, err := fmt.Fprint(os.Stdout, "{\"value\":\"public\"}\n"); err != nil {
			t.Fatal(err)
		}

		for _, result := range results {
			model.Update(result)
		}
	})
	if captured != "{\"value\":\"public\"}\n" {
		t.Fatalf("stdout/stderr JSON changed: %q", captured)
	}

	records := loggingTestRecords(t, &logs)

	want := map[string]any{
		"command": "tui.test", "version": "test-version", "commit": "test-commit", "pid": float64(4242),
		"component": "tui", "operation": "custom_field_run", "level": zerolog.ErrorLevel.String(), "status": "failed",
		"reason": "command_failed", "error_code": "operation_failed", "exit_code": float64(3),
		"message": "custom field command failed",
	}
	if len(records) != 1 || !maps.Equal(records[0], want) {
		t.Fatalf("failure records = %#v, want exactly %#v", records, want)
	}

	for _, secret := range []string{"private-stdout", "private-stderr", "private-session", loggingTestPrivacySentinel} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("logs contain private value %q", secret)
		}
	}
}

func TestCustomFieldCacheFailureIsRetainedAndCloseFlushesAcceptedValue(t *testing.T) {
	var logs bytes.Buffer

	model := customFieldTestModel(t, "{session_name} {custom.blocked}", config.FieldCommands{
		"blocked": shellFieldCommand("printf private-value"),
	})
	model.logger = *zerolog.Ctx(loggingTestContext(&logs))

	path := fields.CachePath("blocked")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}

	row := liveTestSession("$1", "alpha")
	model.SetInitialSessions([]session.Session{row})

	captured := loggingTestWithCapturedStdStreams(t, func() {
		runCustomFieldCommands(t, model, model.syncCustomFields())
	})
	if captured != "" {
		t.Fatalf("cache failure wrote stdout/stderr: %q", captured)
	}

	records := loggingTestRecords(t, &logs)

	want := map[string]any{
		"command": "tui.test", "version": "test-version", "commit": "test-commit", "pid": float64(4242),
		"component": "tui", "operation": "custom_field_cache_write", "level": zerolog.ErrorLevel.String(), "status": "failed",
		"reason": "cache_write_failed", "error_code": "operation_failed",
		"message": "custom field cache write failed",
	}
	if len(records) != 1 || !maps.Equal(records[0], want) {
		t.Fatalf("cache failure records = %#v, want exactly %#v", records, want)
	}

	for _, secret := range []string{"private-value", path, filepath.Dir(path), loggingTestPrivacySentinel} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("cache failure logs contain private value %q", secret)
		}
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	model.Close()

	entries, err := fields.ReadCache(path)
	if err != nil || entries[customFieldSessionRowKey(row)].Text != "private-value" {
		t.Fatalf("accepted value after close flush = %#v, %v", entries, err)
	}

	if len(loggingTestRecords(t, &logs)) != 1 {
		t.Fatalf("successful flush logged another failure: %s", logs.String())
	}
}

func TestCustomFieldCanceledSuccessfulAndStaleResultsAreSilent(t *testing.T) {
	var logs bytes.Buffer

	model := customFieldTestModel(t, "{session_name} {custom.quiet}", config.FieldCommands{
		"quiet": shellFieldCommand("printf ok"),
	})
	model.logger = *zerolog.Ctx(loggingTestContext(&logs))
	row := liveTestSession("$1", "alpha")
	model.SetInitialSessions([]session.Session{row})
	result := collectCustomFieldResults(model.syncCustomFields())[0]
	result.result = fields.Result{Err: context.Canceled, Stderr: loggingTestPrivacySentinel, ExitCode: -1}
	model.Update(result)
	result.request++
	result.result.Err = errLoggingTestActionFailure
	model.Update(result)

	row.WindowCount++
	model.SetInitialSessions([]session.Session{row})
	runCustomFieldCommands(t, model, model.syncCustomFields())

	if logs.Len() != 0 {
		t.Fatalf("silent branches logged: %s", logs.String())
	}
}

func TestOriginSessionLoadsLogDegradationAndRecoveryAcrossInitialAndRefreshLoads(t *testing.T) {
	var logs bytes.Buffer

	model := loggingTestModel(t, &logs)
	model.applySessionRows(sessionLoadResultMsg{originQueried: true, originErr: errLoggingTestRemoteFailure})
	model.handleOriginSessions(originSessionsMsg{observation: model.originObservation, err: errLoggingTestRemoteFailure})
	model.handleOriginSessions(originSessionsMsg{observation: model.originObservation + 1, err: nil})
	model.handleOriginSessions(originSessionsMsg{observation: model.originObservation, err: nil})
	model.handleOriginSessions(originSessionsMsg{observation: model.originObservation, err: nil})

	records := loggingTestRecords(t, &logs)
	if len(records) != 2 {
		t.Fatalf("expected one degradation and recovery: %s", logs.String())
	}

	assertLoggingTestRecord(t, records[0], tuiOperationLoadOriginSessions, "warn", "degraded", "tui dependency degraded")
	assertLoggingTestRecord(t, records[1], tuiOperationLoadOriginSessions, "info", "recovered", "tui dependency recovered")
}

func TestOriginSessionLoadsKeepHealthyAndStaleResultsSilent(t *testing.T) {
	var logs bytes.Buffer

	model := loggingTestModel(t, &logs)
	model.applySessionRows(sessionLoadResultMsg{originQueried: true, originErr: nil})
	model.handleOriginSessions(originSessionsMsg{observation: model.originObservation + 1, err: errLoggingTestRemoteFailure})

	if logs.Len() != 0 {
		t.Fatalf("healthy or stale origin load logged: %s", logs.String())
	}
}

func TestOriginSessionDegradationNamesItsCause(t *testing.T) {
	var logs bytes.Buffer

	model := loggingTestModel(t, &logs)
	model.applySessionRows(sessionLoadResultMsg{originQueried: true, originErr: fmt.Errorf("origin: %w", gotmux.ErrNoServer)})

	records := loggingTestRecords(t, &logs)
	if len(records) != 1 || records[0]["status"] != "degraded" || records[0]["error_code"] != tuiErrorNoServer {
		t.Fatalf("degradation record = %s, want one record with error_code %q", logs.String(), tuiErrorNoServer)
	}
}

func assertLoggingTestRecord(
	t *testing.T,
	record map[string]any,
	operation tuiLogOperation,
	level string,
	status string,
	message string,
) {
	t.Helper()

	want := map[string]any{
		"command":   "tui.test",
		"version":   "test-version",
		"commit":    "test-commit",
		"pid":       float64(4242),
		"component": "tui",
		"operation": string(operation),
		"level":     level,
		"status":    status,
		"message":   message,
	}
	if status == "failed" {
		want["error_code"] = tuiErrorOperationFailed
	}

	if status == "degraded" {
		want["reason"] = "dependency_unavailable"
		want["error_code"] = tuiErrorOperationFailed
	}

	if status == "recovered" {
		want["reason"] = "dependency_available"
	}

	if len(record) != len(want) {
		t.Fatalf("record keys = %#v, want only %#v", record, want)
	}

	for field, value := range want {
		if record[field] != value {
			t.Fatalf("record[%q] = %#v, want %#v in %#v", field, record[field], value, record)
		}
	}
}

func loggingTestWithCapturedStdStreams(t *testing.T, body func()) string {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}

	oldStdout, oldStderr := os.Stdout, os.Stderr

	os.Stdout, os.Stderr = writer, writer

	body()

	os.Stdout, os.Stderr = oldStdout, oldStderr

	if err := writer.Close(); err != nil {
		t.Fatalf("closing captured stream writer: %v", err)
	}

	captured, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading captured streams: %v", err)
	}

	if err := reader.Close(); err != nil {
		t.Fatalf("closing captured stream reader: %v", err)
	}

	return string(captured)
}

func TestLogTUIEventUsesExactOutcomeSchemas(t *testing.T) {
	tests := []struct {
		name    string
		outcome tuiLogOutcome
		level   string
		status  string
		message string
	}{
		{name: "success", outcome: tuiLogOutcomeSuccess, level: "info", status: "ok", message: "tui operation completed"},
		{name: "failure", outcome: tuiLogOutcomeFailure, level: "error", status: "failed", message: "tui operation failed"},
		{name: "degradation", outcome: tuiLogOutcomeDegradation, level: "warn", status: "degraded", message: "tui dependency degraded"},
		{name: "recovery", outcome: tuiLogOutcomeRecovery, level: "info", status: "recovered", message: "tui dependency recovered"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer

			logTUIEvent(loggingTestContext(&output), tuiOperationSaveState, test.outcome)

			records := loggingTestRecords(t, &output)
			if len(records) != 1 {
				t.Fatalf("log record count = %d, want 1", len(records))
			}

			assertLoggingTestRecord(t, records[0], tuiOperationSaveState, test.level, test.status, test.message)

			if strings.Contains(output.String(), loggingTestPrivacySentinel) {
				t.Fatalf("log contains private context value: %s", output.String())
			}
		})
	}
}

func TestLogTUIEventSupportsOnlyClosedOperations(t *testing.T) {
	operations := []tuiLogOperation{
		tuiOperationLoadCatalog,
		tuiOperationLoadRemoteCatalog,
		tuiOperationLoadAgentSummaries,
		tuiOperationEnterLiveSession,
		tuiOperationRestoreSavedSession,
		tuiOperationCreateSession,
		tuiOperationActivateAgentPane,
		tuiOperationRenameSession,
		tuiOperationKillSession,
		tuiOperationDeleteSession,
		tuiOperationResolveWorkingDirectory,
		tuiOperationSaveState,
		tuiOperationSnapshotSession,
		tuiOperationAgentGitScan,
		tuiOperationRunAction,
	}

	var output bytes.Buffer

	ctx := loggingTestContext(&output)

	for _, operation := range operations {
		if !knownTUIOperation(operation) {
			t.Fatalf("knownTUIOperation(%q) = false, want fixed operation vocabulary", operation)
		}

		logTUIEvent(ctx, operation, tuiLogOutcomeSuccess)
	}

	logTUIEvent(ctx, tuiLogOperation(loggingTestPrivacySentinel), tuiLogOutcomeSuccess)

	if knownTUIOperation(tuiLogOperation(loggingTestPrivacySentinel)) {
		t.Fatal("knownTUIOperation() accepted an unknown operation")
	}

	records := loggingTestRecords(t, &output)
	if len(records) != len(operations) {
		t.Fatalf("log record count = %d, want %d", len(records), len(operations))
	}

	for index, operation := range operations {
		assertLoggingTestRecord(t, records[index], operation, "info", "ok", "tui operation completed")
	}

	if strings.Contains(output.String(), loggingTestPrivacySentinel) {
		t.Fatalf("log contains unknown operation name: %s", output.String())
	}
}

func TestModelOperationContextCarriesLoggerWithoutPrivateValues(t *testing.T) {
	var output bytes.Buffer

	model, err := newModelWithTestContext(
		loggingTestContext(&output),
		config.DefaultConfig(),
		testState(),
		Dependencies{},
	)
	if err != nil {
		t.Fatalf("newModelWithTestContext() error = %v", err)
	}

	t.Cleanup(model.Close)

	logTUIEvent(model.operationContext(), tuiOperationLoadCatalog, tuiLogOutcomeSuccess)

	records := loggingTestRecords(t, &output)
	if len(records) != 1 {
		t.Fatalf("log record count = %d, want 1", len(records))
	}

	assertLoggingTestRecord(t, records[0], tuiOperationLoadCatalog, "info", "ok", "tui operation completed")

	if strings.Contains(output.String(), loggingTestPrivacySentinel) {
		t.Fatalf("log contains private context value: %s", output.String())
	}
}

func TestLoggingKeepsRecordsOffStdStreams(t *testing.T) {
	var output bytes.Buffer

	leaked := loggingTestWithCapturedStdStreams(t, func() {
		ctx := loggingTestContext(&output)

		logTUIEvent(ctx, tuiOperationSaveState, tuiLogOutcomeSuccess)
		logTUIFailure(ctx, tuiOperationKillSession, errLoggingTestActionFailure)

		logTUIEvent(context.Background(), tuiOperationSaveState, tuiLogOutcomeSuccess)
		logTUIFailure(context.Background(), tuiOperationKillSession, errLoggingTestActionFailure)
	})

	if leaked != "" {
		t.Fatalf("logging wrote to the process streams: %q", leaked)
	}

	records := loggingTestRecords(t, &output)
	if len(records) != 2 {
		t.Fatalf("log record count = %d, want the two logger-bound records", len(records))
	}

	assertLoggingTestRecord(t, records[0], tuiOperationSaveState, "info", "ok", "tui operation completed")
	assertLoggingTestRecord(t, records[1], tuiOperationKillSession, "error", "failed", "tui operation failed")

	if strings.Contains(output.String(), loggingTestPrivacySentinel) {
		t.Fatalf("log contains raw action error: %s", output.String())
	}
}

func TestSessionActionLoggingDefersOnlyPendingPopupOperations(t *testing.T) {
	var output bytes.Buffer

	model := loggingTestModel(t, &output)

	for _, operation := range []tuiLogOperation{
		tuiOperationEnterLiveSession,
		tuiOperationActivateAgentPane,
	} {
		model.logSessionActionResult(sessionActionResultMsg{operation: operation, deferred: true})
	}

	model.logSessionActionResult(sessionActionResultMsg{
		operation: tuiOperationRestoreSavedSession,
		deferred:  true,
	})

	records := loggingTestRecords(t, &output)
	if len(records) != 1 {
		t.Fatalf("deferred action log count = %d, want only completed restore", len(records))
	}

	assertLoggingTestRecord(
		t,
		records[0],
		tuiOperationRestoreSavedSession,
		"info",
		"ok",
		"tui operation completed",
	)
}

func TestPopupSavedRestoreLogsCompletionWithDeferredSwitch(t *testing.T) {
	var output bytes.Buffer

	model := loggingTestModel(t, &output)
	model.mode = modePopup
	model.filtered = []session.Session{{
		Name:   loggingTestPrivacySentinel,
		Kind:   session.KindSaved,
		Origin: session.OriginLocal,
	}}
	dependenciesForTest(model).TmuxSwitchSession = func(context.Context, string) error {
		t.Fatal("TmuxSwitchSession called before deferred popup worker")

		return nil
	}

	_, command := model.handleEnter()()
	trace := runActionTrace(t, model, command)

	if trace.entered == nil || !trace.entered.deferred || trace.entered.switchSession != loggingTestPrivacySentinel {
		t.Fatalf("popup restore entry = %+v, want a deferred switch to the restored session", trace.entered)
	}

	model.handleSessionActionResult(*trace.entered)

	records := loggingTestRecords(t, &output)
	if len(records) != 1 {
		t.Fatalf("popup restore log count = %d, want completed restore", len(records))
	}

	assertLoggingTestRecord(
		t,
		records[0],
		tuiOperationRestoreSavedSession,
		"info",
		"ok",
		"tui operation completed",
	)

	if strings.Contains(output.String(), loggingTestPrivacySentinel) {
		t.Fatalf("popup restore log contains session identity: %s", output.String())
	}
}

type enterOrCreateCase struct {
	name         string
	exists       bool
	popup        bool
	wantDeferred bool
}

func checkEnterOrCreateCase(t *testing.T, test enterOrCreateCase) {
	t.Helper()

	var output bytes.Buffer

	model := loggingTestModel(t, &output)
	if test.popup {
		model.mode = modePopup
	}

	if test.exists {
		model.sessions = []session.Session{{Name: loggingTestPrivacySentinel, Kind: session.KindLive, Origin: session.OriginLocal}}
	}

	dependenciesForTest(model).TmuxSessionExists = func(context.Context, string) (bool, error) {
		return test.exists, nil
	}

	createCalls := 0
	dependenciesForTest(model).TmuxCreateSession = func(context.Context, string, string) error {
		createCalls++

		return nil
	}

	openTestCreateDirectoryPrompt(model, loggingTestPrivacySentinel)
	model.pathInput.SetValue("/TOP_SECRET_PATH")

	trace := pressTestKey(t, model, keyCode(tea.KeyEnter))
	if trace.entered == nil || trace.entered.deferred != test.wantDeferred {
		t.Fatalf("create entry = %+v, want an entry with deferred=%v", trace.entered, test.wantDeferred)
	}

	wantCreateCalls := 1
	if test.exists {
		wantCreateCalls = 0
	}

	if createCalls != wantCreateCalls {
		t.Fatalf("create calls = %d, want %d", createCalls, wantCreateCalls)
	}

	records := loggingTestRecords(t, &output)
	if len(records) != 1 {
		t.Fatalf("create log count = %d, want one create outcome: %#v", len(records), records)
	}

	assertLoggingTestRecord(t, records[0], tuiOperationCreateSession, "info", "ok", "tui operation completed")

	if strings.Contains(output.String(), loggingTestPrivacySentinel) ||
		strings.Contains(output.String(), "TOP_SECRET_PATH") {
		t.Fatalf("create log contains session identity or path: %s", output.String())
	}
}

func TestCreateLogsOneOutcomeForCreateAndReuse(t *testing.T) {
	tests := []enterOrCreateCase{
		{name: "create"},
		{name: "reuse", exists: true},
		{name: "deferred popup create", popup: true, wantDeferred: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checkEnterOrCreateCase(t, test)
		})
	}
}

func TestSessionActionLoggingMapsSuccessAndConsumedFailure(t *testing.T) {
	operations := []tuiLogOperation{
		tuiOperationEnterLiveSession,
		tuiOperationRestoreSavedSession,
		tuiOperationActivateAgentPane,
		tuiOperationRenameSession,
		tuiOperationKillSession,
		tuiOperationDeleteSession,
	}

	var output bytes.Buffer

	model := loggingTestModel(t, &output)
	for _, operation := range operations {
		model.logSessionActionResult(sessionActionResultMsg{operation: operation})
		model.logSessionActionResult(sessionActionResultMsg{
			operation: operation,
			err:       errLoggingTestActionFailure,
		})
	}

	records := loggingTestRecords(t, &output)
	if len(records) != len(operations)*2 {
		t.Fatalf("action log count = %d, want %d", len(records), len(operations)*2)
	}

	for index, operation := range operations {
		assertLoggingTestRecord(t, records[index*2], operation, "info", "ok", "tui operation completed")
		assertLoggingTestRecord(t, records[index*2+1], operation, "error", "failed", "tui operation failed")
	}

	if strings.Contains(output.String(), loggingTestPrivacySentinel) {
		t.Fatalf("log contains raw action error: %s", output.String())
	}
}

func TestLoaderLoggingRecordsOnlyHealthTransitions(t *testing.T) {
	var output bytes.Buffer

	model := loggingTestModel(t, &output)
	deps := dependenciesForTest(model)

	deps.LoadCatalog = func(context.Context) (session.CatalogResult, error) {
		return session.CatalogResult{}, errLoggingTestCatalogFailure
	}

	if err := model.loadSessions(); !errors.Is(err, errLoggingTestCatalogFailure) {
		t.Fatalf("loadSessions() error = %v, want catalog failure", err)
	}

	if err := model.loadSessions(); !errors.Is(err, errLoggingTestCatalogFailure) {
		t.Fatalf("repeated loadSessions() error = %v, want catalog failure", err)
	}

	deps.LoadCatalog = func(context.Context) (session.CatalogResult, error) {
		return session.CatalogResult{Status: session.StatusComplete}, nil
	}

	if err := model.loadSessions(); err != nil {
		t.Fatalf("loadSessions() recovery error = %v", err)
	}

	if err := model.loadSessions(); err != nil {
		t.Fatalf("loadSessions() steady-state error = %v", err)
	}

	model.handleAgentSummaries(agentSummariesMsg{err: errLoggingTestAgentFailure})
	model.handleAgentSummaries(agentSummariesMsg{err: errLoggingTestAgentFailure})
	model.handleAgentSummaries(agentSummariesMsg{})
	model.handleAgentSummaries(agentSummariesMsg{})

	model.handleRemoteCatalog(remoteCatalogMsg{err: errLoggingTestRemoteFailure})
	model.handleRemoteCatalog(remoteCatalogMsg{err: errLoggingTestRemoteFailure})
	model.handleRemoteCatalog(remoteCatalogMsg{})
	model.handleRemoteCatalog(remoteCatalogMsg{})

	records := loggingTestRecords(t, &output)

	want := []struct {
		operation tuiLogOperation
		level     string
		status    string
		message   string
	}{
		{tuiOperationLoadCatalog, "error", "failed", "tui operation failed"},
		{tuiOperationLoadCatalog, "info", "recovered", "tui dependency recovered"},
		{tuiOperationLoadAgentSummaries, "warn", "degraded", "tui dependency degraded"},
		{tuiOperationLoadAgentSummaries, "info", "recovered", "tui dependency recovered"},
		{tuiOperationLoadRemoteCatalog, "warn", "degraded", "tui dependency degraded"},
		{tuiOperationLoadRemoteCatalog, "info", "recovered", "tui dependency recovered"},
	}
	if len(records) != len(want) {
		t.Fatalf("loader log count = %d, want %d: %#v", len(records), len(want), records)
	}

	for index, expected := range want {
		assertLoggingTestRecord(t, records[index], expected.operation, expected.level, expected.status, expected.message)
	}

	for _, sentinel := range []string{
		errLoggingTestCatalogFailure.Error(),
		errLoggingTestAgentFailure.Error(),
		errLoggingTestRemoteFailure.Error(),
	} {
		if strings.Contains(output.String(), sentinel) {
			t.Fatalf("loader log contains raw error %q: %s", sentinel, output.String())
		}
	}
}

func TestLoaderLoggingRecordsPartialCatalogReloadAsOneDegradation(t *testing.T) {
	var output bytes.Buffer

	model := loggingTestModel(t, &output)
	deps := dependenciesForTest(model)

	deps.LoadCatalog = func(context.Context) (session.CatalogResult, error) {
		return session.CatalogResult{
			Sessions: []session.Session{{ID: "$1", Name: "live", Kind: session.KindLive}},
			Status:   session.StatusPartial,
		}, errLoggingTestCatalogFailure
	}

	for range 2 {
		if err := model.loadSessions(); err != nil {
			t.Fatalf("partial loadSessions() error = %v, want rows applied", err)
		}
	}

	deps.LoadCatalog = func(context.Context) (session.CatalogResult, error) {
		return session.CatalogResult{Status: session.StatusComplete}, nil
	}

	if err := model.loadSessions(); err != nil {
		t.Fatalf("loadSessions() recovery error = %v", err)
	}

	records := loggingTestRecords(t, &output)
	if len(records) != 2 {
		t.Fatalf("partial reload log count = %d, want 2: %#v", len(records), records)
	}

	assertLoggingTestRecord(t, records[0], tuiOperationLoadCatalog, "warn", "degraded", "tui dependency degraded")
	assertLoggingTestRecord(t, records[1], tuiOperationLoadCatalog, "info", "recovered", "tui dependency recovered")

	if strings.Contains(output.String(), errLoggingTestCatalogFailure.Error()) {
		t.Fatalf("loader log contains raw error: %s", output.String())
	}
}

func TestLoaderLoggingKeepsHealthyLoadsSilent(t *testing.T) {
	tests := []struct {
		name string
		body func(model *Model)
	}{
		{
			name: "healthy catalog refresh",
			body: func(model *Model) {
				if err := model.loadSessions(); err != nil {
					t.Fatalf("loadSessions() error = %v", err)
				}
			},
		},
		{
			name: "healthy agent delivery",
			body: func(model *Model) { model.handleAgentSummaries(agentSummariesMsg{}) },
		},
		{
			name: "healthy remote revision",
			body: func(model *Model) { model.handleRemoteCatalog(remoteCatalogMsg{}) },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer

			model := loggingTestModel(t, &output)

			for range 3 {
				test.body(model)
			}

			if records := loggingTestRecords(t, &output); len(records) != 0 {
				t.Fatalf("healthy loader logged records: %#v", records)
			}
		})
	}
}

func firstFrameTestLoad() sessionLoadResultMsg {
	return sessionLoadResultMsg{
		sessions: []session.Session{{ID: "$1", Name: loggingTestPrivacySentinel, Kind: session.KindLive}},
		status:   session.StatusComplete,
		timing:   sessionLoadTiming{catalog: 40 * time.Millisecond, agents: 120 * time.Millisecond},
	}
}

func TestPopupFirstFrameLogsOneTimingRecord(t *testing.T) {
	var output bytes.Buffer

	model := loggingTestModel(t, &output)
	model.mode = modePopup
	model.SetLaunchedAt(time.Now().Add(-250 * time.Millisecond))
	model.Init()

	stdStreams := loggingTestWithCapturedStdStreams(t, func() {
		model.handleSessionLoadResult(firstFrameTestLoad())
		updateTestModel(t, model, popupStartupDeadlineMsg{})
		model.handleSessionLoadResult(firstFrameTestLoad())
	})
	if stdStreams != "" {
		t.Fatalf("first-frame logging wrote to std streams: %q", stdStreams)
	}

	records := loggingTestRecords(t, &output)
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one first-frame record for two loads", records)
	}

	record := records[0]
	for _, field := range []string{"load_ms", "catalog_ms", "agents_ms", "since_launch_ms"} {
		if _, ok := record[field].(float64); !ok {
			t.Fatalf("record[%q] = %#v, want a millisecond count in %#v", field, record[field], record)
		}
	}

	sinceLaunch, _ := record["since_launch_ms"].(float64)
	if record["catalog_ms"] != float64(40) || record["agents_ms"] != float64(120) || sinceLaunch < 250 {
		t.Fatalf("first-frame timing = %#v, want catalog 40, agents 120, since launch >= 250", record)
	}

	delete(record, "load_ms")
	delete(record, "catalog_ms")
	delete(record, "agents_ms")
	delete(record, "since_launch_ms")
	assertLoggingTestRecord(t, record, tuiLogOperation(tuiOperationFirstFrame), "info", "ok", "tui first frame shown")

	if strings.Contains(output.String(), loggingTestPrivacySentinel) {
		t.Fatalf("first-frame record leaked a session name: %s", output.String())
	}
}

func TestPopupLoadingFirstFrameOmitsUnfinishedLoaderTimings(t *testing.T) {
	var output bytes.Buffer

	model := loggingTestModel(t, &output)
	model.mode = modePopup
	model.SetLaunchedAt(time.Now().Add(-250 * time.Millisecond))
	model.Init()

	stdStreams := loggingTestWithCapturedStdStreams(t, func() {
		updateTestModel(t, model, popupStartupDeadlineMsg{})
		updateTestModel(t, model, popupStartupDeadlineMsg{})
		model.handleSessionLoadResult(firstFrameTestLoad())
		updateTestModel(t, model, popupStartupDeadlineMsg{})
	})
	if stdStreams != "" {
		t.Fatalf("first-frame logging wrote to std streams: %q", stdStreams)
	}

	records := loggingTestRecords(t, &output)
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one loading first-frame record", records)
	}

	record := records[0]
	if _, ok := record["load_ms"].(float64); !ok {
		t.Fatalf("record = %#v, want measured first-frame load duration", record)
	}

	sinceLaunch, ok := record["since_launch_ms"].(float64)
	if !ok || sinceLaunch < 250 {
		t.Fatalf("record = %#v, want measured launch duration >= 250ms", record)
	}

	delete(record, "load_ms")
	delete(record, "since_launch_ms")
	assertLoggingTestRecord(t, record, tuiLogOperation(tuiOperationFirstFrame), "info", "ok", "tui first frame shown")

	if strings.Contains(output.String(), loggingTestPrivacySentinel) {
		t.Fatalf("first-frame record leaked a session name: %s", output.String())
	}
}

func TestSeededPopupStartupStaysPopulatedAndSilent(t *testing.T) {
	var output bytes.Buffer

	model := loggingTestModel(t, &output)
	model.mode = modePopup
	model.SetSize(80, 20)
	model.SetInitialSessions(firstFrameTestLoad().sessions)
	model.Init()
	updateTestModel(t, model, popupStartupDeadlineMsg{})

	view := stripANSI(viewString(model.View()))
	if !strings.Contains(view, loggingTestPrivacySentinel) || strings.Contains(view, "Loading sessions...") {
		t.Fatalf("seeded popup view = %q, want populated sessions without a loading frame", view)
	}

	model.handleSessionLoadResult(firstFrameTestLoad())

	if records := loggingTestRecords(t, &output); len(records) != 0 {
		t.Fatalf("seeded popup records = %#v, want none", records)
	}
}

func TestFirstFrameRecordOmitsUnknownLaunchAndSkipsInlinePickers(t *testing.T) {
	var popupOutput bytes.Buffer

	popup := loggingTestModel(t, &popupOutput)
	popup.mode = modePopup
	popup.Init()
	popup.handleSessionLoadResult(firstFrameTestLoad())

	records := loggingTestRecords(t, &popupOutput)
	if len(records) != 1 || records[0]["operation"] != tuiOperationFirstFrame {
		t.Fatalf("popup records = %#v, want one first-frame record", records)
	}

	if _, ok := records[0]["since_launch_ms"]; ok {
		t.Fatalf("first-frame record = %#v, want no launch latency without a launch time", records[0])
	}

	var inlineOutput bytes.Buffer

	inline := loggingTestModel(t, &inlineOutput)
	inline.mode = modeInline
	inline.Init()
	inline.handleSessionLoadResult(firstFrameTestLoad())

	if records := loggingTestRecords(t, &inlineOutput); len(records) != 0 {
		t.Fatalf("inline picker records = %#v, want none: it shows frames while loading", records)
	}
}

func TestDialogActionsLogTypedSuccessOperations(t *testing.T) {
	var output bytes.Buffer

	model := loggingTestModel(t, &output)
	model.filtered = []session.Session{{
		Name:   loggingTestPrivacySentinel,
		Kind:   session.KindLive,
		Origin: session.OriginLocal,
	}}

	invokeTestAction(t, model, action.SessionRename.ID)
	model.textParamInput.SetValue("TOP_SECRET_RENAMED_SESSION")
	pressTestKey(t, model, keyCode(tea.KeyEnter))

	setActionConfirm(model.config, action.SessionKill.ID, false)

	model.filtered = []session.Session{{
		Name:   loggingTestPrivacySentinel,
		Kind:   session.KindLive,
		Origin: session.OriginLocal,
	}}
	invokeTestAction(t, model, action.SessionKill.ID)

	setActionConfirm(model.config, action.SessionDelete.ID, false)

	model.filtered = []session.Session{{
		Name:   loggingTestPrivacySentinel,
		Kind:   session.KindSaved,
		Origin: session.OriginLocal,
	}}
	invokeTestAction(t, model, action.SessionDelete.ID)

	records := loggingTestRecords(t, &output)

	wantOperations := []tuiLogOperation{
		tuiOperationRenameSession,
		tuiOperationKillSession,
		tuiOperationDeleteSession,
	}
	if len(records) != len(wantOperations) {
		t.Fatalf("dialog action log count = %d, want %d: %#v", len(records), len(wantOperations), records)
	}

	for index, operation := range wantOperations {
		assertLoggingTestRecord(t, records[index], operation, "info", "ok", "tui operation completed")
	}

	if strings.Contains(output.String(), "TOP_SECRET") {
		t.Fatalf("dialog action log contains session identity: %s", output.String())
	}
}

func TestCreateDirectoryAndStateSaveLoggingBoundaries(t *testing.T) {
	var output bytes.Buffer

	model := loggingTestModel(t, &output)
	model.textInput.SetValue(loggingTestPrivacySentinel)
	dependenciesForTest(model).GetWorkingDirectory = func() (string, error) {
		return "", errLoggingTestWorkingDirectory
	}

	invokeTestAction(t, model, action.SessionCreate.ID)

	dependenciesForTest(model).SaveModelState = func(*session.State) error {
		return errLoggingTestStateSaveFailure
	}
	failure := model.saveDisplayStateCommand()()
	model.Update(failure)

	dependenciesForTest(model).SaveModelState = func(*session.State) error { return nil }
	success := model.saveDisplayStateCommand()()
	model.Update(success)

	records := loggingTestRecords(t, &output)
	if len(records) != 2 {
		t.Fatalf("boundary log count = %d, want cwd and state failures: %#v", len(records), records)
	}

	assertLoggingTestRecord(
		t,
		records[0],
		tuiOperationResolveWorkingDirectory,
		"error",
		"failed",
		"tui operation failed",
	)
	assertLoggingTestRecord(t, records[1], tuiOperationSaveState, "error", "failed", "tui operation failed")

	if strings.Contains(output.String(), "TOP_SECRET") {
		t.Fatalf("boundary log contains private input, error, or status: %s", output.String())
	}
}

func TestConfirmWithoutPendingActionDoesNotLogSuccess(t *testing.T) {
	var output bytes.Buffer

	model := loggingTestModel(t, &output)
	model.dialog = dialogConfirm
	model.pending = nil

	handler := model.confirmPending()
	if handler == nil {
		t.Fatal("confirmPending() returned nil")
	}

	_, cmd := handler()

	if output.Len() != 0 || cmd != nil {
		t.Fatalf("confirming nothing logged %q and returned command %v, want neither", output.String(), cmd)
	}

	if model.dialog != dialogNone {
		t.Fatalf("dialog after confirming nothing = %v, want closed", model.dialog)
	}
}

func TestSnapshotActionLoggingUsesFixedPrivateSchema(t *testing.T) {
	var output bytes.Buffer

	model := loggingTestModel(t, &output)

	model.logSessionActionResult(sessionActionResultMsg{
		operation: tuiOperationSnapshotSession,
		err:       errLoggingTestActionFailure,
	})
	model.logSessionActionResult(sessionActionResultMsg{
		operation: tuiOperationSnapshotSession,
	})

	records := loggingTestRecords(t, &output)
	if len(records) != 2 {
		t.Fatalf("snapshot log count = %d, want 2", len(records))
	}

	assertLoggingTestRecord(t, records[0], tuiOperationSnapshotSession, "error", "failed", "tui operation failed")
	assertLoggingTestRecord(t, records[1], tuiOperationSnapshotSession, "info", "ok", "tui operation completed")

	if strings.Contains(output.String(), loggingTestPrivacySentinel) {
		t.Fatalf("snapshot log contains raw action error: %s", output.String())
	}
}

func TestLogTUIFailureClassification(t *testing.T) {
	tests := []failureClassificationCase{
		{
			name: "oops metadata does not enter logs",
			err: oops.In("tui").
				Code(loggingTestPrivacySentinel).
				With("phase", loggingTestPrivacySentinel, "action", loggingTestPrivacySentinel).
				Wrap(errLoggingTestSecretDetails),
			wantCode: tuiErrorOperationFailed,
		},
		{
			name:     "session attached sentinel",
			err:      session.ErrSessionAttached,
			wantCode: tuiErrorSessionAttached,
		},
		{
			name:     "session already exists sentinel",
			err:      session.ErrSessionAlreadyExists,
			wantCode: tuiErrorSessionAlreadyExists,
		},
		{
			name:     "session not found sentinel",
			err:      session.ErrSessionNotFound,
			wantCode: tuiErrorSessionNotFound,
		},
		{
			name:     "invalid session name sentinel",
			err:      session.ErrInvalidSessionName,
			wantCode: tuiErrorInvalidSessionName,
		},
		{
			name:     "uncertain publication sentinel",
			err:      session.ErrUncertain,
			wantCode: tuiErrorUncertainOutcome,
		},
		{
			name:     "canceled operation",
			err:      context.Canceled,
			wantCode: tuiErrorCanceled,
		},
		{
			name:     "timed out operation",
			err:      context.DeadlineExceeded,
			wantCode: tuiErrorTimedOut,
		},
		{
			name:     "os not exist sentinel",
			err:      os.ErrNotExist,
			wantCode: tuiErrorNotFound,
		},
		{
			name:     "tmux server missing sentinel",
			err:      gotmux.ErrNoServer,
			wantCode: tuiErrorNoServer,
		},
		{
			name:     "tmux target not found sentinel",
			err:      gotmux.ErrNotFound,
			wantCode: tuiErrorSessionNotFound,
		},
		{
			name:     "unsupported tmux feature",
			err:      gotmux.ErrUnsupported,
			wantCode: tuiErrorUnsupported,
		},
		{
			name:     "unsupported tmux transport",
			err:      gotmux.ErrTransportUnsupported,
			wantCode: tuiErrorUnsupported,
		},
		{
			name: "mutation with unspecified effect",
			err: &gotmux.OperationError{
				Operation: "kill-session",
				Outcome:   gotmux.Outcome{},
				Err:       errLoggingTestRandomFailure,
			},
			wantCode: tuiErrorUncertainOutcome,
		},
		{
			name: "mutation with unknown effect",
			err: &gotmux.OperationError{
				Operation: "kill-session",
				Outcome:   gotmux.Outcome{Effect: gotmux.EffectUnknown},
				Err:       errLoggingTestRandomFailure,
			},
			wantCode: tuiErrorUncertainOutcome,
		},
		{
			name:     "unknown error fallback",
			err:      errLoggingTestRandomFailure,
			wantCode: tuiErrorOperationFailed,
		},
		{
			name:     "nil error fallback",
			err:      nil,
			wantCode: tuiErrorOperationFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkFailureClassification(t, tt)
		})
	}
}

type failureClassificationCase struct {
	name       string
	err        error
	wantCode   string
	wantPhase  string
	wantAction string
}

func checkFailureClassification(t *testing.T, tt failureClassificationCase) {
	t.Helper()

	var output bytes.Buffer

	ctx := loggingTestContext(&output)
	logTUIFailure(ctx, tuiOperationEnterLiveSession, tt.err)

	records := loggingTestRecords(t, &output)
	if len(records) != 1 {
		t.Fatalf("records count = %d, want 1", len(records))
	}

	record := records[0]
	if record["error_code"] != tt.wantCode {
		t.Errorf("error_code = %q, want %q", record["error_code"], tt.wantCode)
	}

	if record["component"] != "tui" || record["operation"] != string(tuiOperationEnterLiveSession) ||
		record["level"] != "error" || record["status"] != "failed" {
		t.Errorf("record schema = %#v, want fixed tui failure schema", record)
	}

	assertClassificationContext(t, record, tt)

	if strings.Contains(output.String(), "secret details") || strings.Contains(output.String(), loggingTestPrivacySentinel) {
		t.Fatalf("log contains raw error: %s", output.String())
	}
}

func assertAllowedFailureFields(t *testing.T, record map[string]any) {
	t.Helper()

	for field := range record {
		switch field {
		case "command", "version", "commit", "pid", "component",
			"operation", "level", "status", "message", "error_code":
		default:
			t.Errorf("record field %q = %#v, want only the fixed failure schema", field, record[field])
		}
	}
}

func assertClassificationContext(t *testing.T, record map[string]any, tt failureClassificationCase) {
	t.Helper()

	if tt.wantPhase != "" && record["phase"] != tt.wantPhase {
		t.Errorf("phase = %q, want %q", record["phase"], tt.wantPhase)
	}

	if tt.wantAction != "" && record["action"] != tt.wantAction {
		t.Errorf("action = %q, want %q", record["action"], tt.wantAction)
	}

	if tt.wantPhase == "" && tt.wantAction == "" {
		assertAllowedFailureFields(t, record)
	}
}
