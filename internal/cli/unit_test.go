package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelpAndIncompleteParityNotice(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "root help shows subcommands",
			args: []string{"--help"},
			want: []string{snapshotCmd, actionCmd, "Usage:", "sesh [command]"},
		},
		{
			name: "snapshot help states incomplete parity",
			args: []string{snapshotCmd, "--help"},
			want: []string{
				"practical session reconstruction",
				"exact fidelity limits visible",
				"list", "history", "verify",
			},
		},
		{
			name: "snapshot list help states incomplete parity",
			args: []string{snapshotCmd, "list", "--help"},
			want: []string{
				"Practical recovery generations",
				"exact fidelity limits",
				testRepositoryFlag, testGenerationFlag, "--full",
			},
		},
		{
			name: "snapshot history help states incomplete parity",
			args: []string{snapshotCmd, "history", "--help"},
			want: []string{
				"exact completeness",
				"practical recovery observations",
				testRepositoryFlag, "--full",
			},
		},
		{
			name: "snapshot verify help states incomplete parity",
			args: []string{snapshotCmd, "verify", "--help"},
			want: []string{
				"Practical readiness",
				"runtime preflight",
				testRepositoryFlag, testGenerationFlag,
			},
		},
		{
			name: "action snapshot-save help states incomplete parity",
			args: []string{"action", "snapshot-save", "--help"},
			want: []string{
				"practical recovery observations",
				"not atomic checkpoints",
				testRepositoryFlag, testSocketFlag, testLeaseRootFlag,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := newRootCommand(t.Context())

			stdout, stderr, err := executeCommand(root, tc.args...)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if stderr != "" {
				t.Fatalf("stderr not empty: %s", stderr)
			}

			for _, w := range tc.want {
				if !strings.Contains(stdout, w) {
					t.Fatalf("output missing %q:\n%s", w, stdout)
				}
			}
		})
	}
}

func TestBareCommandsPrintHelp(t *testing.T) {
	for _, cmdPath := range [][]string{
		{snapshotCmd},
		{"action"},
	} {
		t.Run(strings.Join(cmdPath, " "), func(t *testing.T) {
			root := newRootCommand(t.Context())

			stdout, stderr, err := executeCommand(root, cmdPath...)
			if ExitCode(err) != exitCodeUsage {
				t.Fatalf("exit code = %d, want usage error: %v", ExitCode(err), err)
			}

			if !strings.Contains(stderr, "subcommand required") {
				t.Fatalf("stderr missing subcommand diagnostic: %s", stderr)
			}

			if !strings.Contains(stdout, "Usage:") {
				t.Fatalf("output missing Usage:\n%s", stdout)
			}
		})
	}
}

func TestUnknownCommandsAndFlagsExitCodeUsage(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"root unknown command", []string{"unknowncmd"}, "unknown command"},
		{"snapshot unknown command", []string{snapshotCmd, "badsub"}, "unknown command"},
		{"action unknown command", []string{"action", "badact"}, "unknown command"},
		{"root unknown flag", []string{"--nonexistent"}, "unknown flag"},
		{"root global json rejected", []string{testJSONFlag}, "unknown flag"},
		{"snapshot unknown flag", []string{snapshotCmd, "--bogus"}, "unknown flag"},
		{"action snapshot-save unknown flag", []string{"action", "snapshot-save", "--bogus"}, "unknown flag"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newRootCommand(t.Context())

			stdout, stderr, err := executeCommand(root, tc.args...)
			if err == nil {
				t.Fatalf("expected error for %v, got nil", tc.args)
			}

			code := ExitCode(err)
			if code != exitCodeUsage {
				t.Fatalf("ExitCode = %d, want %d (usage)", code, exitCodeUsage)
			}

			if stdout != "" {
				t.Fatalf("stdout should be empty on error, got: %q", stdout)
			}

			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("stderr missing %q, got: %q", tc.want, stderr)
			}

			if strings.Count(stderr, "Usage:") > 0 {
				t.Fatalf("stderr dumped duplicate Cobra usage:\n%s", stderr)
			}
		})
	}
}

func TestUnregisteredCommandsFail(t *testing.T) {
	prohibited := [][]string{
		{"agent"},
		{"agent", "next"},
		{"agent", "back"},
		{"agent", "focus"},
		{"agent", "status"},
		{"new"},
		{"rename"},
		{"kill"},
		{"delete"},
		{"switch"},
		{snapshotCmd, "save"},
		{snapshotCmd, "restore"},
		{"autosave", "status"},
		{"autosave", "save-if-due"},
		{"autosave", "restore-on-start"},
		{"resurrect"},
		{"resurrect", "save"},
		{"resurrect", "restore"},
		{"resurrect", "list"},
		{"resurrect", "history"},
		{"resurrect", "delete-session"},
		{"resurrect", "delete-all"},
		{"continuum"},
		{"continuum", "status"},
		{"continuum", "save-if-due"},
		{"continuum", "restore-on-start"},
		{"restore"},
		{"resume"},
		{"delete-session"},
		{"delete-all"},
		{"picker"},
		{snapshotCmd, "resume"},
		{snapshotCmd, "delete"},
		{snapshotCmd, "delete-session"},
		{snapshotCmd, "delete-all"},
		{snapshotCmd, "picker"},
		{"action", "snapshot-resume"},
	}

	for _, args := range prohibited {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := newRootCommand(t.Context())

			_, _, err := executeCommand(root, args...)
			if err == nil {
				t.Fatalf("prohibited command %v succeeded, want error", args)
			}

			code := ExitCode(err)
			if code != exitCodeUsage {
				t.Fatalf("ExitCode = %d, want %d", code, exitCodeUsage)
			}
		})
	}
}

func TestSnapshotReadCommandsDefaultToConfiguredRepository(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)

	for _, cmdArgs := range [][]string{
		{snapshotCmd, "list", "--no-config"},
		{snapshotCmd, "history", "--no-config"},
		{snapshotCmd, "verify", "--no-config"},
	} {
		t.Run(strings.Join(cmdArgs, " "), func(t *testing.T) {
			root := newRootCommand(t.Context())

			_, stderr, err := executeCommand(root, cmdArgs...)
			if ExitCode(err) == exitCodeUsage {
				t.Fatalf("%v without --repository is a usage error: %v / %q", cmdArgs, err, stderr)
			}

			if _, statErr := os.Stat(filepath.Join(dataHome, "sesh", "snapshots")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("read command created the default repository: %v", statErr)
			}
		})
	}
}

func TestReadCommandsDoNotCreateNonexistentRepository(t *testing.T) {
	tempBase := t.TempDir()
	nonexistent := filepath.Join(tempBase, "does-not-exist")

	commands := [][]string{
		{snapshotCmd, "list", testRepositoryFlag, nonexistent},
		{snapshotCmd, "history", testRepositoryFlag, nonexistent},
		{snapshotCmd, "verify", testRepositoryFlag, nonexistent},
	}

	for _, cmdArgs := range commands {
		t.Run(strings.Join(cmdArgs, " "), func(t *testing.T) {
			root := newRootCommand(t.Context())

			stdout, stderr, err := executeCommand(root, cmdArgs...)
			if err == nil {
				t.Fatalf("expected error reading nonexistent repo, got nil")
			}

			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("err %v should wrap os.ErrNotExist", err)
			}

			code := ExitCode(err)
			if code != exitCodeGeneral {
				t.Fatalf("ExitCode = %d, want %d", code, exitCodeGeneral)
			}

			if stdout != "" {
				t.Fatalf("stdout should be empty, got: %q", stdout)
			}

			if stderr == "" {
				t.Fatalf("stderr should contain error message")
			}

			if _, statErr := os.Stat(nonexistent); !os.IsNotExist(statErr) {
				t.Fatalf("read command created nonexistent repository directory: %v", statErr)
			}
		})
	}
}

func TestOutputWriterFailureDetection(t *testing.T) {
	cause := &os.PathError{Op: "write", Path: "/synthetic/pipe", Err: os.ErrPermission}
	root := newRootCommand(t.Context())
	root.SetOut(failedWriter{cause: cause})

	var stderr bytes.Buffer
	root.SetErr(&stderr)
	root.SetArgs([]string{"--version"})

	err := root.Execute()
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("lost permission error identity: %v", err)
	}

	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr != cause {
		t.Fatalf("lost typed *os.PathError: %v", err)
	}
}
