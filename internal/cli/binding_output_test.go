package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/zigai/sesh/internal/app"
)

func TestBindingReportsDoNotReachTmuxRunShellOutput(t *testing.T) {
	commands := [][]string{
		{"agent", "window-status-clear"},
		{"agent", "next"},
		{snapshotCmd, "save"},
		{snapshotCmd, "restore"},
		{"session", "kill"},
		{"pick", "popup"},
		{"hooks", "run"},
	}

	for _, path := range commands {
		t.Run(path[0]+"_"+path[1], func(t *testing.T) {
			for _, mode := range []string{"binding", "outside_tmux", "json"} {
				t.Run(mode, func(t *testing.T) {
					testBindingReportOutput(t, path, mode)
				})
			}
		})
	}
}

func testBindingReportOutput(t *testing.T, path []string, mode string) {
	t.Helper()
	t.Setenv("TMUX", "/tmp/test-binding/socket,1,0")

	if mode == "outside_tmux" {
		t.Setenv("TMUX", "")
	}

	root := newRootCommand(t.Context())

	cmd, _, err := root.Find(path)
	if err != nil {
		t.Fatal(err)
	}

	report := "command report\n"

	if mode == "json" {
		report = "{\"ok\":true}\n"

		if err := cmd.Flags().Set("json", "true"); err != nil {
			t.Fatal(err)
		}
	}

	var (
		output, diagnostics commandBuffer
		stdout, stderr      bytes.Buffer
	)

	if _, err := output.Write([]byte(report)); err != nil {
		t.Fatal(err)
	}

	if err := publishCommandOutput(cmd, &output, &diagnostics, &stdout, &stderr, nil, nil); err != nil {
		t.Fatal(err)
	}

	want := report
	if mode == "binding" {
		want = ""
	}

	if stdout.String() != want || stderr.Len() != 0 {
		t.Fatalf("output = %q / %q, want %q / empty", stdout.String(), stderr.String(), want)
	}
}

func TestBindingPartialSnapshotKeepsFailureWithoutPrintingReport(t *testing.T) {
	t.Setenv("TMUX", "/tmp/test-binding/socket,1,0")
	root := newRootCommand(t.Context())

	cmd, _, err := root.Find([]string{snapshotCmd, "save"})
	if err != nil {
		t.Fatal(err)
	}

	var (
		output, diagnostics commandBuffer
		stdout, stderr      bytes.Buffer
	)

	if _, err := output.Write([]byte("partial snapshot report\n")); err != nil {
		t.Fatal(err)
	}

	err = publishCommandOutput(cmd, &output, &diagnostics, &stdout, &stderr, app.ErrPartialSnapshot, nil)
	if !errors.Is(err, app.ErrPartialSnapshot) {
		t.Fatalf("error = %v, want partial snapshot cause", err)
	}

	if stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf("output = %q / %q, want no stdout and a failure diagnostic", stdout.String(), stderr.String())
	}
}
