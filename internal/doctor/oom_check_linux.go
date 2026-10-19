//go:build linux

package doctor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"
)

const oomPolicyQueryTimeout = 3 * time.Second

var errCgroupUninspected = errors.New("unified process cgroup could not be determined")

func serviceUnitOf(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return "", fmt.Errorf("read process cgroup: %w", err)
	}

	return unitFromCgroup(data)
}

func unitFromCgroup(data []byte) (string, error) {
	for line := range bytes.SplitSeq(bytes.TrimSpace(data), []byte("\n")) {
		cgroupPath, found := strings.CutPrefix(string(line), "0::")
		if !found || !path.IsAbs(cgroupPath) {
			continue
		}

		// Only the leaf counts: a server started from a terminal sits in a scope under the user
		// manager, whose own service must not be mistaken for it.
		if name := path.Base(cgroupPath); strings.HasSuffix(name, ".service") {
			return name, nil
		}

		return "", errNoServiceUnit
	}

	return "", errCgroupUninspected
}

func serviceOOMPolicyOf(ctx context.Context, unit string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, oomPolicyQueryTimeout)
	defer cancel()

	output, err := exec.CommandContext(ctx, "systemctl", "--user", "show", "--property=OOMPolicy", "--value", unit).Output()
	if err != nil {
		return "", fmt.Errorf("query unit oom policy: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}
