//go:build integration && (linux || darwin)

package snapshot_test

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"
)

func TestCreationMarkerReconciliationIsNecessaryButNotUnique(t *testing.T) {
	server := tmuxtest.NewServer(t)

	const name = "adopt-slot"

	if _, err := server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:    name,
		Program: tmux.Exec("sleep", "300"),
		TmuxEnv: map[string]string{sessionMarker: sessionToken},
		Size:    tmux.Size{Width: 100, Height: 30},
		Start:   tmux.ExistingOnly,
	}); err != nil {
		t.Fatal(err)
	}

	matches := sessionsWithMarker(t, server, sessionToken)
	if len(matches) != 1 {
		t.Fatalf("marker matched %d sessions, want 1", len(matches))
	}

	found, err := server.FindSession(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}

	identity := found.ID()

	if matches[0].ID() != identity {
		t.Fatalf("marker matched %s, name resolved %s", matches[0].ID(), identity)
	}

	if err = found.Rename(t.Context(), "adopt-slot-renamed"); err != nil {
		t.Fatal(err)
	}

	if _, err = server.FindSession(t.Context(), name); err == nil {
		t.Fatal("name-based lookup survived a rename")
	}

	matches = sessionsWithMarker(t, server, sessionToken)
	if len(matches) != 1 || matches[0].ID() != identity {
		t.Fatalf("marker matched %v after rename, want %s", matches, identity)
	}

	decoy, err := server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:    "adopt-decoy",
		Program: tmux.Exec("sleep", "300"),
		Start:   tmux.ExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err = decoy.Environment().Set(t.Context(), sessionMarker, sessionToken); err != nil {
		t.Fatal(err)
	}

	if matches = sessionsWithMarker(t, server, sessionToken); len(matches) != 2 {
		t.Fatalf("copied marker matched %d sessions, want 2", len(matches))
	}
}

func TestRemovedMarkerReturnsTheWrongCandidate(t *testing.T) {
	server := tmuxtest.NewServer(t)

	owner, err := server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:    "adopt-slot",
		Program: tmux.Exec("sleep", "300"),
		TmuxEnv: map[string]string{sessionMarker: sessionToken},
		Start:   tmux.ExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	decoy, err := server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:    "adopt-decoy",
		Program: tmux.Exec("sleep", "300"),
		Start:   tmux.ExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err = decoy.Environment().Set(t.Context(), sessionMarker, sessionToken); err != nil {
		t.Fatal(err)
	}

	if err = owner.Environment().Unset(t.Context(), sessionMarker); err != nil {
		t.Fatal(err)
	}

	matches := sessionsWithMarker(t, server, sessionToken)
	if len(matches) != 1 || matches[0].ID() != decoy.ID() {
		t.Fatalf("marker matched %v after removal, want the decoy %s", matches, decoy.ID())
	}
}

func TestIncarnationReuseLeavesTheStaleCreationHandleRejected(t *testing.T) {
	server := tmuxtest.NewServer(t)

	const name = "adopt-slot"

	owner, err := server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:    name,
		Program: tmux.Exec("sleep", "300"),
		TmuxEnv: map[string]string{sessionMarker: sessionToken},
		Start:   tmux.ExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	identity := owner.ID()

	if err = owner.Kill(t.Context()); err != nil {
		t.Fatal(err)
	}

	replacement, err := server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:    name,
		Program: tmux.Exec("sleep", "300"),
		Start:   tmux.ExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	if replacement.ID() == identity {
		t.Fatalf("replacement reused session identity %s", identity)
	}

	requireNoSessionMarker(t, replacement)

	if err = owner.Rename(t.Context(), "adopt-slot-again"); !errors.Is(err, tmux.ErrNotFound) {
		t.Fatalf("stale session handle rename returned %v, want ErrNotFound", err)
	}

	staleValue, staleErr := owner.Environment().Get(t.Context(), sessionMarker, false)

	if value, present := staleValue.Value.Get(); present {
		t.Fatalf("stale session handle read %s=%q from a dead session", sessionMarker, value)
	}

	if staleErr == nil {
		t.Fatal("stale session handle read the environment of a dead session")
	}

	if errors.Is(staleErr, tmux.ErrServerChanged) {
		t.Fatalf("stale session read reported a daemon change: %v", staleErr)
	}

	if !errors.Is(staleErr, tmux.ErrNotFound) {
		var staleOperation *tmux.OperationError
		if !errors.As(staleErr, &staleOperation) || staleOperation.Operation != "Environment.Get" {
			t.Fatalf("stale session read returned %v, want ErrNotFound or Environment.Get OperationError", staleErr)
		}

		t.Logf("stale session read returned unclassified operation error: %v (effect %v)", staleErr, staleOperation.Outcome.Effect)
	}
}

func TestGroupSessionsShareWindowsButNotSessionEnvironment(t *testing.T) {
	server := tmuxtest.NewServer(t)
	helper := buildLaunchHelper(t)

	dir := t.TempDir()

	owner, err := server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:    "group-owner",
		Dir:     dir,
		Program: tmux.Exec("sleep", "300"),
		TmuxEnv: map[string]string{sessionMarker: sessionToken},
		Size:    tmux.Size{Width: 100, Height: 30},
		Start:   tmux.ExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	member, err := server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:  "group-member",
		Group: "group-owner",
		Start: tmux.ExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, session := range []tmux.Session{owner, member} {
		info, err := session.Info(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		group, present := info.Group.Get()
		if !present || group != "group-owner" {
			t.Fatalf("session %s group = %q (present=%v), want group-owner", info.ID, group, present)
		}
	}

	if before := sessionWindowIDs(t, owner); !windowMapsEqual(before, sessionWindowIDs(t, member)) {
		t.Fatalf("group members disagree on window links: %v", before)
	}

	requireSessionMarker(t, owner)
	requireNoSessionMarker(t, member)

	report := filepath.Join(dir, "group-window.report")

	link, err := owner.NewWindow(t.Context(), tmux.NewWindowOptions{
		Name:    "group-window",
		Dir:     dir,
		Program: tmux.Exec(helper, "-report", report, "-hold", "300s"),
		TmuxEnv: map[string]string{windowMarker: windowToken},
	})
	if err != nil {
		t.Fatal(err)
	}

	requireProcessEnvironment(t, awaitProcessReport(t, report), windowMarker, windowToken)
	requireNoTmuxEnvironment(t, server, windowMarker, owner, member)

	after := sessionWindowIDs(t, member)

	propagated, present := after[link.Index()]
	if !present || propagated != link.Window().ID() {
		t.Fatalf("group member links %v, want window %s at index %d", after, link.Window().ID(), link.Index())
	}

	if !windowMapsEqual(sessionWindowIDs(t, owner), after) {
		t.Fatal("group members disagree after a window was created")
	}
}

func TestLinkedWindowIsOneObjectAcrossSessions(t *testing.T) {
	server := tmuxtest.NewServer(t)
	helper := buildLaunchHelper(t)

	owner, other, shared, linked, launched := linkedWindowFixture(t, server, helper)

	if !linked.Valid() || linked.Index() != 7 || linked.Session().ID() != other.ID() || linked.Window().ID() != shared.ID() {
		t.Fatalf("linked membership %v, want window %s at index 7 of %s", linked, shared.ID(), other.ID())
	}

	ownerPanes, err := shared.Panes(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	linkedPanes, err := linked.Window().Panes(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(ownerPanes) == 0 || !slices.Equal(paneIDs(ownerPanes), paneIDs(linkedPanes)) {
		t.Fatalf("shared window exposes differing panes: %v", ownerPanes)
	}

	if ownerPanes[0].PID != launched.PID {
		t.Fatalf("shared pane PID %d, want the single launched process %d", ownerPanes[0].PID, launched.PID)
	}

	requireSessionMarker(t, owner)
	requireNoSessionMarker(t, other)
	requireNoTmuxEnvironment(t, server, windowMarker, owner, other)
}

func TestUnlinkingAWindowMembershipKeepsTheSharedProcess(t *testing.T) {
	server := tmuxtest.NewServer(t)
	helper := buildLaunchHelper(t)

	owner, other, shared, _, launched := linkedWindowFixture(t, server, helper)

	ownerLinks, err := owner.Windows(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	unlinked := false

	for _, link := range ownerLinks {
		if link.Window().ID() != shared.ID() {
			continue
		}

		if err = link.Unlink(t.Context()); err != nil {
			t.Fatal(err)
		}

		unlinked = true
	}

	if !unlinked {
		t.Fatal("owner held no link for the shared window")
	}

	if _, present := sessionWindowIDs(t, owner)[4]; present {
		t.Fatal("unlinked window is still indexed in the owner session")
	}

	remaining, present := sessionWindowIDs(t, other)[7]
	if !present || remaining != shared.ID() {
		t.Fatalf("shared window lost from %s after unlink: %v", other.ID(), sessionWindowIDs(t, other))
	}

	after, err := shared.Panes(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(after) == 0 || after[0].PID != launched.PID {
		t.Fatalf("unlink restarted the shared process: %v, want PID %d", after, launched.PID)
	}
}

func TestDaemonRestartInvalidatesCreationHandles(t *testing.T) {
	server := tmuxtest.NewServer(t)

	const name = "restart-slot"

	session, err := server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:    name,
		Program: tmux.Exec("sleep", "300"),
		TmuxEnv: map[string]string{sessionMarker: sessionToken},
		Start:   tmux.ExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	identity := session.ID()

	requireSessionMarker(t, session)

	before, after := restartDaemon(t, server)

	if err = session.Rename(t.Context(), "restart-slot-renamed"); !errors.Is(err, tmux.ErrServerChanged) {
		t.Fatalf("stale handle rename returned %v, want ErrServerChanged", err)
	}

	if _, err = session.Environment().Get(t.Context(), sessionMarker, false); !errors.Is(err, tmux.ErrServerChanged) {
		t.Fatalf("stale handle environment read returned %v, want ErrServerChanged", err)
	}

	if after.Equal(before) {
		t.Fatal("restarted daemon reports the previous identity")
	}

	if after.ReportedSocket != before.ReportedSocket {
		t.Fatalf("restart moved the socket to %q", after.ReportedSocket)
	}

	if !before.Started.Equal(before.Started.Truncate(time.Second)) {
		t.Fatalf("daemon start time carries sub-second precision: %v", before.Started)
	}

	infos, err := server.Sessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, info := range infos {
		if info.ID == identity {
			t.Fatalf("session %s survived the daemon restart", identity)
		}
	}

	if _, err = server.Session(t.Context(), identity); !errors.Is(err, tmux.ErrNotFound) {
		t.Fatalf("stale session ID %s resolved on the replacement daemon: %v", identity, err)
	}
}

func TestRestartCannotProveCreationContinuity(t *testing.T) {
	server := tmuxtest.NewServer(t)

	const name = "restart-slot"

	original, err := server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:    name,
		Program: tmux.Exec("sleep", "300"),
		TmuxEnv: map[string]string{sessionMarker: sessionToken},
		Start:   tmux.ExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	identity := original.ID()

	restartDaemon(t, server)

	recreated, err := server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:    name,
		Program: tmux.Exec("sleep", "300"),
		TmuxEnv: map[string]string{sessionMarker: sessionToken},
		Start:   tmux.ExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	requireSessionMarker(t, recreated)

	info, err := recreated.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if info.Name != name {
		t.Fatalf("recreated session name = %q, want %q", info.Name, name)
	}

	if err = original.Rename(t.Context(), "restart-slot-renamed"); !errors.Is(err, tmux.ErrServerChanged) {
		t.Fatalf("stale handle rename on replacement daemon returned %v, want ErrServerChanged", err)
	}

	if info.ID == identity {
		t.Logf("replacement daemon reused session ID %s for a different session; stale handle correctly rejected despite ID collision", identity)
	}

	if recreated.Identity().Equal(original.Identity()) {
		t.Fatal("recreated session reports the previous daemon identity")
	}

	if matches := sessionsWithMarker(t, server, sessionToken); len(matches) != 1 {
		t.Fatalf("recreated marker matched %d sessions, want 1", len(matches))
	}
}
