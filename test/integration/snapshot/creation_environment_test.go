//go:build integration && (linux || darwin)

package snapshot_test

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"
)

func TestSessionCreationMarkerIsSessionScopedAndIndependentlyQueryable(t *testing.T) {
	server := tmuxtest.NewServer(t)
	helper := buildLaunchHelper(t)

	dir := t.TempDir()
	report := filepath.Join(dir, "session.report")

	testArgs := []string{
		"-report", report,
		"-hold", "300s",
		"literal with spaces and \t tabs",
		"argv",
		"pid",
		"env",
		"SESH_OPERATION=decoy",
		"",
	}

	session, err := server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:    "marker-owner",
		Dir:     dir,
		Program: tmux.Exec(helper, testArgs...),
		TmuxEnv: map[string]string{sessionMarker: sessionToken},
		Size:    tmux.Size{Width: 100, Height: 30},
		Start:   tmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	requireSessionMarker(t, session)

	requireListedEnvironment(t, session, sessionMarker)

	requireNoGlobalEnvironment(t, server, sessionMarker)

	launched := awaitProcessReport(t, report)
	requireProcessEnvironment(t, launched, sessionMarker, sessionToken)

	if want := append([]string{helper}, testArgs...); !slices.Equal(launched.Argv, want) {
		t.Fatalf("initial argv = %q, want %q", launched.Argv, want)
	}

	panes, err := session.Panes(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(panes) != 1 || panes[0].PID != launched.PID {
		t.Fatalf("pane PIDs %v do not match the launched process %d", panes, launched.PID)
	}

	unrelatedReport := filepath.Join(dir, "unrelated.report")

	unrelated, err := server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:    "marker-unrelated",
		Dir:     dir,
		Program: tmux.Exec(helper, "-report", unrelatedReport, "-hold", "300s"),
		Size:    tmux.Size{Width: 100, Height: 30},
		Start:   tmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	requireNoSessionMarker(t, unrelated)
	requireNoProcessEnvironment(t, awaitProcessReport(t, unrelatedReport), sessionMarker)
}

func TestSessionMarkerSurvivesInitialProcessExitAndRename(t *testing.T) {
	server := tmuxtest.NewServer(t)
	helper := buildLaunchHelper(t)

	dir := t.TempDir()

	session, err := server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:    "survivor",
		Dir:     dir,
		Program: tmux.Exec(helper, "-report", filepath.Join(dir, "initial.report"), "-hold", "2s"),
		TmuxEnv: map[string]string{sessionMarker: sessionToken},
		Size:    tmux.Size{Width: 100, Height: 30},
		Start:   tmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	identity := session.ID()

	keeperReport := filepath.Join(dir, "keeper.report")

	if _, err = session.NewWindow(t.Context(), tmux.NewWindowOptions{
		Name:    "keeper",
		Dir:     dir,
		Program: tmux.Exec(helper, "-report", keeperReport, "-hold", "300s"),
	}); err != nil {
		t.Fatal(err)
	}

	requireProcessEnvironment(t, awaitProcessReport(t, keeperReport), sessionMarker, sessionToken)

	awaitWindowCount(t, session, 1)

	panes, err := session.Panes(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(panes) != 1 {
		t.Fatalf("session retained %d panes after the initial process exited", len(panes))
	}

	requireSessionMarker(t, session)

	if err = session.Rename(t.Context(), "survivor-renamed"); err != nil {
		t.Fatal(err)
	}

	info, err := session.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if info.ID != identity || info.Name != "survivor-renamed" {
		t.Fatalf("rename changed identity=%s name=%q, want %s/%q", info.ID, info.Name, identity, "survivor-renamed")
	}

	requireSessionMarker(t, session)

	if _, err = server.FindSession(t.Context(), "survivor"); err == nil {
		t.Fatal("renamed session still answers to its previous name")
	}

	found, err := server.FindSession(t.Context(), "survivor-renamed")
	if err != nil {
		t.Fatal(err)
	}

	if found.ID() != identity {
		t.Fatalf("name lookup resolved %s, want %s", found.ID(), identity)
	}
}

func TestWindowAndPaneNativeEnvironmentIsOneShotAndProcessLocal(t *testing.T) {
	server := tmuxtest.NewServer(t)
	helper := buildLaunchHelper(t)

	dir := t.TempDir()

	session, err := server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:    "one-shot",
		Dir:     dir,
		Program: tmux.Exec("sleep", "300"),
		TmuxEnv: map[string]string{sessionMarker: sessionToken},
		Size:    tmux.Size{Width: 120, Height: 40},
		Start:   tmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	markedReport := filepath.Join(dir, "window.report")

	if _, err = session.NewWindow(t.Context(), tmux.NewWindowOptions{
		Name:    "marked",
		Dir:     dir,
		Program: tmux.Exec(helper, "-report", markedReport, "-hold", "300s"),
		TmuxEnv: map[string]string{windowMarker: windowToken},
	}); err != nil {
		t.Fatal(err)
	}

	markedProcess := awaitProcessReport(t, markedReport)
	requireProcessEnvironment(t, markedProcess, windowMarker, windowToken)
	requireProcessEnvironment(t, markedProcess, sessionMarker, sessionToken)

	requireNoTmuxEnvironment(t, server, windowMarker, session)

	plainReport := filepath.Join(dir, "plain.report")

	plain, err := session.NewWindow(t.Context(), tmux.NewWindowOptions{
		Name:    "plain",
		Dir:     dir,
		Program: tmux.Exec(helper, "-report", plainReport, "-hold", "300s"),
	})
	if err != nil {
		t.Fatal(err)
	}

	plainProcess := awaitProcessReport(t, plainReport)
	requireProcessEnvironment(t, plainProcess, sessionMarker, sessionToken)
	requireNoProcessEnvironment(t, plainProcess, windowMarker)

	pane, err := plain.Window().ActivePane(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	splitReport := filepath.Join(dir, "split.report")

	if _, err = pane.Split(t.Context(), tmux.SplitOptions{
		Direction: tmux.DirectionVertical,
		Size:      tmux.SplitSize{Cells: 10},
		Dir:       dir,
		Program:   tmux.Exec(helper, "-report", splitReport, "-hold", "300s"),
		TmuxEnv:   map[string]string{paneMarker: paneToken},
	}); err != nil {
		t.Fatal(err)
	}

	splitProcess := awaitProcessReport(t, splitReport)
	requireProcessEnvironment(t, splitProcess, paneMarker, paneToken)
	requireProcessEnvironment(t, splitProcess, sessionMarker, sessionToken)
	requireNoProcessEnvironment(t, splitProcess, windowMarker)
	requireNoTmuxEnvironment(t, server, paneMarker, session)

	secondSplitReport := filepath.Join(dir, "second-split.report")

	if _, err = pane.Split(t.Context(), tmux.SplitOptions{
		Direction: tmux.DirectionVertical,
		Size:      tmux.SplitSize{Cells: 10},
		Dir:       dir,
		Program:   tmux.Exec(helper, "-report", secondSplitReport, "-hold", "300s"),
	}); err != nil {
		t.Fatal(err)
	}

	secondSplit := awaitProcessReport(t, secondSplitReport)
	requireNoProcessEnvironment(t, secondSplit, paneMarker)
	requireProcessEnvironment(t, secondSplit, sessionMarker, sessionToken)
}

func TestCreationStartCommandIsDurableButNotTheLiteralArgv(t *testing.T) {
	server := tmuxtest.NewServer(t)
	helper := buildLaunchHelper(t)

	session, launched, dead := recordedLaunchFixture(t, server, helper)

	requireProcessEnvironment(t, launched, windowMarker, windowToken)
	requireProcessEnvironment(t, launched, processMarker, processToken)

	recorded, present := paneStartCommand(t, server, dead.ID)
	if !present {
		t.Fatal("tmux dropped the launch record of a dead pane")
	}

	if dead.PID != launched.PID {
		t.Fatalf("dead pane PID %d, want %d", dead.PID, launched.PID)
	}

	if !strings.HasPrefix(recorded, "/bin/sh -c ") {
		t.Fatalf("launch record is not the documented wrapper: %q", recorded)
	}

	if !strings.Contains(recorded, processMarker+"="+processToken) {
		t.Fatalf("launch record %q omits the process environment override", recorded)
	}

	if strings.HasPrefix(recorded, helper) {
		t.Fatalf("launch record %q unexpectedly holds the literal argv", recorded)
	}

	requireNoTmuxEnvironment(t, server, windowMarker, session)
}

func TestRespawnReusesPaneIdentityAndRewritesTheLaunchRecord(t *testing.T) {
	server := tmuxtest.NewServer(t)
	helper := buildLaunchHelper(t)

	session, _, dead := recordedLaunchFixture(t, server, helper)

	recorded, present := paneStartCommand(t, server, dead.ID)
	if !present {
		t.Fatal("tmux dropped the launch record of a dead pane")
	}

	pane := dead.Handle()

	dir := t.TempDir()
	report := filepath.Join(dir, "respawned.report")

	if err := pane.Respawn(t.Context(), tmux.RespawnOptions{
		Program: tmux.Exec(helper, "-report", report, "-hold", "300s"),
		Dir:     dir,
	}); err != nil {
		t.Fatal(err)
	}

	respawned := awaitProcessReport(t, report)

	requireProcessEnvironment(t, respawned, sessionMarker, sessionToken)
	requireNoProcessEnvironment(t, respawned, windowMarker)
	requireNoProcessEnvironment(t, respawned, processMarker)

	rewritten, present := paneStartCommand(t, server, dead.ID)
	if !present {
		t.Fatal("respawned pane lost its launch record")
	}

	if strings.Contains(rewritten, processMarker) || rewritten == recorded {
		t.Fatalf("respawn did not rewrite the launch record: %q", rewritten)
	}

	paneInfo, err := pane.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if paneInfo.ID != dead.ID || paneInfo.PID != respawned.PID {
		t.Fatalf("respawn changed pane identity %s/%d, want %s/%d", paneInfo.ID, paneInfo.PID, dead.ID, respawned.PID)
	}

	requireNoTmuxEnvironment(t, server, windowMarker, session)
}

func TestFailedCreationLeaksNoMarkerAndReportsNoIdentity(t *testing.T) {
	server := tmuxtest.NewServer(t)

	const refused = "op-refused"

	existing, err := server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:    "duplicate-slot",
		Program: tmux.Exec("sleep", "300"),
		TmuxEnv: map[string]string{sessionMarker: sessionToken},
		Start:   tmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	before, err := server.Sessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	_, err = server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:    "duplicate-slot",
		Program: tmux.Exec("sleep", "300"),
		TmuxEnv: map[string]string{sessionMarker: refused},
		Start:   tmux.StartPolicyExistingOnly,
	})
	if err == nil {
		t.Fatal("creating a duplicate session name succeeded")
	}

	var opErr *tmux.OperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("duplicate creation returned %T, want *tmux.OperationError", err)
	}

	if opErr.Operation != "NewSession" {
		t.Fatalf("failed operation reported as %q", opErr.Operation)
	}

	if len(opErr.Outcome.Created) != 0 {
		t.Fatalf("failed creation recovered %d created objects, want none", len(opErr.Outcome.Created))
	}

	t.Logf("duplicate creation failed with effect %v (gotmux v0.4.0)", opErr.Outcome.Effect)
	requireSessionMarker(t, existing)

	after, err := server.Sessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(after) != len(before) {
		t.Fatalf("session count changed from %d to %d", len(before), len(after))
	}

	if matches := sessionsWithMarker(t, server, refused); len(matches) != 0 {
		t.Fatalf("refused marker present on %d sessions", len(matches))
	}
}
