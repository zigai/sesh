//go:build integration && (linux || darwin)

package cli_test

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/integration/tmuxserver"
	"github.com/zigai/sesh/internal/task"
)

const (
	artifactExpectedGotmuxVersion = "v0.7.4"
	artifactExpectedGotmuxSum     = "h1:O05yGQB9Xb5DZE6vX6ExWKrzLCTRQ6VC7OsCLBd9b7Q="
	exitCodeGeneral               = 1
	jsonSchemaVersion             = 2
)

type snapshotSaveResultJSON struct {
	Attempt    string `json:"attempt"`
	Generation string `json:"generation"`
	Status     string `json:"status"`
}

type snapshotRestoreResultJSON struct {
	OperationID string `json:"operationId"`
	Generation  string `json:"generation"`
	Status      string `json:"status"`
	Verified    bool   `json:"verified"`
}

type artifactBinaryFixture struct {
	binary  string
	workDir string
	env     []string
}

type artifactSessionStructure struct {
	Name    string
	Windows []artifactWindowStructure
}

type artifactWindowStructure struct {
	Index  int
	Name   string
	Active bool
	Panes  []artifactPaneStructure
}

type artifactPaneStructure struct {
	Index  int
	Width  int
	Height int
	Left   int
	Top    int
	Active bool
}

func TestBuiltArtifactSavesRestoresAndRejectsCorruptNativeRepository(t *testing.T) {
	fixture := newArtifactBinaryFixture(t)
	source, sourceSession, wantStructure := newArtifactSource(t)
	repository := filepath.Join(t.TempDir(), "repository")
	leaseRoot := filepath.Join(t.TempDir(), "leases")

	generation := saveThroughArtifact(t, fixture, repository, leaseRoot, source, sourceSession)
	if err := source.Kill(t.Context()); err != nil {
		t.Fatalf("destroy source server: %v", err)
	}

	if _, err := source.Sessions(t.Context()); err == nil {
		t.Fatal("source server remained reachable before new-process restore")
	}

	target := tmuxtest.NewServer(t)
	restoreThroughArtifact(t, fixture, repository, leaseRoot, generation, target, wantStructure)
	rejectCorruptRepositoryWithoutTargetMutation(t, fixture, repository, leaseRoot, generation)
}

func TestBuiltArtifactTaskActionReloadsSelectedConfig(t *testing.T) {
	fixture, server, popup, store := newTaskActionArtifactFixture(t)

	for _, selection := range []string{"absolute", "relative", "stdin"} {
		t.Run(selection, func(t *testing.T) {
			runConfiguredTaskAction(t, fixture, server, popup, store, selection)
		})
	}
}

func newTaskActionArtifactFixture(t *testing.T) (artifactBinaryFixture, *gotmux.Server, *gotmux.Server, task.Store) {
	t.Helper()

	fixture := newArtifactBinaryFixture(t)
	server := tmuxtest.NewServer(t)
	tmuxDir := filepath.Dir(server.Endpoint().SocketPath)
	t.Setenv("TMUX_TMPDIR", tmuxDir)
	fixture.env = append(fixture.env, "TMUX_TMPDIR="+tmuxDir, "SHELL=/bin/sh")

	for _, entry := range fixture.env {
		name, value, _ := strings.Cut(entry, "=")
		t.Setenv(name, value)
	}

	popup, err := tmuxserver.OpenNamed(server.Endpoint().SocketPath, "actions", "/dev/null")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := popup.Kill(ctx); err != nil && !errors.Is(err, gotmux.ErrNoServer) {
			t.Errorf("kill task action popup: %v", err)
		}
	})

	if _, err := popup.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name: "fixture", Dir: fixture.workDir, Program: gotmux.Exec("sleep", "300"),
		Start: gotmux.StartPolicyAllowStart,
	}); err != nil {
		t.Fatal(err)
	}

	if err := popup.GlobalSessionOptions().SetDefaultShell(t.Context(), "/bin/sh"); err != nil {
		t.Fatal(err)
	}

	var stateHome string

	for _, entry := range fixture.env {
		if value, ok := strings.CutPrefix(entry, "XDG_STATE_HOME="); ok {
			stateHome = value
		}
	}

	store := task.NewStore(filepath.Join(stateHome, "sesh", "tasks.json"))

	return fixture, server, popup, store
}

func runConfiguredTaskAction(t *testing.T, fixture artifactBinaryFixture, server, popup *gotmux.Server, store task.Store, selection string) {
	t.Helper()

	directory := t.TempDir()
	marker := filepath.Join(directory, "action-marker")
	configPath := filepath.Join(fixture.workDir, selection+" config.toml")

	source := fmt.Sprintf("[tasks]\npopup_socket = 'actions'\n[task_actions.mark]\nrun = ['/bin/sh', '-c', 'printf selected > \"$1\"', 'mark', %q]\n", marker)
	if err := os.WriteFile(configPath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	switch selection {
	case "relative":
		configPath = filepath.Base(configPath)
	case "stdin":
		configPath = "-"
	}

	record := task.EmptyRecord()
	record.ID, record.Kind, record.State, record.Directory = "t1", task.KindLoose, task.StateActive, directory

	if _, err := store.Update(t.Context(), func(ledger *task.Ledger) error {
		ledger.Put(record)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(t.Context(), fixture.binary, "task", "run", record.ID, "mark", "--socket", server.Endpoint().SocketPath, "--config", configPath)

	command.Dir, command.Env, command.Stdin = fixture.workDir, fixture.env, strings.NewReader(source)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("start task action: %v\n%s", err, output)
	}

	waitForTaskActionResult(t, popup, store, record, marker)
}

func waitForTaskActionResult(t *testing.T, popup *gotmux.Server, store task.Store, record task.Record, marker string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for {
		ledger, err := store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		finished, _ := ledger.Find(record.ID)
		if len(finished.Runs) == 1 && finished.Runs[0].Status == task.RunOK {
			content, err := os.ReadFile(marker)
			if err != nil || string(content) != "selected" {
				t.Fatalf("action marker = %q, error = %v; want selected", content, err)
			}

			if finished.Runs[0].ExitCode != 0 {
				t.Fatalf("action exit code = %d, want 0", finished.Runs[0].ExitCode)
			}

			return
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			panes, _ := popup.Panes(t.Context())

			var output []byte

			for _, pane := range panes {
				captured, _ := pane.Handle().Capture(t.Context(), gotmux.CaptureOptions{})
				output = append(output, captured...)
			}

			t.Fatalf("spawned task action did not finish: runs=%+v; popup output:\n%s", finished.Runs, output)
		}
	}
}

func newArtifactBinaryFixture(t *testing.T) artifactBinaryFixture {
	t.Helper()

	repositoryRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}

	root := t.TempDir()
	binary := filepath.Join(root, "bin", "sesh")
	workDir := filepath.Join(root, "outside-checkout")

	paths := []string{
		filepath.Dir(binary),
		workDir,
		filepath.Join(root, "home"),
		filepath.Join(root, "config"),
		filepath.Join(root, "data"),
		filepath.Join(root, "state"),
		filepath.Join(root, "cache"),
		filepath.Join(root, "tmp"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("create isolated artifact directory: %v", err)
		}
	}

	build := exec.CommandContext(t.Context(), "go", "build", "-mod=readonly", "-o", binary, "./cmd/sesh")
	build.Dir = repositoryRoot
	build.Env = environmentWithGOWORKOff()

	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build standalone sesh artifact: %v\n%s", err, output)
	}

	assertArtifactUsesPublishedGotmux(t, binary)

	return artifactBinaryFixture{
		binary:  binary,
		workDir: workDir,
		env: []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + filepath.Join(root, "home"),
			"XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
			"XDG_DATA_HOME=" + filepath.Join(root, "data"),
			"XDG_STATE_HOME=" + filepath.Join(root, "state"),
			"XDG_CACHE_HOME=" + filepath.Join(root, "cache"),
			"TMPDIR=" + filepath.Join(root, "tmp"),
			"GOWORK=off",
			"TERM=dumb",
			"NO_COLOR=1",
			"LANG=C.UTF-8",
		},
	}
}

func environmentWithGOWORKOff() []string {
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name != "GOWORK" {
			environment = append(environment, entry)
		}
	}

	return append(environment, "GOWORK=off")
}

func assertArtifactUsesPublishedGotmux(t *testing.T, binary string) {
	t.Helper()

	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		t.Fatalf("read artifact build metadata: %v", err)
	}

	for _, dependency := range info.Deps {
		if dependency.Path != "github.com/zigai/gotmux" {
			continue
		}

		if dependency.Version != artifactExpectedGotmuxVersion || dependency.Sum != artifactExpectedGotmuxSum || dependency.Replace != nil {
			t.Fatalf(
				"artifact gotmux dependency = %s sum=%s replace=%v, want published %s %s",
				dependency.Version, dependency.Sum, dependency.Replace,
				artifactExpectedGotmuxVersion, artifactExpectedGotmuxSum,
			)
		}

		return
	}

	t.Fatal("artifact build metadata does not include gotmux")
}

func newArtifactSource(t *testing.T) (*gotmux.Server, gotmux.Session, artifactSessionStructure) {
	t.Helper()

	server := newCaptureCLIServer(t)

	sessions, err := server.Sessions(t.Context())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("observe artifact source sessions: count=%d error=%v", len(sessions), err)
	}

	session := sessions[0].Handle()
	if err := session.Rename(t.Context(), "artifact-native-source"); err != nil {
		t.Fatalf("rename artifact source session: %v", err)
	}

	links, err := session.Windows(t.Context())
	if err != nil || len(links) != 1 {
		t.Fatalf("observe initial source window: count=%d error=%v", len(links), err)
	}

	if err := links[0].Window().Rename(t.Context(), "editor"); err != nil {
		t.Fatalf("rename initial source window: %v", err)
	}

	firstPane, err := links[0].Window().ActivePane(t.Context())
	if err != nil {
		t.Fatalf("observe initial source pane: %v", err)
	}

	if _, err := firstPane.Split(t.Context(), gotmux.SplitOptions{Direction: gotmux.DirectionVertical}); err != nil {
		t.Fatalf("split source window: %v", err)
	}

	sparseIndex := 3
	if _, err := session.NewWindow(t.Context(), gotmux.NewWindowOptions{
		Name:   "terminal",
		Index:  &sparseIndex,
		Select: false,
	}); err != nil {
		t.Fatalf("create sparse source window: %v", err)
	}

	return server, session, observeArtifactSession(t, server, "artifact-native-source")
}

func observeArtifactSession(t *testing.T, server *gotmux.Server, name string) artifactSessionStructure {
	t.Helper()

	session, err := server.FindSession(t.Context(), name)
	if err != nil {
		t.Fatalf("find observed session: %v", err)
	}

	links, err := session.Windows(t.Context())
	if err != nil {
		t.Fatalf("observe session windows: %v", err)
	}

	structure := artifactSessionStructure{Name: name, Windows: make([]artifactWindowStructure, 0, len(links))}
	for _, link := range links {
		info, err := link.Info(t.Context())
		if err != nil {
			t.Fatalf("observe window link: %v", err)
		}

		panes, err := link.Window().Panes(t.Context())
		if err != nil {
			t.Fatalf("observe window panes: %v", err)
		}

		window := artifactWindowStructure{
			Index:  info.Index,
			Name:   info.WindowName,
			Active: info.Active,
			Panes:  make([]artifactPaneStructure, 0, len(panes)),
		}
		for _, pane := range panes {
			window.Panes = append(window.Panes, artifactPaneStructure{
				Index: pane.Index, Width: pane.Width, Height: pane.Height,
				Left: pane.Left, Top: pane.Top, Active: pane.Active,
			})
		}

		slices.SortFunc(window.Panes, func(left, right artifactPaneStructure) int { return left.Index - right.Index })
		structure.Windows = append(structure.Windows, window)
	}

	slices.SortFunc(structure.Windows, func(left, right artifactWindowStructure) int { return left.Index - right.Index })

	return structure
}

func saveThroughArtifact(
	t *testing.T,
	fixture artifactBinaryFixture,
	repository, leaseRoot string,
	server *gotmux.Server,
	session gotmux.Session,
) string {
	t.Helper()

	stdout, stderr := fixture.runSuccess(t,
		"snapshot", "save",
		testRepositoryFlag, repository,
		testLeaseRootFlag, leaseRoot,
		testSocketFlag, server.Endpoint().SocketPath,
		string(session.ID()),
		testNoConfigFlag,
		testJSONFlag,
	)
	if len(stderr) != 0 {
		t.Fatalf("artifact save wrote stderr: %s", stderr)
	}

	envelope := decodeArtifactEnvelope(t, stdout)

	result := decodeJSONPayload[snapshotSaveResultJSON](t, envelope.Result)
	if envelope.Command != "snapshot.save" || !envelope.OK || envelope.Error != nil || result.Status != "best_effort" || result.Generation == "" || result.Attempt == "" {
		t.Fatalf("artifact save lost practical receipt: envelope=%+v result=%+v", envelope, result)
	}

	return result.Generation
}

func restoreThroughArtifact(
	t *testing.T,
	fixture artifactBinaryFixture,
	repository, leaseRoot, generation string,
	target *gotmux.Server,
	want artifactSessionStructure,
) {
	t.Helper()

	stdout, stderr := fixture.runSuccess(t,
		"snapshot", "restore",
		testRepositoryFlag, repository,
		testLeaseRootFlag, leaseRoot,
		testSocketFlag, target.Endpoint().SocketPath,
		testGenerationFlag, generation,
		"--no-processes",
		testNoConfigFlag,
		testJSONFlag,
	)
	if len(stderr) != 0 {
		t.Fatalf("artifact restore wrote stderr: %s", stderr)
	}

	envelope := decodeArtifactEnvelope(t, stdout)

	result := decodeJSONPayload[snapshotRestoreResultJSON](t, envelope.Result)
	if envelope.Command != "snapshot.restore" || !envelope.OK || envelope.Error != nil || result.OperationID == "" || result.Generation != generation || result.Status != "best_effort" || !result.Verified {
		t.Fatalf("artifact restore lost practical operation receipt: envelope=%+v result=%+v", envelope, result)
	}

	got := observeArtifactSession(t, target, want.Name)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("standalone artifact restored different tmux structure (-want +got):\n%s", diff)
	}
}

func rejectCorruptRepositoryWithoutTargetMutation(
	t *testing.T,
	fixture artifactBinaryFixture,
	repository, leaseRoot, generation string,
) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(repository, "catalog.json"), []byte("pane\tlegacy\n"), 0o600); err != nil {
		t.Fatalf("replace native catalog with non-native bytes: %v", err)
	}

	target := tmuxtest.NewServer(t)
	before := artifactServerIdentity(t, target)

	stdout, stderr := fixture.runExitOne(t,
		"snapshot", "restore",
		testRepositoryFlag, repository,
		testLeaseRootFlag, leaseRoot,
		testSocketFlag, target.Endpoint().SocketPath,
		testGenerationFlag, generation,
		"--no-processes",
		testNoConfigFlag,
		testJSONFlag,
	)
	if len(stdout) != 0 {
		t.Fatalf("corrupt repository rejection wrote stdout: %s", stdout)
	}

	envelope := decodeArtifactEnvelope(t, stderr)
	if envelope.Command != "snapshot.restore" || envelope.OK || envelope.Error == nil || envelope.Error.Code != "repositoryCorrupt" || envelope.Error.ExitCode != exitCodeGeneral {
		t.Fatalf("corrupt repository was not rejected: %+v", envelope)
	}

	after := artifactServerIdentity(t, target)
	if diff := cmp.Diff(before, after); diff != "" {
		t.Fatalf("corrupt repository rejection mutated target tmux (-before +after):\n%s", diff)
	}
}

func artifactServerIdentity(t *testing.T, server *gotmux.Server) []string {
	t.Helper()

	sessions, err := server.Sessions(t.Context())
	if err != nil {
		t.Fatalf("observe target sessions: %v", err)
	}

	identity := make([]string, 0)
	for _, session := range sessions {
		identity = append(identity, fmt.Sprintf("session:%s:%s", session.ID, session.Name))

		links, err := session.Handle().Windows(t.Context())
		if err != nil {
			t.Fatalf("observe target windows: %v", err)
		}

		for _, link := range links {
			info, err := link.Info(t.Context())
			if err != nil {
				t.Fatalf("observe target link: %v", err)
			}

			identity = append(identity, fmt.Sprintf("window:%s:%s:%d", info.WindowID, info.WindowName, info.Index))

			panes, err := link.Window().Panes(t.Context())
			if err != nil {
				t.Fatalf("observe target panes: %v", err)
			}

			for _, pane := range panes {
				identity = append(identity, fmt.Sprintf("pane:%s:%d:%d:%d:%d:%d", pane.ID, pane.Index, pane.Left, pane.Top, pane.Width, pane.Height))
			}
		}
	}

	sort.Strings(identity)

	return identity
}

func (fixture artifactBinaryFixture) runExitOne(t *testing.T, args ...string) ([]byte, []byte) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	command := exec.CommandContext(t.Context(), fixture.binary, args...)
	command.Dir = fixture.workDir
	command.Env = fixture.env
	command.Stdout = &stdout
	command.Stderr = &stderr

	err := command.Run()

	exitErr, ok := errors.AsType[*exec.ExitError](err)
	if !ok || exitErr.ExitCode() != exitCodeGeneral {
		t.Fatalf("artifact command exit = %v, want %d; stdout=%s stderr=%s", err, exitCodeGeneral, stdout.String(), stderr.String())
	}

	return stdout.Bytes(), stderr.Bytes()
}

func (fixture artifactBinaryFixture) runSuccess(t *testing.T, args ...string) ([]byte, []byte) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	command := exec.CommandContext(t.Context(), fixture.binary, args...)
	command.Dir = fixture.workDir
	command.Env = fixture.env
	command.Stdout = &stdout
	command.Stderr = &stderr

	if err := command.Run(); err != nil {
		t.Fatalf("artifact command failed: %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	return stdout.Bytes(), stderr.Bytes()
}

func decodeArtifactEnvelope(t *testing.T, data []byte) testJSONEnvelope {
	t.Helper()

	if len(bytes.TrimSpace(data)) == 0 {
		t.Fatal("artifact command returned no JSON envelope")
	}

	var envelope testJSONEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("artifact command did not return exactly one JSON envelope: %v\n%s", err, data)
	}

	if envelope.SchemaVersion != jsonSchemaVersion {
		t.Fatalf("artifact JSON schema version = %d, want %d", envelope.SchemaVersion, jsonSchemaVersion)
	}

	return envelope
}
