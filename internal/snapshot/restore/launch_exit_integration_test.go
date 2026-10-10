//go:build (linux || darwin) && integration

package restore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/snapshot/model"
)

type restoredLaunchFixture struct {
	manifest    model.Manifest
	paneIndex   int
	directory   string
	ready       string
	exited      string
	usable      string
	shellMarker string
}

func TestRestoredProgramExitLeavesPaneWithUsableShell(t *testing.T) {
	for _, test := range []struct {
		placement string
		interrupt bool
	}{
		{placement: "initial"},
		{placement: "split"},
		{placement: "window"},
		{placement: "initial", interrupt: true},
		{placement: "split", interrupt: true},
		{placement: "window", interrupt: true},
	} {
		t.Run(fmt.Sprintf("%s/interrupt=%t", test.placement, test.interrupt), func(t *testing.T) {
			server := tmuxtest.NewServer(t)

			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()

			if err := server.Environment().Set(ctx, "SESH_AGENT_EXIT_REMOVE", "target-value"); err != nil {
				t.Fatal(err)
			}

			fixture := configuredRestoredLaunchFixture(t, test.placement)
			pane := executeRestoredLaunch(t, ctx, server, fixture)
			exitRestoredProgram(t, ctx, pane, fixture, test.interrupt)
			assertRestoredShellUsable(t, ctx, pane, fixture)
		})
	}
}

func configuredRestoredLaunchFixture(t *testing.T, placement string) restoredLaunchFixture {
	t.Helper()

	manifest, paneIndex := restoredLaunchManifest(placement)
	directory := t.TempDir()
	ready := filepath.Join(directory, "ready")
	exited := filepath.Join(directory, "exited")
	usable := filepath.Join(directory, "usable")
	configuredShell := filepath.Join(directory, "configured shell")
	shellMarker := filepath.Join(directory, "shell")

	shellScript := "#!/bin/sh\nprintf configured > '" + strings.ReplaceAll(shellMarker, "'", "'\\''") + "'\nexec /bin/sh \"$@\"\n"
	if err := os.WriteFile(configuredShell, []byte(shellScript), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(configuredShell, 0o700); err != nil {
		t.Fatal(err)
	}

	manifest.Source.Options = []model.Option{{Scope: "global-session", Name: model.Bytes("default-shell"), Value: model.Bytes("/bin/sh"), Present: true}}
	manifest.Sessions[0].Options = []model.Option{{Scope: "session", Name: model.Bytes("default-shell"), Value: model.Bytes(configuredShell), Present: true}}
	manifest.Panes[paneIndex].Launch = model.Launch{
		Executable: model.Bytes("/bin/sh"),
		Arguments: []model.Bytes{
			model.Bytes("sh"), model.Bytes("-c"),
			model.Bytes(`trap 'printf exited > "$2"; exit 130' INT; [ "$3" = 'literal spaces "$HOME"; $(touch unexpected)' ] && [ "${SESH_AGENT_EXIT_REMOVE-unset}" = unset ] || exit 99; printf ready > "$1"; read -r answer; printf exited > "$2"`),
			model.Bytes("sh"), model.Bytes(ready), model.Bytes(exited), model.Bytes(`literal spaces "$HOME"; $(touch unexpected)`),
		},
		Directory:   model.Bytes(directory),
		Environment: []model.Variable{{Name: model.Bytes("SESH_AGENT_EXIT_REMOVE"), State: "removed"}},
		Recovery:    model.RecoveryResume,
	}

	return restoredLaunchFixture{manifest: manifest, paneIndex: paneIndex, directory: directory, ready: ready, exited: exited, usable: usable, shellMarker: shellMarker}
}

func executeRestoredLaunch(t *testing.T, ctx context.Context, server *gotmux.Server, fixture restoredLaunchFixture) gotmux.Pane {
	t.Helper()

	plan, err := BuildPlan(fixture.manifest, []string{"s0"})
	if err != nil {
		t.Fatal(err)
	}

	report, err := NewExecutor(server, fixture.manifest, ExecuteConfig{LaunchProcesses: true}).Execute(ctx, plan, newMemoryJournal())
	if err != nil {
		t.Fatal(err)
	}

	var runtimeID string

	for _, result := range report.Panes {
		if result.PaneID == fixture.manifest.Panes[fixture.paneIndex].ID {
			runtimeID = result.RuntimeID
		}
	}

	pane, err := server.Pane(ctx, gotmux.PaneID(runtimeID))
	if err != nil {
		t.Fatal(err)
	}

	return pane
}

func exitRestoredProgram(t *testing.T, ctx context.Context, pane gotmux.Pane, fixture restoredLaunchFixture, interrupt bool) {
	t.Helper()

	if got := waitForLaunchMarker(t, ctx, fixture.ready); got != "ready" {
		t.Fatalf("program readiness = %q", got)
	}

	assertRestoredProgramForeground(t, ctx, pane, fixture)

	exitKey := gotmux.KeyEnter
	if interrupt {
		exitKey = gotmux.KeyCtrlC
	}

	if err := pane.SendKeys(ctx, exitKey); err != nil {
		t.Fatal(err)
	}

	if got := waitForLaunchMarker(t, ctx, fixture.exited); got != "exited" {
		t.Fatalf("program exit marker = %q", got)
	}
}

func assertRestoredShellUsable(t *testing.T, ctx context.Context, pane gotmux.Pane, fixture restoredLaunchFixture) {
	t.Helper()

	if got := waitForLaunchMarker(t, ctx, fixture.shellMarker); got != "configured" {
		t.Fatalf("returning configured shell marker = %q", got)
	}

	command := fmt.Sprintf(`printf '%%s|%%s' "$PWD" "${SESH_AGENT_EXIT_REMOVE-unset}" > '%s'`, strings.ReplaceAll(fixture.usable, "'", "'\\''"))
	if err := pane.SendText(ctx, command); err != nil {
		t.Fatalf("restored pane disappeared after program exit: %v", err)
	}

	if err := pane.SendKeys(ctx, gotmux.KeyEnter); err != nil {
		t.Fatal(err)
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		info, err := pane.Info(ctx)
		if err != nil || info.Dead {
			t.Fatalf("restored pane did not survive program exit: dead=%t err=%v", info.Dead, err)
		}

		if data, err := os.ReadFile(fixture.usable); err == nil && len(data) > 0 {
			if got, want := string(data), fixture.directory+"|unset"; got != want {
				t.Fatalf("returning shell directory and environment = %q, want %q", got, want)
			}

			break
		}

		select {
		case <-ctx.Done():
			t.Fatalf("restored pane has no usable shell after program exit: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func restoredLaunchManifest(placement string) (model.Manifest, int) {
	manifest := newIntegrationManifest()
	paneIndex := 0

	switch placement {
	case "split":
		manifest = nestedLayoutFixture()
		paneIndex = 1
	case "window":
		window := manifest.Windows[0]
		window.ID = "w1"
		window.Name = model.Bytes("other")
		window.Layout.Pane = "p1"
		manifest.Windows = append(manifest.Windows, window)
		manifest.Links = append(manifest.Links, model.Link{Session: "s0", Window: "w1", Index: 2})
		pane := manifest.Panes[0]
		pane.ID = "p1"
		pane.Window = "w1"
		manifest.Panes = append(manifest.Panes, pane)
		paneIndex = 1
	}

	return manifest, paneIndex
}

func assertRestoredProgramForeground(t *testing.T, ctx context.Context, pane gotmux.Pane, fixture restoredLaunchFixture) {
	t.Helper()

	info, err := pane.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}

	birth, err := process.ReadIdentity(ctx, info.PID)
	if err != nil {
		t.Fatal(err)
	}

	observed, err := process.ObserveForeground(ctx, process.ForegroundRequest{
		PanePID: info.PID, ExpectedPaneBirth: birth,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := make([]string, 1, len(fixture.manifest.Panes[fixture.paneIndex].Launch.Arguments))
	want[0] = "/bin/sh"

	for _, argument := range fixture.manifest.Panes[fixture.paneIndex].Launch.Arguments[1:] {
		want = append(want, string(argument))
	}

	got := make([]string, 0, len(observed.Arguments))
	for _, argument := range observed.Arguments {
		got = append(got, string(argument))
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("foreground application arguments mismatch (-want +got):\n%s", diff)
	}
}
