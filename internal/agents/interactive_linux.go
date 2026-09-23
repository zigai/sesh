//go:build linux

package agents

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func agentProcessArguments(pid int) []string {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil || len(data) == 0 {
		return nil
	}

	return strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
}
