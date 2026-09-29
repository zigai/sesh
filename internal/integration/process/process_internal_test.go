//go:build linux

package process

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/samber/oops"
	"golang.org/x/sys/unix"
)

const (
	testBootID     = "11111111-2222-3333-4444-555555555555"
	testStartTime  = 123456
	testPID        = 42
	oversizedBytes = (2 << 20) + 16
)

func createProcFixture(t *testing.T, statContent, cmdlineContent, environContent []byte) (string, string) {
	t.Helper()

	procRoot := t.TempDir()

	pidDir := filepath.Join(procRoot, strconv.Itoa(testPID))
	if err := os.MkdirAll(pidDir, 0o700); err != nil {
		t.Fatalf("mkdir pid dir: %v", err)
	}

	bootIDPath := filepath.Join(procRoot, "boot_id")
	if err := os.WriteFile(bootIDPath, []byte(testBootID+"\n"), 0o600); err != nil {
		t.Fatalf("write boot_id: %v", err)
	}

	if statContent != nil {
		if err := os.WriteFile(filepath.Join(pidDir, "stat"), statContent, 0o600); err != nil {
			t.Fatalf("write stat: %v", err)
		}
	}

	if cmdlineContent != nil {
		if err := os.WriteFile(filepath.Join(pidDir, "cmdline"), cmdlineContent, 0o600); err != nil {
			t.Fatalf("write cmdline: %v", err)
		}
	}

	if environContent != nil {
		if err := os.WriteFile(filepath.Join(pidDir, "environ"), environContent, 0o600); err != nil {
			t.Fatalf("write environ: %v", err)
		}
	}

	_ = os.Symlink("/bin/sh", filepath.Join(pidDir, "exe"))
	_ = os.Symlink("/tmp", filepath.Join(pidDir, "cwd"))

	return procRoot, bootIDPath
}

func validStatContent(pid int, state byte, startTime uint64) []byte {
	return []byte(fmt.Sprintf("%d (test_proc) %c 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 %d 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n",
		pid, state, startTime))
}

func assertFixtureError(t *testing.T, procRoot, bootIDPath string, envKeys []string, wantErr error) {
	t.Helper()

	_, err := observeLinux(t.Context(), procRoot, bootIDPath, Request{
		PID:             testPID,
		ExpectedBirth:   Identity{BootID: testBootID, StartTime: testStartTime},
		EnvironmentKeys: envKeys,
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("expected %v, got: %v", wantErr, err)
	}
}

func TestObserve_ParserFixtures(t *testing.T) {
	validStat := validStatContent(testPID, 'S', testStartTime)

	t.Run("malformed cmdline lacking trailing NUL", func(t *testing.T) {
		procRoot, bootIDPath := createProcFixture(t, validStat, []byte("arg1\x00arg2_without_nul"), nil)
		assertFixtureError(t, procRoot, bootIDPath, nil, ErrMalformedCmdline)
	})

	t.Run("empty cmdline (kernel thread)", func(t *testing.T) {
		procRoot, bootIDPath := createProcFixture(t, validStat, []byte{}, nil)
		assertFixtureError(t, procRoot, bootIDPath, nil, ErrEmptyCmdline)
	})

	t.Run("oversized cmdline exceeding bound", func(t *testing.T) {
		oversized := make([]byte, oversizedBytes)
		procRoot, bootIDPath := createProcFixture(t, validStat, oversized, nil)
		assertFixtureError(t, procRoot, bootIDPath, nil, ErrInputTooLarge)
	})

	t.Run("malformed environ lacking trailing NUL", func(t *testing.T) {
		procRoot, bootIDPath := createProcFixture(t, validStat, []byte("tool\x00"), []byte("FOO=bar"))
		assertFixtureError(t, procRoot, bootIDPath, []string{"FOO"}, ErrMalformedEnviron)
	})

	t.Run("bare and empty-name environment entries are not selected variables", func(t *testing.T) {
		environ := []byte("NO_EQUALS\x00=empty-name\x00FOO=value\x00")
		procRoot, bootIDPath := createProcFixture(t, validStat, []byte("tool\x00"), environ)

		obs, err := observeLinux(t.Context(), procRoot, bootIDPath, Request{
			PID:             testPID,
			ExpectedBirth:   Identity{BootID: testBootID, StartTime: testStartTime},
			EnvironmentKeys: []string{"NO_EQUALS", "FOO"},
		})
		if err != nil {
			t.Fatalf("observeLinux: %v", err)
		}

		if len(obs.Environment) != 2 || obs.Environment[0].State != "absent" || string(obs.Environment[1].Value) != "value" {
			t.Fatalf("unexpected selected environment: %+v", obs.Environment)
		}
	})

	t.Run("duplicate requested environment variable with same value", func(t *testing.T) {
		environ := []byte("FOO=bar\x00FOO=bar\x00")
		procRoot, bootIDPath := createProcFixture(t, validStat, []byte("tool\x00"), environ)
		assertFixtureError(t, procRoot, bootIDPath, []string{"FOO"}, ErrDuplicateEnvironmentVariable)
	})

	t.Run("duplicate requested environment variable with different value", func(t *testing.T) {
		environ := []byte("FOO=bar\x00FOO=baz\x00")
		procRoot, bootIDPath := createProcFixture(t, validStat, []byte("tool\x00"), environ)
		assertFixtureError(t, procRoot, bootIDPath, []string{"FOO"}, ErrDuplicateEnvironmentVariable)
	})

	t.Run("duplicate unrequested environment variable ignored", func(t *testing.T) {
		environ := []byte("OTHER=1\x00OTHER=2\x00FOO=target\x00")
		procRoot, bootIDPath := createProcFixture(t, validStat, []byte("tool\x00"), environ)

		obs, err := observeLinux(t.Context(), procRoot, bootIDPath, Request{
			PID:             testPID,
			ExpectedBirth:   Identity{BootID: testBootID, StartTime: testStartTime},
			EnvironmentKeys: []string{"FOO"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(obs.Environment) != 1 || string(obs.Environment[0].Value) != "target" {
			t.Errorf("unexpected observed env: %+v", obs.Environment)
		}
	})

	t.Run("oversized environ exceeding bound", func(t *testing.T) {
		oversized := make([]byte, oversizedBytes)
		procRoot, bootIDPath := createProcFixture(t, validStat, []byte("tool\x00"), oversized)
		assertFixtureError(t, procRoot, bootIDPath, []string{"FOO"}, ErrInputTooLarge)
	})

	t.Run("missing boot_id", func(t *testing.T) {
		procRoot, _ := createProcFixture(t, validStat, []byte("tool\x00"), nil)
		missingBootIDPath := filepath.Join(procRoot, "non_existent_boot_id")
		assertFixtureError(t, procRoot, missingBootIDPath, nil, os.ErrNotExist)
	})
}

func TestReadOpenProcLinkPinsPathAndDeletionEvidence(t *testing.T) {
	tempDir := t.TempDir()
	firstPath := filepath.Join(tempDir, "first")
	secondPath := filepath.Join(tempDir, "second")
	linkPath := filepath.Join(tempDir, "current")

	for _, path := range []string{firstPath, secondPath} {
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatalf("write target: %v", err)
		}
	}

	if err := os.Symlink(firstPath, linkPath); err != nil {
		t.Fatalf("create initial link: %v", err)
	}

	fd, err := unix.Open(linkPath, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open pinned target: %v", err)
	}

	t.Cleanup(func() { _ = unix.Close(fd) })

	if err := os.Remove(linkPath); err != nil {
		t.Fatalf("remove initial link: %v", err)
	}

	if err := os.Symlink(secondPath, linkPath); err != nil {
		t.Fatalf("retarget link: %v", err)
	}

	if err := os.Remove(firstPath); err != nil {
		t.Fatalf("unlink pinned target: %v", err)
	}

	gotPath, deleted, err := readOpenProcLink(t.Context(), fd)
	if err != nil {
		t.Fatalf("read pinned target: %v", err)
	}

	if string(gotPath) != firstPath {
		t.Fatalf("pinned path = %q, want %q", gotPath, firstPath)
	}

	if !deleted {
		t.Fatal("pinned target deletion was not reported")
	}
}

func TestProcessGoneClassificationIncludesPinnedProcReadESRCH(t *testing.T) {
	err := &os.PathError{Op: "read", Path: "/private/proc-entry", Err: unix.ESRCH}
	if !isProcessGone(err) {
		t.Fatal("wrapped ESRCH was not classified as a vanished process")
	}
}

func TestClassifyProcLinkRejectsChangingDeletionEvidence(t *testing.T) {
	_, _, err := classifyProcLink([]byte("/tmp/work (deleted)"), uint64(1), uint64(0))
	if !errors.Is(err, errProcLinkChanged) {
		t.Fatalf("classifyProcLink error = %v, want %v", err, errProcLinkChanged)
	}
}

func TestClassifyProcLinkAcceptsLinkCountChangesOfLiveTarget(t *testing.T) {
	path, deleted, err := classifyProcLink([]byte("/tmp/work"), uint64(3), uint64(4))
	if err != nil {
		t.Fatalf("classifyProcLink: %v", err)
	}

	if string(path) != "/tmp/work" || deleted {
		t.Fatalf("classifyProcLink = (%q, %t), want live path", path, deleted)
	}
}

func TestClassifyProcLinkRejectsPositiveLinkDeletedSuffix(t *testing.T) {
	_, _, err := classifyProcLink([]byte("/tmp/work (deleted)"), uint64(1), uint64(1))
	if !errors.Is(err, ErrAmbiguousProcLink) {
		t.Fatalf("classifyProcLink error = %v, want %v", err, ErrAmbiguousProcLink)
	}
}

func TestClassifyProcLinkStripsOneSuffixWithZeroLinks(t *testing.T) {
	path, deleted, err := classifyProcLink([]byte("/tmp/work (deleted) (deleted)"), uint64(0), uint64(0))
	if err != nil {
		t.Fatalf("classifyProcLink: %v", err)
	}

	if string(path) != "/tmp/work (deleted)" || !deleted {
		t.Fatalf("classifyProcLink = (%q, %t), want literal suffixed path marked deleted", path, deleted)
	}
}

func TestAmbiguousProcLinkErrorWrappingPreservesSentinelAndCode(t *testing.T) {
	err := wrapError(ErrAmbiguousProcLink, "observe")
	if !errors.Is(err, ErrAmbiguousProcLink) {
		t.Fatalf("wrapped error = %v, want %v in cause chain", err, ErrAmbiguousProcLink)
	}

	oopsErr, ok := oops.AsOops(err)
	if !ok {
		t.Fatal("expected oops error")
	}

	if oopsErr.Code() != CodeAmbiguousProcLink {
		t.Fatalf("error code = %q, want %q", oopsErr.Code(), CodeAmbiguousProcLink)
	}
}

func TestParseStat_Valid(t *testing.T) {
	t.Run("empty comm permitted (prctl)", func(t *testing.T) {
		emptyCommStat := []byte("42 () S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 123456 0\n")

		state, startTime, err := parseStat(emptyCommStat)
		if err != nil {
			t.Fatalf("parseStat failed on empty comm: %v", err)
		}

		if state != 'S' || startTime != 123456 {
			t.Errorf("expected state S and startTime 123456, got %c, %d", state, startTime)
		}
	})

	t.Run("comm with parentheses", func(t *testing.T) {
		complexCommStat := []byte("42 (complex (comm) name) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 123456 0\n")

		state, startTime, err := parseStat(complexCommStat)
		if err != nil {
			t.Fatalf("parseStat failed on complex comm: %v", err)
		}

		if state != 'S' || startTime != 123456 {
			t.Errorf("expected state S and startTime 123456, got %c, %d", state, startTime)
		}
	})

	t.Run("startTime zero permitted", func(t *testing.T) {
		zeroStartStat := []byte("1 (init) S 0 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 0 0\n")

		state, startTime, err := parseStat(zeroStartStat)
		if err != nil {
			t.Fatalf("parseStat failed for startTime zero: %v", err)
		}

		if state != 'S' || startTime != 0 {
			t.Errorf("expected state S and startTime 0, got %c, %d", state, startTime)
		}
	})
}

func TestParseStat_Invalid(t *testing.T) {
	t.Run("PID prefix zero rejected", func(t *testing.T) {
		zeroPIDStat := []byte("0 (test) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 123456 0\n")

		_, _, err := parseStat(zeroPIDStat)
		if !errors.Is(err, ErrMalformedStat) {
			t.Errorf("expected ErrMalformedStat for PID zero, got: %v", err)
		}
	})

	t.Run("PID prefix negative rejected", func(t *testing.T) {
		negPIDStat := []byte("-1 (test) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 123456 0\n")

		_, _, err := parseStat(negPIDStat)
		if !errors.Is(err, ErrMalformedStat) {
			t.Errorf("expected ErrMalformedStat for negative PID, got: %v", err)
		}
	})

	t.Run("multi-byte state rejected", func(t *testing.T) {
		multiByteStateStat := []byte("42 (test) SS 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 123456 0\n")

		_, _, err := parseStat(multiByteStateStat)
		if !errors.Is(err, ErrMalformedStat) {
			t.Errorf("expected ErrMalformedStat for multi-byte state, got: %v", err)
		}
	})

	t.Run("unknown state rejected", func(t *testing.T) {
		unknownStateStat := []byte("42 (test) ? 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 123456 0\n")

		_, _, err := parseStat(unknownStateStat)
		if !errors.Is(err, ErrMalformedStat) {
			t.Errorf("expected ErrMalformedStat for unknown state, got: %v", err)
		}
	})

	t.Run("truncated fields rejected", func(t *testing.T) {
		truncatedStat := []byte("42 (test) S 1 2 3\n")

		_, _, err := parseStat(truncatedStat)
		if !errors.Is(err, ErrMalformedStat) {
			t.Errorf("expected ErrMalformedStat for truncated stat, got: %v", err)
		}
	})

	t.Run("non-numeric start time rejected", func(t *testing.T) {
		nonNumericStat := []byte("42 (test) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 not_a_number 0\n")

		_, _, err := parseStat(nonNumericStat)
		if !errors.Is(err, ErrMalformedStat) {
			t.Errorf("expected ErrMalformedStat for non-numeric start time, got: %v", err)
		}
	})
}

func TestObserve_SyntheticPrivatePathSanitization(t *testing.T) {
	privateProcRoot := "/private/super/secret/proc/path"
	privateBootID := filepath.Join(privateProcRoot, "boot_id")

	_, err := observeLinux(t.Context(), privateProcRoot, privateBootID, Request{
		PID:             testPID,
		ExpectedBirth:   Identity{BootID: testBootID, StartTime: testStartTime},
		EnvironmentKeys: nil,
	})
	if err == nil {
		t.Fatal("expected error with synthetic private path, got nil")
	}

	wrapped := wrapError(err, "observe")
	if strings.Contains(wrapped.Error(), "private") || strings.Contains(wrapped.Error(), "secret") {
		t.Fatalf("error leaked private procfs path: %s", wrapped.Error())
	}

	if pathErr, ok := errors.AsType[*os.PathError](wrapped); ok {
		if pathErr.Path != "[sanitized]" {
			t.Errorf("expected sanitized Path in os.PathError, got %q", pathErr.Path)
		}
	} else {
		t.Errorf("expected os.PathError in error cause chain, got %v", wrapped)
	}
}
