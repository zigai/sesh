//go:build linux

package agents

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const kernelProcessDir = "/proc"

func agentProcessArguments(pid int) []string {
	data, err := os.ReadFile(filepath.Join(kernelProcessDir, strconv.Itoa(pid), "cmdline"))
	if err != nil || len(data) == 0 {
		return nil
	}

	return strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
}
