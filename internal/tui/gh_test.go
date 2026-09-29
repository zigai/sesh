package tui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/display/layout"
)

func TestGHScanSharesRepositoryBranchResultAndRetainsMinuteCache(t *testing.T) {
	calls := 0
	query := func(_ context.Context, _ ghTarget, at time.Time) ghEntry {
		calls++
		return ghEntry{at: at, pr: ghCell("#12", "", layout.Present), ci: ghCell("passing", "success", layout.Present)}
	}
	directory := t.TempDir()
	key := ghKey{repo: directory, branch: "feature"}
	targets := []ghTarget{
		{paneID: "%1", key: key, directory: directory, hasRemote: true},
		{paneID: "%2", key: key, directory: directory, hasRemote: true},
		{paneID: "%3", key: key, directory: directory, remote: true, hasRemote: true},
	}
	at := time.Now()

	entries, rows := scanGHWithQuery(t.Context(), targets, nil, at, true, query)
	if got := rows["%1"][display.FieldAgentPR]; got.State != layout.Present || got.Text != "#12" {
		t.Fatalf("PR cell = %+v", got)
	}

	if got := rows["%2"][display.FieldAgentCI].Text; got != "passing" {
		t.Fatalf("CI cell = %q", got)
	}

	if got := rows["%3"][display.FieldAgentPR].State; got != layout.Unsupported {
		t.Fatalf("remote PR state = %v", got)
	}

	_, _ = scanGHWithQuery(t.Context(), targets, entries, at.Add(30*time.Second), true, query)
	if calls != 1 {
		t.Fatalf("gh invocations = %d, want one", calls)
	}
}

func TestGHScanReportsUnsupportedWithoutCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, rows := scanGH(t.Context(), []ghTarget{{paneID: "%1", key: ghKey{repo: "repo", branch: "branch"}, directory: t.TempDir(), hasRemote: true}}, nil, time.Now())
	if got := rows["%1"][display.FieldAgentCI].State; got != layout.Unsupported {
		t.Fatalf("CI state without gh = %v", got)
	}
}

func TestGHChecksPreferFailureThenPendingThenSuccess(t *testing.T) {
	for _, tc := range []struct {
		json string
		want string
	}{
		{json: `{"number":1,"statusCheckRollup":[{"conclusion":"SUCCESS","status":"COMPLETED"}]}`, want: "passing"},
		{json: `{"number":1,"statusCheckRollup":[{"state":"PENDING"}]}`, want: "pending"},
		{json: `{"number":1,"statusCheckRollup":[{"conclusion":"FAILURE"},{"state":"PENDING"}]}`, want: "failing"},
	} {
		var value ghPullRequest
		if err := json.Unmarshal([]byte(tc.json), &value); err != nil {
			t.Fatal(err)
		}

		if got := classifyGHChecks(value.Checks).Text; got != tc.want {
			t.Fatalf("checks %s = %q, want %q", strings.TrimSpace(tc.json), got, tc.want)
		}
	}
}
