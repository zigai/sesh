//go:build linux

package providers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	ResourceSupported = true

	processStatFields  = 20
	residentFieldCount = 2
)

var (
	errInvalidProcessStat = errors.New("invalid process stat")
	errShortProcessStat   = errors.New("short process stat")
	errInvalidPageSize    = errors.New("invalid page size")
)

func ReadResourceSample(ctx context.Context, procRoot string, at time.Time) (ResourceSample, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return ResourceSample{}, fmt.Errorf("read process inventory: %w", err)
	}

	pageSize := os.Getpagesize()
	if pageSize <= 0 {
		return ResourceSample{}, errInvalidPageSize
	}

	pageBytes := uint64(pageSize)
	sample := ResourceSample{At: at, Processes: make(map[int]ProcessSample)}

	for _, entry := range entries {
		if ctx.Err() != nil {
			return ResourceSample{}, fmt.Errorf("read resource sample: %w", ctx.Err())
		}

		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}

		if process, ok := readProcessSample(procRoot, entry.Name(), pid, pageBytes); ok {
			sample.Processes[pid] = process
		}
	}

	return sample, nil
}

func readProcessSample(procRoot, name string, pid int, pageBytes uint64) (ProcessSample, bool) {
	stat, err := os.ReadFile(filepath.Join(procRoot, name, "stat"))
	if err != nil {
		return ProcessSample{PID: 0, ParentPID: 0, StartTime: 0, CPUTicks: 0, RSSBytes: 0}, false
	}

	process, err := parseProcessStat(pid, string(stat))
	if err != nil {
		return ProcessSample{PID: 0, ParentPID: 0, StartTime: 0, CPUTicks: 0, RSSBytes: 0}, false
	}

	statm, err := os.ReadFile(filepath.Join(procRoot, name, "statm"))
	if err != nil {
		return process, true
	}

	fields := strings.Fields(string(statm))
	if len(fields) >= residentFieldCount {
		if resident, parseErr := strconv.ParseUint(fields[1], 10, 64); parseErr == nil {
			process.RSSBytes = resident * pageBytes
		}
	}

	return process, true
}

func parseProcessStat(pid int, value string) (ProcessSample, error) {
	closeIndex := strings.LastIndexByte(value, ')')
	if closeIndex < 0 || closeIndex+2 >= len(value) {
		return ProcessSample{}, errInvalidProcessStat
	}

	fields := strings.Fields(value[closeIndex+2:])
	if len(fields) < processStatFields {
		return ProcessSample{}, errShortProcessStat
	}

	parent, parentErr := strconv.Atoi(fields[1])
	user, userErr := strconv.ParseUint(fields[11], 10, 64)
	system, systemErr := strconv.ParseUint(fields[12], 10, 64)

	start, startErr := strconv.ParseUint(fields[19], 10, 64)
	if err := errors.Join(parentErr, userErr, systemErr, startErr); err != nil {
		return ProcessSample{}, err
	}

	return ProcessSample{PID: pid, ParentPID: parent, StartTime: start, CPUTicks: user + system, RSSBytes: 0}, nil
}
