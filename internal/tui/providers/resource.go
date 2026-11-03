package providers

import (
	"errors"
	"strconv"
	"time"

	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/display/layout"
)

const (
	resourceTickInterval = 2 * time.Second
	resourceCacheAge     = 10 * time.Second
	cpuTicksPerSecond    = 100
	percentScale         = 100
	bytesPerMiB          = 1024 * 1024
)

var ErrResourceUnsupported = errors.New("resource sampling unsupported")

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

func ResourceRows(sample, previous ResourceSample, targets []ResourceTarget) Rows {
	rows := make(Rows, len(targets))
	paneTotals, windowTotals, sessionTotals := aggregateResourceTargets(sample, previous, targets)

	for _, target := range targets {
		if target.PaneID == "" {
			continue
		}

		if target.Remote {
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
