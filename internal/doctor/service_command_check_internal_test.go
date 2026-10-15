//go:build linux || darwin

package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var errTestRejected = errors.New("rejected")

func TestSplitExecLineFollowsSystemdQuotingAndPrefixes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		value string
		want  []string
	}{
		{value: `/bin/sesh autosave final-save --deadline 20s`, want: []string{"/bin/sesh", "autosave", "final-save", "--deadline", "20s"}},
		{value: `-/bin/sesh daemon tmux --socket "/tmp/a b"`, want: []string{"/bin/sesh", "daemon", "tmux", "--socket", "/tmp/a b"}},
		{value: `  /bin/sesh   --name 'it''s' "q\"uote"`, want: []string{"/bin/sesh", "--name", "its", `q"uote`}},
		{value: `/bin/sesh --path "%%h/$$HOME" ""`, want: []string{"/bin/sesh", "--path", "%h/$HOME", ""}},
		{value: `@/bin/sesh sesh-alias daemon coordinator`, want: []string{"/bin/sesh", "daemon", "coordinator"}},
		{value: `:+!/bin/sesh doctor`, want: []string{"/bin/sesh", "doctor"}},
	} {
		if got := splitExecLine(tc.value); !slices.Equal(got, tc.want) {
			t.Errorf("splitExecLine(%q) = %q, want %q", tc.value, got, tc.want)
		}
	}
}

func TestUnitLinesJoinContinuationsAndDropComments(t *testing.T) {
	t.Parallel()

	data := []byte("# ExecStart=/bin/sesh old\n[Service]\nExecStart=/bin/sesh daemon \\\n  coordinator\n; note\n")

	want := []unitLine{{text: "[Service]", number: 2}, {text: "ExecStart=/bin/sesh daemon  coordinator", number: 3}}
	if got := unitLines(data); !slices.Equal(got, want) {
		t.Fatalf("unitLines = %+v, want %+v", got, want)
	}
}

func writeUnit(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// unitsService checks units across a user directory and a lower-precedence data directory,
// rejecting every sesh command line that runs "old".
func unitsService(t *testing.T) (*Service, string, string) {
	t.Helper()

	root := t.TempDir()
	userDir := filepath.Join(root, "config", "systemd", "user")
	dataDir := filepath.Join(root, "data", "systemd", "user")

	service := NewService(Options{
		ServiceDirs: []string{userDir, dataDir},
		ValidateCommand: func(args []string) error {
			if slices.Contains(args, "old") {
				return errTestRejected
			}

			return nil
		},
	})

	return service, userDir, dataDir
}

func TestServiceUnitsAreReadTheWaySystemdResolvesThem(t *testing.T) {
	t.Parallel()

	service, userDir, dataDir := unitsService(t)
	elsewhere := filepath.Join(t.TempDir(), "linked.service")

	writeUnit(t, elsewhere, "[Service]\nExecStart=/usr/bin/sesh old\n")

	if err := os.MkdirAll(userDir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(elsewhere, filepath.Join(userDir, "linked.service")); err != nil {
		t.Fatal(err)
	}

	writeUnit(t, filepath.Join(userDir, "replaced.service"), "[Service]\nExecStart=/usr/bin/sesh daemon coordinator\n")
	writeUnit(t, filepath.Join(dataDir, "replaced.service.d", "override.conf"), "[Service]\nExecStart=\nExecStart=sesh old\n")
	writeUnit(t, filepath.Join(dataDir, "shared.service"), "[Service]\nExecStop=sesh old\n")
	writeUnit(t, filepath.Join(userDir, "reset.service"), "[Service]\nExecStart=sesh old\n")
	writeUnit(t, filepath.Join(userDir, "reset.service.d", "clear.conf"), "[Service]\nExecStart=\nExecStart=/bin/true\n")
	writeUnit(t, filepath.Join(userDir, "shadow.service"), "[Service]\nExecStart=sesh daemon coordinator\n")
	writeUnit(t, filepath.Join(dataDir, "shadow.service"), "[Service]\nExecStart=sesh old\n")

	result := service.checkServiceUnits()
	if result.Status != StatusFail || result.Reason != ReasonStale || result.Fix == nil {
		t.Fatalf("result = %+v, want a failure with a fix", result)
	}

	stale := make([]string, 0, len(result.Details))
	for _, detail := range result.Details {
		unit, _, _ := strings.Cut(detail, ":")
		stale = append(stale, unit)
	}

	if want := []string{"linked.service", "replaced.service", "shared.service"}; !slices.Equal(stale, want) {
		t.Fatalf("stale units = %q, want %q; details %q", stale, want, result.Details)
	}

	if !strings.Contains(result.Details[1], filepath.Join("replaced.service.d", "override.conf")+":3") {
		t.Fatalf("detail must point at the drop-in line that sets the command: %q", result.Details[1])
	}
}

func TestServiceUnitsSkipMaskedUnitsAndReportWhenNoneRunSesh(t *testing.T) {
	t.Parallel()

	service, userDir, dataDir := unitsService(t)

	writeUnit(t, filepath.Join(dataDir, "masked.service"), "[Service]\nExecStart=sesh old\n")
	writeUnit(t, filepath.Join(userDir, "other.service"), "[Service]\nExecStart=/usr/bin/true old\n")

	if err := os.Symlink(os.DevNull, filepath.Join(userDir, "masked.service")); err != nil {
		t.Fatal(err)
	}

	if result := service.checkServiceUnits(); result.Status != StatusInfo {
		t.Fatalf("result = %+v, want info that no unit runs sesh", result)
	}
}
