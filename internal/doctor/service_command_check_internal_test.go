//go:build linux || darwin

package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

func writeUnit(tb testing.TB, path, content string) {
	tb.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		tb.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		tb.Fatal(err)
	}
}

func unitsService(tb testing.TB) (*Service, string, string) {
	tb.Helper()

	root := tb.TempDir()
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

	results := service.checkServiceUnits()
	if runtime.GOOS != "linux" {
		if len(results) != 0 {
			t.Fatalf("unsupported unit checks collected: %+v", results)
		}

		return
	}

	if len(results) != 1 {
		t.Fatalf("checks = %+v, want one unit check", results)
	}

	result := results[0]
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

func TestServiceUnitsRetainInspectionErrors(test *testing.T) {
	test.Parallel()

	for _, kind := range []string{"search directory", "unit symlink", "drop-in directory"} {
		test.Run(kind, func(test *testing.T) {
			test.Parallel()
			service := unreadableUnitsService(test, kind)

			results := service.checkServiceUnits()
			if runtime.GOOS != "linux" {
				if len(results) != 0 {
					test.Fatalf("unsupported unit checks collected: %+v", results)
				}

				return
			}

			if len(results) != 1 {
				test.Fatalf("inspection check omitted: %+v", results)
			}

			result := results[0]
			if result.Status != StatusWarn || result.Reason != ReasonInspectionFailed || result.Fix == nil || len(result.Details) == 0 {
				test.Fatalf("inspection error was omitted or claimed healthy: %+v", result)
			}
		})
	}
}

func unreadableUnitsService(tb testing.TB, kind string) *Service {
	tb.Helper()

	service, userDir, _ := unitsService(tb)

	switch kind {
	case "search directory":
		writeUnit(tb, userDir, "not a directory")
	case "unit symlink":
		if err := os.MkdirAll(userDir, 0o700); err != nil {
			tb.Fatal(err)
		}

		if err := os.Symlink("loop.service", filepath.Join(userDir, "loop.service")); err != nil {
			tb.Fatal(err)
		}
	case "drop-in directory":
		writeUnit(tb, filepath.Join(userDir, "sesh.service"), "[Service]\nExecStart=sesh doctor\n")
		writeUnit(tb, filepath.Join(userDir, "sesh.service.d"), "not a directory")
	}

	return service
}

func TestServiceUnitsPreserveUnavailableValidationSkip(test *testing.T) {
	test.Parallel()

	service := NewService(Options{ServiceDirs: []string{test.TempDir()}})

	results := service.checkServiceUnits()
	if runtime.GOOS != "linux" {
		if len(results) != 0 {
			test.Fatalf("unsupported unit checks collected: %+v", results)
		}

		return
	}

	if len(results) != 1 || results[0].Status != StatusSkip {
		test.Fatalf("unavailable command validation lost its skip: %+v", results)
	}
}

func TestServiceUnitsOmitMaskedAndUnrelatedUnits(test *testing.T) {
	test.Parallel()

	service, userDir, dataDir := unitsService(test)

	writeUnit(test, filepath.Join(dataDir, "masked.service"), "[Service]\nExecStart=sesh old\n")
	writeUnit(test, filepath.Join(userDir, "other.service"), "[Service]\nExecStart=/usr/bin/true old\n")

	if err := os.Symlink(os.DevNull, filepath.Join(userDir, "masked.service")); err != nil {
		test.Fatal(err)
	}

	if results := service.checkServiceUnits(); len(results) != 0 {
		test.Fatalf("unused service unit checks were collected: %+v", results)
	}
}
