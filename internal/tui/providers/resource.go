package providers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/display/layout"
)

const (
	resourceTickInterval = 2 * time.Second
	resourceCacheAge     = 10 * time.Second
	cpuTicksPerSecond    = 100
	percentScale         = 100
	processStatFields    = 20
	residentFieldCount   = 2
	bytesPerMiB          = 1024 * 1024
)

var (
	ErrResourceUnsupported = errors.New("resource sampling unsupported")
	errInvalidProcessStat  = errors.New("invalid process stat")
	errShortProcessStat    = errors.New("short process stat")
	errInvalidPageSize     = errors.New("invalid page size")
)

type ProcessSample struct {
	PID       int    `json:"pid"`
	ParentPID int    `json:"parentPid"`
	StartTime uint64 `json:"startTime"`
	CPUTicks  uint64 `json:"cpuTicks"`
	RSSBytes  uint64 `json:"rssBytes"`
}

type ResourceSample struct {
	At        time.Time             `json:"at"`
	Processes map[int]ProcessSample `json:"processes"`
}

type ResourceTarget struct {
	PaneID      string
	PanePID     int
	WindowIndex int
	SessionKey  string
	Remote      bool
}

type resourceTotals struct {
	cpu     float64
	memory  uint64
	pending bool
}

func ResourceCadence() Cadence {
	return Cadence{Kind: CadencePoll, Interval: resourceTickInterval}
}

func ReadResourceSample(ctx context.Context, procRoot string, at time.Time) (ResourceSample, error) {
	if runtime.GOOS != "linux" {
		return ResourceSample{}, ErrResourceUnsupported
	}

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

func ResourceRows(sample, previous ResourceSample, targets []ResourceTarget) Rows {
	rows := make(Rows, len(targets))
	paneTotals, windowTotals, sessionTotals := aggregateResourceTargets(sample, previous, targets)

	for _, target := range targets {
		if target.PaneID == "" {
			continue
		}

		if target.Remote || runtime.GOOS != "linux" {
			rows[target.PaneID] = resourceFields(layout.Unsupported)
			continue
		}

		pane, ok := paneTotals[target.PaneID]
		if !ok {
			rows[target.PaneID] = resourceFields(layout.Absent)
			continue
		}

		window := windowTotals[target.SessionKey+":"+strconv.Itoa(target.WindowIndex)]
		session := sessionTotals[target.SessionKey]
		rows[target.PaneID] = map[string]layout.Cell{
			display.FieldPaneCPU: cpuCell(pane), display.FieldPaneMem: memoryCell(pane),
			display.FieldWindowCPU: cpuCell(window), display.FieldWindowMem: memoryCell(window),
			display.FieldSessionCPU: cpuCell(session), display.FieldSessionMem: memoryCell(session),
		}
	}

	return rows
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

func aggregateResourceTargets(sample, previous ResourceSample, targets []ResourceTarget) (map[string]resourceTotals, map[string]resourceTotals, map[string]resourceTotals) {
	children := make(map[int][]int)
	for pid, process := range sample.Processes {
		children[process.ParentPID] = append(children[process.ParentPID], pid)
	}

	paneTotals := make(map[string]resourceTotals, len(targets))
	windowTotals := make(map[string]resourceTotals)
	sessionTotals := make(map[string]resourceTotals)

	for _, target := range targets {
		if target.Remote || target.PaneID == "" || target.PanePID <= 0 {
			continue
		}

		total, found := measurePaneSubtree(target.PanePID, sample, previous, children)
		if !found {
			continue
		}

		paneTotals[target.PaneID] = total
		windowKey := target.SessionKey + ":" + strconv.Itoa(target.WindowIndex)
		windowTotals[windowKey] = addResourceTotals(windowTotals[windowKey], total)
		sessionTotals[target.SessionKey] = addResourceTotals(sessionTotals[target.SessionKey], total)
	}

	return paneTotals, windowTotals, sessionTotals
}

func addResourceTotals(current, next resourceTotals) resourceTotals {
	current.cpu += next.cpu
	current.memory += next.memory
	current.pending = current.pending || next.pending

	return current
}

func measurePaneSubtree(rootPID int, sample, previous ResourceSample, children map[int][]int) (resourceTotals, bool) {
	var total resourceTotals

	seen := make(map[int]bool)
	stack := []int{rootPID}
	found := false
	delta := sample.At.Sub(previous.At)
	canUsePrevious := delta > 0 && delta <= resourceCacheAge

	for len(stack) > 0 {
		pid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if seen[pid] {
			continue
		}

		seen[pid] = true

		process, ok := sample.Processes[pid]
		if !ok {
			continue
		}

		found = true

		total.memory += process.RSSBytes
		if prior, ok := previous.Processes[pid]; canUsePrevious && ok && prior.StartTime == process.StartTime && process.CPUTicks >= prior.CPUTicks {
			total.cpu += float64(process.CPUTicks-prior.CPUTicks) / cpuTicksPerSecond / delta.Seconds() * percentScale
		} else {
			total.pending = true
		}

		stack = append(stack, children[pid]...)
	}

	return total, found
}

func resourceFields(state layout.State) map[string]layout.Cell {
	fields := make(map[string]layout.Cell)
	for _, field := range display.FieldsByProvider(display.ProviderResource) {
		fields[field] = layout.Cell{Text: "", State: state, Role: "", Variants: nil}
	}

	return fields
}

func cpuCell(total resourceTotals) layout.Cell {
	if total.pending {
		return layout.Cell{Text: "", State: layout.Pending, Role: "", Variants: nil}
	}

	return layout.Cell{Text: strconv.FormatFloat(total.cpu, 'f', 0, 64) + "%", State: layout.Present, Role: "", Variants: nil}
}

func memoryCell(total resourceTotals) layout.Cell {
	return layout.Cell{Text: strconv.FormatUint(total.memory/bytesPerMiB, 10) + "MiB", State: layout.Present, Role: "", Variants: nil}
}
