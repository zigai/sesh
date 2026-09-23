//go:build linux || darwin

package cmdsnapshot

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/snapshot/store"
	"github.com/zigai/sesh/internal/tui/snapshotpicker"
)

type snapshotPickerConflictCase struct {
	name  string
	flag  string
	value string
	want  error
}

func TestSnapshotRestorePickerRejectsConflictsBeforeRepositoryAccess(t *testing.T) {
	t.Parallel()

	tests := []snapshotPickerConflictCase{
		{name: "json", flag: jsonFlagName, value: "true", want: errSnapshotPickerJSONConflict},
		{name: "generation", flag: "generation", value: "selected", want: errSnapshotPickerGenerationConflict},
		{name: "operation", flag: "operation", value: "selected", want: errSnapshotPickerOperationConflict},
	}

	for _, action := range []bool{false, true} {
		surface := "snapshot"
		if action {
			surface = "action"
		}

		for _, test := range tests {
			t.Run(surface+"/"+test.name, func(t *testing.T) {
				t.Parallel()
				testSnapshotRestorePickerConflict(t, action, test)
			})
		}
	}
}

func testSnapshotRestorePickerConflict(t *testing.T, action bool, test snapshotPickerConflictCase) {
	t.Helper()

	repository := filepath.Join(t.TempDir(), "must-not-exist")

	cmd := newSnapshotRestoreCommand()
	if action {
		cmd = NewActionRestoreCommand()
	}

	cmd.SetIn(&bytes.Buffer{})
	cmd.SetOut(&bytes.Buffer{})

	for flag, value := range map[string]string{
		"repository": repository,
		"socket":     "/tmp/sesh-picker-test.sock",
		"lease-root": t.TempDir(),
		"pick":       "true",
		test.flag:    test.value,
	} {
		if err := cmd.Flags().Set(flag, value); err != nil {
			t.Fatal(err)
		}
	}

	repositoryPath, socketPath, cfg, err := parseRestoreInput(cmd, nil)
	if !errors.Is(err, test.want) || clibase.ExitCode(err) != clibase.ExitCodeUsage {
		t.Fatalf("parse error = %v, want %v", err, test.want)
	}

	if repositoryPath != "" || socketPath != "" || cfg.Generation != "" {
		t.Fatalf("conflict rejection returned restore input: %q %q %+v", repositoryPath, socketPath, cfg)
	}

	if _, err := os.Stat(repository); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository touched before conflict rejection: %v", err)
	}
}

func TestSnapshotRestorePickerRejectsNonTerminalBeforeRepositoryAccess(t *testing.T) {
	t.Parallel()

	for _, action := range []bool{false, true} {
		name := "snapshot"
		if action {
			name = "action"
		}

		t.Run(name, func(t *testing.T) {
			t.Parallel()
			testSnapshotRestorePickerNonTerminal(t, action)
		})
	}
}

func testSnapshotRestorePickerNonTerminal(t *testing.T, action bool) {
	t.Helper()

	repository := filepath.Join(t.TempDir(), "must-not-exist")

	cmd := newSnapshotRestoreCommand()
	if action {
		cmd = NewActionRestoreCommand()
	}

	cmd.SetIn(&bytes.Buffer{})
	cmd.SetOut(&bytes.Buffer{})

	for flag, value := range map[string]string{
		"repository": repository,
		"socket":     "/tmp/sesh-picker-test.sock",
		"lease-root": t.TempDir(),
		"pick":       "true",
	} {
		if err := cmd.Flags().Set(flag, value); err != nil {
			t.Fatal(err)
		}
	}

	repositoryPath, socketPath, cfg, err := parseRestoreInput(cmd, nil)
	if !errors.Is(err, errSnapshotPickerTerminalRequired) || clibase.ExitCode(err) != clibase.ExitCodeUsage {
		t.Fatalf("non-terminal error = %v, want %v", err, errSnapshotPickerTerminalRequired)
	}

	if repositoryPath != "" || socketPath != "" || cfg.Generation != "" {
		t.Fatalf("non-terminal rejection returned restore input: %q %q %+v", repositoryPath, socketPath, cfg)
	}

	if _, err := os.Stat(repository); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository touched before non-terminal rejection: %v", err)
	}
}

func TestSnapshotPickerEntryReportsIncompleteParityAsWarning(t *testing.T) {
	t.Parallel()

	entry := snapshotPickerEntry(
		store.Generation{ID: "gen-incomplete", Sequence: 1, Complete: false},
		time.Now().UnixNano(),
		1, 2, 3,
		1024,
		"gen-incomplete",
	)

	if entry.Validity != snapshotpicker.SnapshotPartial {
		t.Fatalf("validity = %v, want %v", entry.Validity, snapshotpicker.SnapshotPartial)
	}

	if len(entry.Warnings) != 1 || entry.Warnings[0] != "parity evidence is incomplete; restore will report a partial result" {
		t.Fatalf("warnings = %v, want incomplete parity warning", entry.Warnings)
	}

	if !entry.Latest {
		t.Fatal("entry was not marked latest")
	}
}
