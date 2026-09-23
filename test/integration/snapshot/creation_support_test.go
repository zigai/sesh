//go:build integration && (linux || darwin)

package snapshot_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zigai/gotmux/tmux"
)

const (
	launchHelperPackage = "./testdata/creation/launchhelper"

	maxReportFileSize       = 1 << 20
	maxReportSectionEntries = 4096
	sessionMarker           = "SESH_OPERATION"
	windowMarker            = "SESH_WINDOW_OPERATION"
	paneMarker              = "SESH_PANE_OPERATION"
	processMarker           = "SESH_PROCESS_OPERATION"

	sessionToken = "op-2c40"
	windowToken  = "op-9ab1"
	paneToken    = "op-d3e7"
	processToken = "op-b7c2"
)

var errReportTooLarge = errors.New("report file exceeds maximum allowed size")

func buildLaunchHelper(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "launchhelper")

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "build", "-o", path, launchHelperPackage)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build launch helper: %v: %s", err, out)
	}

	return path
}

type processReport struct {
	PID  int
	Argv []string
	Env  []string
}

func readBoundedReportFile(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open report %s: %w", path, err)
	}
	defer func() {
		_ = f.Close()
	}()

	lr := io.LimitReader(f, maxBytes+1)

	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, fmt.Errorf("read report %s: %w", path, err)
	}

	if int64(len(data)) > maxBytes {
		return nil, errReportTooLarge
	}

	return data, nil
}

func awaitProcessReport(t *testing.T, path string) processReport {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	var lastErr error

	for {
		data, err := readBoundedReportFile(path, maxReportFileSize)
		if err == nil {
			return parseProcessReport(t, path, data)
		}

		if errors.Is(err, errReportTooLarge) {
			t.Fatalf("launch report %s exceeds maximum size %d bytes", path, maxReportFileSize)
		}

		lastErr = err

		select {
		case <-ctx.Done():
			t.Fatalf("no launch report at %s: %v", path, lastErr)
		case <-ticker.C:
		}
	}
}

func popNULTerminated(data []byte, idx *int) (string, bool) {
	if *idx >= len(data) {
		return "", false
	}

	pos := bytes.IndexByte(data[*idx:], 0)
	if pos == -1 {
		return "", false
	}

	val := string(data[*idx : *idx+pos])
	*idx += pos + 1

	return val, true
}

func applyReportSection(t *testing.T, path, name string, entries []string, report *processReport) {
	t.Helper()

	switch name {
	case "pid":
		if len(entries) != 1 {
			t.Fatalf("malformed launch report pid section in %s: expected 1 entry, got %d", path, len(entries))
		}

		pid, err := strconv.Atoi(entries[0])
		if err != nil || pid <= 0 {
			t.Fatalf("malformed pid in %s: %q", path, entries[0])
		}

		report.PID = pid
	case "argv":
		report.Argv = entries
	case "env":
		report.Env = entries
	default:
		t.Fatalf("unknown section %q in %s", name, path)
	}
}

func readSectionEntries(t *testing.T, path, name string, data []byte, idx *int) []string {
	t.Helper()

	countStr, ok := popNULTerminated(data, idx)
	if !ok {
		t.Fatalf("malformed launch report at %s: unexpected EOF reading section %s count", path, name)
	}

	count, err := strconv.Atoi(countStr)
	if err != nil || count < 0 || count > maxReportSectionEntries {
		t.Fatalf("malformed launch report section %s count in %s: %q (max %d)", name, path, countStr, maxReportSectionEntries)
	}

	entries := make([]string, count)

	for i := range count {
		entry, ok := popNULTerminated(data, idx)
		if !ok {
			t.Fatalf("malformed launch report section %s entry %d in %s: unexpected EOF", name, i, path)
		}

		entries[i] = entry
	}

	return entries
}

func parseProcessReport(t *testing.T, path string, data []byte) processReport {
	t.Helper()

	var report processReport

	idx := 0
	sectionsSeen := make(map[string]bool)

	for idx < len(data) {
		name, ok := popNULTerminated(data, &idx)
		if !ok {
			t.Fatalf("malformed launch report at %s: unexpected EOF reading section name", path)
		}

		if sectionsSeen[name] {
			t.Fatalf("duplicate section %q in launch report %s", name, path)
		}

		sectionsSeen[name] = true
		entries := readSectionEntries(t, path, name, data, &idx)

		applyReportSection(t, path, name, entries, &report)
	}

	if !sectionsSeen["pid"] || !sectionsSeen["argv"] || !sectionsSeen["env"] || report.PID <= 0 || len(report.Argv) == 0 {
		t.Fatalf("incomplete launch report at %s (pid=%d, argv=%d, env=%d)", path, report.PID, len(report.Argv), len(report.Env))
	}

	return report
}

func processEnvironment(report processReport, name string) (string, bool) {
	prefix := name + "="

	for _, entry := range report.Env {
		if value, ok := strings.CutPrefix(entry, prefix); ok {
			return value, true
		}
	}

	return "", false
}

func requireProcessEnvironment(t *testing.T, report processReport, name, want string) {
	t.Helper()

	got, present := processEnvironment(report, name)
	if !present || got != want {
		t.Fatalf("process %d environment %s = %q (present=%v), want %q", report.PID, name, got, present, want)
	}
}

func requireNoProcessEnvironment(t *testing.T, report processReport, name string) {
	t.Helper()

	if got, present := processEnvironment(report, name); present {
		t.Fatalf("process %d unexpectedly inherited %s=%q", report.PID, name, got)
	}
}

func sessionEnvironment(t *testing.T, session tmux.Session, name string) (string, bool) {
	t.Helper()

	value, err := session.Environment().Get(t.Context(), name, false)
	if err != nil {
		t.Fatalf("read session environment %s: %v", name, err)
	}

	return value.Value.Get()
}

func requireSessionMarker(t *testing.T, session tmux.Session) {
	t.Helper()

	got, present := sessionEnvironment(t, session, sessionMarker)
	if !present || got != sessionToken {
		t.Fatalf("session %s environment %s = %q (present=%v), want %q", session.ID(), sessionMarker, got, present, sessionToken)
	}
}

func requireNoSessionMarker(t *testing.T, session tmux.Session) {
	t.Helper()

	if got, present := sessionEnvironment(t, session, sessionMarker); present {
		t.Fatalf("session %s unexpectedly carries %s=%q", session.ID(), sessionMarker, got)
	}
}

func requireNoGlobalEnvironment(t *testing.T, server *tmux.Server, name string) {
	t.Helper()

	requireNoEnvironmentInScope(t, "global", server.Environment(), name)
}

func requireNoTmuxEnvironment(t *testing.T, server *tmux.Server, name string, sessions ...tmux.Session) {
	t.Helper()

	scopes := make([]struct {
		label string
		scope tmux.EnvironmentScope
	}, 0, len(sessions)+1)
	scopes = append(scopes, struct {
		label string
		scope tmux.EnvironmentScope
	}{"global", server.Environment()})

	for _, session := range sessions {
		scopes = append(scopes, struct {
			label string
			scope tmux.EnvironmentScope
		}{"session " + string(session.ID()), session.Environment()})
	}

	for _, entry := range scopes {
		requireNoEnvironmentInScope(t, entry.label, entry.scope, name)
	}
}

func requireNoEnvironmentInScope(t *testing.T, label string, scope tmux.EnvironmentScope, name string) {
	t.Helper()

	for _, hidden := range []bool{false, true} {
		value, err := scope.Get(t.Context(), name, hidden)
		if err != nil {
			t.Fatalf("read %s environment %s: %v", label, name, err)
		}

		if got, present := value.Value.Get(); present {
			t.Fatalf("%s environment carries %s=%q (hidden=%v)", label, name, got, hidden)
		}

		if value.Unset {
			t.Fatalf("%s environment marks %s removed", label, name)
		}
	}
}

func requireListedEnvironment(t *testing.T, session tmux.Session, name string) {
	t.Helper()

	entries, err := session.Environment().List(t.Context())
	if err != nil {
		t.Fatalf("list session environment: %v", err)
	}

	for _, entry := range entries {
		if entry.Name == name {
			return
		}
	}

	t.Fatalf("session %s environment listing of %d entries omits %s", session.ID(), len(entries), name)
}

func sessionsWithMarker(t *testing.T, server *tmux.Server, value string) []tmux.Session {
	t.Helper()

	infos, err := server.Sessions(t.Context())
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}

	var matches []tmux.Session

	for _, info := range infos {
		got, present := sessionEnvironment(t, info.Handle(), sessionMarker)
		if present && got == value {
			matches = append(matches, info.Handle())
		}
	}

	return matches
}

func sessionWindowIDs(t *testing.T, session tmux.Session) map[int]tmux.WindowID {
	t.Helper()

	links, err := session.Windows(t.Context())
	if err != nil {
		t.Fatalf("list session %s windows: %v", session.ID(), err)
	}

	out := make(map[int]tmux.WindowID, len(links))

	for _, link := range links {
		out[link.Index()] = link.Window().ID()
	}

	return out
}

func paneStartCommand(t *testing.T, server *tmux.Server, id tmux.PaneID) (string, bool) {
	t.Helper()

	infos, err := server.PanesWith(t.Context(), tmux.QueryOptions{ExtraFields: []string{"pane_start_command"}})
	if err != nil {
		t.Fatalf("query pane start command: %v", err)
	}

	for _, info := range infos {
		if info.ID != id {
			continue
		}

		command, present := info.Raw("pane_start_command")

		return string(command), present
	}

	t.Fatalf("pane %s missing from typed pane query", id)

	return "", false
}

func awaitWindowCount(t *testing.T, session tmux.Session, want int) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for {
		links, err := session.Windows(ctx)
		if err != nil {
			t.Fatalf("list session windows: %v", err)
		}

		if len(links) == want {
			return
		}

		select {
		case <-ctx.Done():
			t.Fatalf("session holds %d windows, want %d", len(links), want)
		case <-ticker.C:
		}
	}
}

func awaitPaneDead(t *testing.T, window tmux.Window, id tmux.PaneID) tmux.PaneInfo {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for {
		panes, err := window.Panes(ctx)
		if err != nil {
			t.Fatalf("list window panes: %v", err)
		}

		for _, pane := range panes {
			if pane.ID == id && pane.Dead {
				return pane
			}
		}

		select {
		case <-ctx.Done():
			t.Fatalf("pane %s never reported a terminated process", id)
		case <-ticker.C:
		}
	}
}

func recordedLaunchFixture(t *testing.T, server *tmux.Server, helper string) (tmux.Session, processReport, tmux.PaneInfo) {
	t.Helper()

	dir := t.TempDir()

	session, err := server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:    "start-command",
		Dir:     dir,
		Program: tmux.Exec("sleep", "300"),
		TmuxEnv: map[string]string{sessionMarker: sessionToken},
		Size:    tmux.Size{Width: 100, Height: 30},
		Start:   tmux.ExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	report := filepath.Join(dir, "recorded.report")

	link, err := session.NewWindow(t.Context(), tmux.NewWindowOptions{
		Name:    "recorded",
		Dir:     dir,
		Program: tmux.Exec(helper, "-report", report, "-hold", "2s"),
		Env:     map[string]string{processMarker: processToken},
		TmuxEnv: map[string]string{windowMarker: windowToken},
	})
	if err != nil {
		t.Fatal(err)
	}

	window := link.Window()

	if err = window.Options().SetRemainOnExit(t.Context(), tmux.RemainOn); err != nil {
		t.Fatal(err)
	}

	pane, err := window.ActivePane(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	dead := awaitPaneDead(t, window, pane.ID())

	return session, awaitProcessReport(t, report), dead
}

func linkedWindowFixture(t *testing.T, server *tmux.Server, helper string) (tmux.Session, tmux.Session, tmux.Window, tmux.WindowLink, processReport) {
	t.Helper()

	dir := t.TempDir()

	owner, err := server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:    "link-owner",
		Dir:     dir,
		Program: tmux.Exec("sleep", "300"),
		TmuxEnv: map[string]string{sessionMarker: sessionToken},
		Size:    tmux.Size{Width: 100, Height: 30},
		Start:   tmux.ExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	other, err := server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:    "link-other",
		Dir:     dir,
		Program: tmux.Exec("sleep", "300"),
		Size:    tmux.Size{Width: 100, Height: 30},
		Start:   tmux.ExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	report := filepath.Join(dir, "shared.report")

	created, err := owner.NewWindow(t.Context(), tmux.NewWindowOptions{
		Name:    "shared-window",
		Dir:     dir,
		Program: tmux.Exec(helper, "-report", report, "-hold", "300s"),
		TmuxEnv: map[string]string{windowMarker: windowToken},
		Index:   new(4),
	})
	if err != nil {
		t.Fatal(err)
	}

	shared := created.Window()

	linked, err := shared.Link(t.Context(), other, tmux.LinkOptions{Index: new(7)})
	if err != nil {
		t.Fatal(err)
	}

	return owner, other, shared, linked, awaitProcessReport(t, report)
}

func restartDaemon(t *testing.T, server *tmux.Server) (tmux.ServerIdentity, tmux.ServerIdentity) {
	t.Helper()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := server.Kill(ctx); err != nil && !errors.Is(err, tmux.ErrNoServer) {
			t.Errorf("cleanup kill restarted daemon: %v", err)
		}
	})

	before, err := server.Probe(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if err = server.Kill(t.Context()); err != nil {
		t.Fatal(err)
	}

	probeCtx, probeCancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer probeCancel()

	for {
		_, err = server.Probe(probeCtx)
		if errors.Is(err, tmux.ErrNoServer) {
			break
		}

		select {
		case <-probeCtx.Done():
			t.Fatalf("probe after kill returned %v, want ErrNoServer", err)
		case <-time.After(20 * time.Millisecond):
		}
	}

	if _, err = server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:    "restart-anchor",
		Program: tmux.Exec("sleep", "300"),
		Start:   tmux.AllowStart,
	}); err != nil {
		t.Fatal(err)
	}

	after, err := server.Probe(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	return before.Identity, after.Identity
}

func windowMapsEqual(a, b map[int]tmux.WindowID) bool {
	if len(a) != len(b) {
		return false
	}

	for index, id := range a {
		if b[index] != id {
			return false
		}
	}

	return true
}

func paneIDs(panes []tmux.PaneInfo) []tmux.PaneID {
	out := make([]tmux.PaneID, 0, len(panes))

	for _, pane := range panes {
		out = append(out, pane.ID)
	}

	return out
}
