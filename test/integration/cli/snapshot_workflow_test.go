//go:build integration

package cli_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cmdsnapshot "github.com/zigai/sesh/internal/cli/cmd/snapshot"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

type (
	snapshotListResultJSON    = cmdsnapshot.ListResultJSON
	snapshotHistoryResultJSON = cmdsnapshot.HistoryResultJSON
	snapshotVerifyResultJSON  = cmdsnapshot.VerifyResultJSON
)

func TestSnapshotSaveListHistoryVerifyWorkflow(t *testing.T) {
	server := newCaptureCLIServer(t)

	socket := server.Endpoint().SocketPath
	if socket == "" {
		t.Fatal("empty socket from tmuxtest server")
	}

	repoDir := filepath.Join(t.TempDir(), "repo")
	leaseDir := filepath.Join(t.TempDir(), "lease")

	sessions, err := server.Sessions(t.Context())
	if err != nil {
		t.Fatalf("listing sessions: %v", err)
	}

	if len(sessions) == 0 {
		t.Fatal("expected at least 1 session from tmuxtest server")
	}

	sessionID := string(sessions[0].ID)
	sessionName := sessions[0].Name

	savedGen, savedAttempt := testSaveWorkflow(t, repoDir, socket, leaseDir, sessionID)
	testActionSaveParity(t, repoDir, socket, leaseDir, sessionID)
	testSavePlainText(t, repoDir, socket, leaseDir, sessionID)
	testListWorkflow(t, repoDir, savedGen, sessionName)
	testHistoryWorkflow(t, repoDir)
	testVerifyWorkflow(t, repoDir, savedGen)

	_ = savedAttempt
}

func testSaveWorkflow(t *testing.T, repoDir, socket, leaseDir, sessionID string) (string, string) {
	t.Helper()

	execution := executeJSON(
		t,
		actionCmd, "snapshot-save",
		testRepositoryFlag, repoDir,
		testSocketFlag, socket,
		testLeaseRootFlag, leaseDir,
		testSessionFlag, sessionID,
		testJSONFlag,
	)

	if !execution.envelope.OK {
		t.Fatalf("save expected practical success, got: %s", execution.stdout)
	}

	if execution.envelope.Command != "action.snapshot-save" {
		t.Fatalf("command = %q, want snapshot.save", execution.envelope.Command)
	}

	if execution.envelope.Error != nil {
		t.Fatalf("save returned unexpected error: %#v", execution.envelope.Error)
	}

	res := decodeJSONPayload[snapshotSaveResultJSON](t, execution.envelope.Result)
	if res.Attempt == "" || res.Generation == "" {
		t.Fatalf("missing attempt or generation in save result: %#v", res)
	}

	if res.Status != "best_effort" {
		t.Fatalf("status = %q, want best_effort", res.Status)
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(execution.stdout), &raw); err != nil {
		t.Fatalf("unmarshaling stdout: %v", err)
	}

	assertAllKeysCamelCase(t, raw, "$")

	return res.Generation, res.Attempt
}

func testActionSaveParity(t *testing.T, repoDir, socket, leaseDir, sessionID string) {
	t.Helper()

	execution := executeJSON(
		t,
		"action", "snapshot-save",
		testRepositoryFlag, repoDir,
		testSocketFlag, socket,
		testLeaseRootFlag, leaseDir,
		testSessionFlag, sessionID,
		testJSONFlag,
	)

	if !execution.envelope.OK {
		t.Fatalf("action save expected practical success, got: %s", execution.stdout)
	}

	if execution.envelope.Command != "action.snapshot-save" {
		t.Fatalf("command = %q, want action.snapshot-save", execution.envelope.Command)
	}

	if execution.envelope.Error != nil {
		t.Fatalf("action save returned unexpected error: %#v", execution.envelope.Error)
	}

	res := decodeJSONPayload[snapshotSaveResultJSON](t, execution.envelope.Result)
	if res.Status != "best_effort" {
		t.Fatalf("status = %q, want best_effort", res.Status)
	}

	if res.Attempt == "" || res.Generation == "" {
		t.Fatalf("action save missing attempt/generation: %#v", res)
	}
}

func testSavePlainText(t *testing.T, repoDir, socket, leaseDir, sessionID string) {
	t.Helper()

	stdout, stderr, err := executeCommand(
		t,
		actionCmd, "snapshot-save",
		testRepositoryFlag, repoDir,
		testSocketFlag, socket,
		testLeaseRootFlag, leaseDir,
		testSessionFlag, sessionID,
	)
	if err != nil {
		t.Fatalf("save plain text failed: %v", err)
	}

	if !strings.Contains(stdout, "Status:     best_effort") {
		t.Fatalf("stdout missing status best_effort:\n%s", stdout)
	}

	if !strings.Contains(stdout, "Generation:") || !strings.Contains(stdout, "Attempt:") {
		t.Fatalf("stdout missing generation or attempt:\n%s", stdout)
	}

	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
}

func testListWorkflow(t *testing.T, repoDir, savedGen, sessionName string) {
	t.Helper()

	implicitExec := executeJSON(t, snapshotCmd, "list", testRepositoryFlag, repoDir, testJSONFlag)
	if !implicitExec.envelope.OK {
		t.Fatalf("implicit practical list failed: %s", implicitExec.stderr)
	}

	implicit := decodeJSONPayload[snapshotListResultJSON](t, implicitExec.envelope.Result)
	if implicit.Generation == "" {
		t.Fatal("implicit practical list returned no generation")
	}

	explicitExec := executeJSON(
		t,
		snapshotCmd, "list",
		testRepositoryFlag, repoDir,
		testGenerationFlag, savedGen,
		testJSONFlag,
	)

	if !explicitExec.envelope.OK {
		t.Fatalf("list failed: %s", explicitExec.stderr)
	}

	listRes := decodeJSONPayload[snapshotListResultJSON](t, explicitExec.envelope.Result)
	if len(listRes.Sessions) == 0 {
		t.Fatalf("expected saved sessions in list result, got 0")
	}

	found := false

	for _, s := range listRes.Sessions {
		if s.Name == sessionName {
			found = true

			break
		}
	}

	if !found {
		t.Fatalf("session %q not found in list: %#v", sessionName, listRes.Sessions)
	}

	stdout, stderr, err := executeCommand(
		t,
		snapshotCmd, "list",
		testRepositoryFlag, repoDir,
		testGenerationFlag, savedGen,
	)
	if err != nil {
		t.Fatalf("list table error: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "Name") || !strings.Contains(stdout, sessionName) {
		t.Fatalf("list output missing table header or session name:\n%s", stdout)
	}
}

func testHistoryWorkflow(t *testing.T, repoDir string) {
	t.Helper()

	execution := executeJSON(
		t,
		snapshotCmd, "history",
		testRepositoryFlag, repoDir,
		testJSONFlag,
	)

	if !execution.envelope.OK {
		t.Fatalf("history failed: %s", execution.stderr)
	}

	if execution.envelope.Command != "snapshot.history" {
		t.Fatalf("command = %q, want snapshot.history", execution.envelope.Command)
	}

	histRes := decodeJSONPayload[snapshotHistoryResultJSON](t, execution.envelope.Result)
	if len(histRes.Attempts) == 0 || len(histRes.Generations) == 0 {
		t.Fatalf("expected attempts and generations in history: %#v", histRes)
	}

	if histRes.Current == "" {
		t.Fatalf("history current generation is empty")
	}

	stdout, stderr, err := executeCommand(
		t,
		snapshotCmd, "history",
		testRepositoryFlag, repoDir,
		"--full",
	)
	if err != nil {
		t.Fatalf("history table error: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "Saved") || !strings.Contains(stdout, "Result") || !strings.Contains(stdout, "saved") {
		t.Fatalf("history output missing headers:\n%s", stdout)
	}
}

func testVerifyWorkflow(t *testing.T, repoDir, savedGen string) {
	t.Helper()

	implicitExec := executeJSON(t, snapshotCmd, "verify", testRepositoryFlag, repoDir, testJSONFlag)
	if !implicitExec.envelope.OK {
		t.Fatalf("implicit practical verify failed: %s", implicitExec.stderr)
	}

	implicit := decodeJSONPayload[snapshotVerifyResultJSON](t, implicitExec.envelope.Result)
	if implicit.Generation == "" || !implicit.RestoreReady {
		t.Fatalf("implicit verify = %#v, want selected practical generation", implicit)
	}

	explicitExec := executeJSON(
		t,
		snapshotCmd, "verify",
		testRepositoryFlag, repoDir,
		testGenerationFlag, savedGen,
		testJSONFlag,
	)

	if !explicitExec.envelope.OK {
		t.Fatalf("verify failed: %s", explicitExec.stderr)
	}

	verRes := decodeJSONPayload[snapshotVerifyResultJSON](t, explicitExec.envelope.Result)
	assertExplicitVerifyResult(t, verRes)

	stdout, stderr, err := executeCommand(
		t,
		snapshotCmd, "verify",
		testRepositoryFlag, repoDir,
		testGenerationFlag, savedGen,
	)
	if err != nil {
		t.Fatalf("verify error: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "Byte Integrity:           valid") {
		t.Fatalf("stdout missing Byte Integrity: valid:\n%s", stdout)
	}

	if !strings.Contains(stdout, "Source Evidence Complete: no") {
		t.Fatalf("stdout missing Source Evidence Complete: no:\n%s", stdout)
	}

	if !strings.Contains(stdout, "Restore Ready:            yes") {
		t.Fatalf("stdout missing Restore Ready: yes:\n%s", stdout)
	}
}

func TestSnapshotVerifyReportsCorruptedArtifacts(t *testing.T) {
	server := newCaptureCLIServer(t)
	socket := server.Endpoint().SocketPath

	repoDir := filepath.Join(t.TempDir(), "repo")
	leaseDir := filepath.Join(t.TempDir(), "lease")

	sessions, err := server.Sessions(t.Context())
	if err != nil || len(sessions) == 0 {
		t.Fatalf("listing sessions: %v", err)
	}

	sessionID := string(sessions[0].ID)

	execution := executeJSON(
		t,
		actionCmd, "snapshot-save",
		testRepositoryFlag, repoDir,
		testSocketFlag, socket,
		testLeaseRootFlag, leaseDir,
		testSessionFlag, sessionID,
		testJSONFlag,
	)

	if !execution.envelope.OK || execution.envelope.Error != nil {
		t.Fatalf("save failed: %s", execution.stderr)
	}

	saveRes := decodeJSONPayload[snapshotSaveResultJSON](t, execution.envelope.Result)
	genID := saveRes.Generation
	corruptFirstArtifact(t, repoDir, genID)

	verifyExec := executeJSON(
		t,
		snapshotCmd, "verify",
		testRepositoryFlag, repoDir,
		testGenerationFlag, genID,
		testJSONFlag,
	)

	if verifyExec.envelope.OK {
		t.Fatalf("expected verify to fail on corrupted artifact, got ok=true: %s", verifyExec.stdout)
	}

	if verifyExec.envelope.Error == nil {
		t.Fatalf("missing error payload in degraded result: %s", verifyExec.stdout)
	}

	if verifyExec.envelope.Error.Code != "repositoryCorrupt" {
		t.Fatalf("error.code = %q, want repositoryCorrupt", verifyExec.envelope.Error.Code)
	}

	verRes := decodeJSONPayload[snapshotVerifyResultJSON](t, verifyExec.envelope.Result)
	if verRes.ByteIntegrity {
		t.Fatalf("byteIntegrity = true on corrupted artifact, want false")
	}

	if verRes.RestoreReady {
		t.Fatalf("restoreReady = true on corrupted artifact, want false")
	}
}

func corruptFirstArtifact(t *testing.T, repoDir, genID string) {
	t.Helper()

	artifactsDir := filepath.Join(repoDir, "generations", genID, "artifacts")

	entries, err := os.ReadDir(artifactsDir)
	if err != nil {
		t.Fatalf("reading artifacts dir: %v", err)
	}

	if len(entries) == 0 {
		t.Fatal("no artifacts found in snapshot")
	}

	corruptPath := filepath.Join(artifactsDir, entries[0].Name())
	if err := os.WriteFile(corruptPath, []byte("corrupted-not-gzip-bytes"), 0o600); err != nil {
		t.Fatalf("corrupting artifact: %v", err)
	}
}

func assertExplicitVerifyResult(t *testing.T, verRes snapshotVerifyResultJSON) {
	t.Helper()

	if !verRes.ByteIntegrity {
		t.Fatalf("byteIntegrity = false, want true")
	}

	if verRes.SourceEvidenceComplete {
		t.Fatalf("sourceEvidenceComplete = true, want false for observational capture")
	}

	if !verRes.RestoreReady {
		t.Fatalf("restoreReady = false, want practical reconstruction readiness")
	}

	if verRes.RestoreReason != "practical_reconstruction_available" {
		t.Fatalf("restoreReason = %q, want practical_reconstruction_available", verRes.RestoreReason)
	}

	if verRes.Consistency != "observed" {
		t.Fatalf("consistency = %q, want observed", verRes.Consistency)
	}

	for _, e := range verRes.Evidence {
		if e.Status != "unverified" && e.Status != "observed" {
			t.Fatalf("evidence status = %q, want visible observational limitation", e.Status)
		}
	}
}

func TestSnapshotVerifyCompleteNativeFixture(t *testing.T) {
	repoDir := filepath.Join(t.TempDir(), "repo")

	repo, err := store.Open(t.Context(), repoDir)
	if err != nil {
		t.Fatal(err)
	}

	attempt, err := repo.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UnixNano()

	manifest := model.Manifest{
		Format:   "sesh",
		Version:  model.Schema,
		Started:  now,
		Finished: now,
		Source: model.Source{
			TmuxVersion:    "3.6",
			LibraryVersion: "v0.3.0",
			Consistency:    "checkpoint",
			Options:        nil,
			Environment:    nil,
		},
		Sessions: []model.Session{
			{ID: "s0", Name: model.Bytes("work"), Directory: model.Bytes("/tmp"), Group: "g0"},
			{ID: "s1", Name: model.Bytes("peer"), Directory: model.Bytes("/tmp"), Group: "g0"},
		},
		Groups: []model.Group{
			{ID: "g0", Name: model.Bytes("group"), Members: []string{"s0", "s1"}},
		},
		Windows: []model.Window{
			{ID: "w0", Name: model.Bytes("editor"), Width: 80, Height: 24, Layout: model.Layout{Width: 80, Height: 24, Pane: "p0"}},
		},
		Links: []model.Link{
			{Session: "s0", Window: "w0", Index: 3, Active: true},
			{Session: "s1", Window: "w0", Index: 3, Active: true},
		},
		Panes: []model.Pane{
			{ID: "p0", Window: "w0", Directory: model.Bytes("/tmp"), Width: 80, Height: 24, Active: true, Launch: model.Launch{Recovery: "unverified"}},
		},
		Artifacts: nil,
	}
	for _, domain := range []string{"S01", "S02", "S03", "S04", "S05", "S06", "S07", "S08", "S09", "S10"} {
		manifest.Evidence = append(manifest.Evidence, model.Evidence{Domain: domain, Status: "verified", Reason: ""})
	}

	if !manifest.Complete() {
		t.Fatal("test fixture must have Complete() == true")
	}

	gen, err := repo.Commit(t.Context(), attempt.ID, manifest, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}

	execution := executeJSON(
		t,
		snapshotCmd, "verify",
		testRepositoryFlag, repoDir,
		testGenerationFlag, gen.ID,
		testJSONFlag,
	)

	if !execution.envelope.OK {
		t.Fatalf("verify failed: %s", execution.stderr)
	}

	verRes := decodeJSONPayload[snapshotVerifyResultJSON](t, execution.envelope.Result)
	if !verRes.ByteIntegrity {
		t.Fatal("expected byteIntegrity true")
	}

	if !verRes.SourceEvidenceComplete {
		t.Fatal("expected sourceEvidenceComplete true for complete fixture")
	}

	if !verRes.RestoreReady {
		t.Fatal("restoreReady = false, want practical reconstruction readiness")
	}

	if verRes.RestoreReason != "practical_reconstruction_available" {
		t.Fatalf("restoreReason = %q, want practical_reconstruction_available", verRes.RestoreReason)
	}
}

func appendFailedAttemptToCatalog(t *testing.T, repoDir string) {
	t.Helper()

	catalogPath := filepath.Join(repoDir, "catalog.json")

	data, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatalf("reading catalog.json: %v", err)
	}

	var cat struct {
		Version     int                `json:"version"`
		Sequence    uint64             `json:"sequence"`
		Current     string             `json:"current"`
		Attempts    []store.Attempt    `json:"attempts"`
		Generations []store.Generation `json:"generations"`
		Operations  []store.Operation  `json:"operations"`
		Rewrites    []store.Rewrite    `json:"rewrites"`
	}

	if err := json.Unmarshal(data, &cat); err != nil {
		t.Fatalf("unmarshaling catalog.json: %v", err)
	}

	cat.Sequence++
	failedAttempt := store.Attempt{
		ID:             strings.Repeat("f", 32),
		Sequence:       cat.Sequence,
		State:          "failed",
		Reason:         "save_failed",
		ManifestSHA256: "",
	}
	cat.Attempts = append(cat.Attempts, failedAttempt)

	updatedData, err := json.MarshalIndent(cat, "", "  ")
	if err != nil {
		t.Fatalf("marshaling updated catalog: %v", err)
	}

	if err := os.WriteFile(catalogPath, updatedData, 0o600); err != nil {
		t.Fatalf("writing updated catalog: %v", err)
	}
}

func TestFailedLatestSaveFallsBackToNewestFinishedGeneration(t *testing.T) {
	server := newCaptureCLIServer(t)
	socket := server.Endpoint().SocketPath

	repoDir := filepath.Join(t.TempDir(), "repo")
	leaseDir := filepath.Join(t.TempDir(), "lease")

	sessions, err := server.Sessions(t.Context())
	if err != nil || len(sessions) == 0 {
		t.Fatalf("listing sessions: %v", err)
	}

	sessionID := string(sessions[0].ID)

	execution := executeJSON(
		t,
		actionCmd, "snapshot-save",
		testRepositoryFlag, repoDir,
		testSocketFlag, socket,
		testLeaseRootFlag, leaseDir,
		testSessionFlag, sessionID,
		testJSONFlag,
	)

	if !execution.envelope.OK || execution.envelope.Error != nil {
		t.Fatalf("save failed: %s", execution.stderr)
	}

	saveRes := decodeJSONPayload[snapshotSaveResultJSON](t, execution.envelope.Result)
	committedGen := saveRes.Generation

	appendFailedAttemptToCatalog(t, repoDir)

	listExec := executeJSON(t, snapshotCmd, "list", testRepositoryFlag, repoDir, testJSONFlag)
	if !listExec.envelope.OK {
		t.Fatalf("a failed latest save must not block list: %s", listExec.stderr)
	}

	if listed := decodeJSONPayload[snapshotListResultJSON](t, listExec.envelope.Result); listed.Generation != committedGen {
		t.Fatalf("list generation = %q, want the newest finished save %q", listed.Generation, committedGen)
	}

	verifyExec := executeJSON(t, snapshotCmd, "verify", testRepositoryFlag, repoDir, testJSONFlag)
	if !verifyExec.envelope.OK {
		t.Fatalf("a failed latest save must not block verify: %s", verifyExec.stderr)
	}

	if verified := decodeJSONPayload[snapshotVerifyResultJSON](t, verifyExec.envelope.Result); verified.Generation != committedGen {
		t.Fatalf("verify generation = %q, want the newest finished save %q", verified.Generation, committedGen)
	}

	explicitList := executeJSON(
		t,
		snapshotCmd, "list",
		testRepositoryFlag, repoDir,
		testGenerationFlag, committedGen,
		testJSONFlag,
	)

	if !explicitList.envelope.OK {
		t.Fatalf("explicit list failed: %s", explicitList.stderr)
	}

	explicitVerify := executeJSON(
		t,
		snapshotCmd, "verify",
		testRepositoryFlag, repoDir,
		testGenerationFlag, committedGen,
		testJSONFlag,
	)

	if !explicitVerify.envelope.OK {
		t.Fatalf("explicit verify failed: %s", explicitVerify.stderr)
	}
}

func TestSubprocessExecution(t *testing.T) {
	binaryDir := t.TempDir()
	binaryPath := filepath.Join(binaryDir, "sesh")

	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}

	buildCmd := exec.CommandContext(t.Context(), "go", "build", "-o", binaryPath, "./cmd/sesh")

	buildCmd.Dir = repoRoot
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\noutput: %s", err, out)
	}

	versionCmd := exec.CommandContext(t.Context(), binaryPath, "--version")

	out, err := versionCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sesh --version error: %v\noutput: %s", err, out)
	}

	if !strings.HasPrefix(string(out), "sesh dev") {
		t.Fatalf("unexpected version output: %s", out)
	}

	unknownCmd := exec.CommandContext(t.Context(), binaryPath, "nonexistent")

	out, err = unknownCmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected failure for unknown command")
	}

	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		if exitErr.ExitCode() != 2 {
			t.Fatalf("exit code = %d, want 2", exitErr.ExitCode())
		}
	} else {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}

	if !strings.Contains(string(out), "unknown command") {
		t.Fatalf("unexpected output: %s", out)
	}
}
