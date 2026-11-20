//go:build linux && integration

package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func waitForFixtureIdle(t *testing.T, root string) {
	t.Helper()

	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		writers := fixtureWriters(root)
		if len(writers) == 0 {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("fixture still written by %v", writers)
		}
	}
}

func fixtureWriters(root string) []string {
	var writers []string

	processes, err := os.ReadDir("/proc")
	if err != nil {
		return writers
	}

	for _, process := range processes {
		pid := process.Name()
		if _, err := strconv.Atoi(pid); err != nil {
			continue
		}

		command := strings.TrimSpace(
			readProcFile("/proc/"+pid+"/cmdline") + " " + readProcFile("/proc/"+pid+"/comm"),
		)

		if strings.Contains(command, root) {
			writers = append(writers, fmt.Sprintf("pid=%s argv=%s", pid, command))
		}

		if link, err := os.Readlink(
			"/proc/" + pid + "/cwd",
		); err == nil &&
			strings.HasPrefix(link, root) {
			writers = append(writers, fmt.Sprintf("pid=%s cwd=%s cmd=%s", pid, link, command))
		}

		fds, err := os.ReadDir("/proc/" + pid + "/fd")
		if err != nil {
			continue
		}

		for _, fd := range fds {
			link, err := os.Readlink("/proc/" + pid + "/fd/" + fd.Name())
			if err == nil && strings.HasPrefix(link, root) {
				writers = append(writers, fmt.Sprintf("pid=%s fd=%s cmd=%s", pid, link, command))
			}
		}
	}

	return writers
}

func readProcFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	return strings.ReplaceAll(string(data), "\x00", " ")
}

func fixtureRoot(fixture *cliSessionFixture) string {
	return filepath.Dir(filepath.Dir(fixture.repository))
}
