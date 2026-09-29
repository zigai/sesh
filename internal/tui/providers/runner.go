package providers

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/display/layout"
)

const (
	Git             ID = "git"
	Clock           ID = "clock"
	Resource        ID = "resource"
	GH              ID = "gh"
	AHT             ID = "aht"
	gitPollInterval    = 2 * time.Second
)

const (
	CadenceEvent CadenceKind = iota
	CadenceClock
	CadencePoll
)

type ID string

type CadenceKind uint8

type Cadence struct {
	Kind     CadenceKind
	Interval time.Duration
}

type Definition struct {
	ID      ID
	Fields  []string
	Cadence Cadence
}

type Rows map[string]map[string]layout.Cell

type Snapshot struct {
	Version uint64
	Rows    Rows
}

type Runner struct {
	entries map[ID]*entry
}

type entry struct {
	request  uint64
	inFlight bool
	cancel   context.CancelFunc
	snapshot Snapshot
}

func Definitions() []Definition {
	return []Definition{
		{ID: Git, Fields: display.FieldsByProvider(display.ProviderGit), Cadence: Cadence{Kind: CadencePoll, Interval: gitPollInterval}},
		{ID: Clock, Fields: display.FieldsByProvider(display.ProviderClock), Cadence: Cadence{Kind: CadenceClock, Interval: time.Second}},
		{ID: Resource, Fields: display.FieldsByProvider(display.ProviderResource), Cadence: ResourceCadence()},
		{ID: GH, Fields: display.FieldsByProvider(display.ProviderGH), Cadence: Cadence{Kind: CadencePoll, Interval: time.Minute}},
		{ID: AHT, Fields: display.FieldsByProvider(display.ProviderAHT), Cadence: Cadence{Kind: CadenceEvent, Interval: 0}},
	}
}

func DefinitionFor(id ID) (Definition, bool) {
	for _, definition := range Definitions() {
		if definition.ID == id {
			return definition, true
		}
	}

	var missing Definition

	return missing, false
}

func (r *Runner) Begin(ctx context.Context, id ID) (context.Context, uint64, bool) {
	current := r.get(id)
	if current.inFlight {
		return ctx, 0, false
	}

	current.request++
	current.inFlight = true
	workerCtx, cancel := context.WithCancel(ctx)
	current.cancel = cancel

	return workerCtx, current.request, true
}

func (r *Runner) Complete(id ID, request uint64, rows Rows) bool {
	current := r.get(id)
	if !current.inFlight || request != current.request {
		return false
	}

	current.cancel()
	current.cancel = nil
	current.inFlight = false
	current.snapshot = Snapshot{Version: current.snapshot.Version + 1, Rows: cloneRows(rows)}

	return true
}

func (r *Runner) Publish(id ID, rows Rows) uint64 {
	current := r.get(id)
	if rowsEqual(current.snapshot.Rows, rows) && current.snapshot.Version > 0 {
		return current.snapshot.Version
	}

	current.snapshot = Snapshot{Version: current.snapshot.Version + 1, Rows: cloneRows(rows)}

	return current.snapshot.Version
}

func (r *Runner) Invalidate(id ID) {
	current := r.get(id)

	current.request++
	if current.cancel != nil {
		current.cancel()
		current.cancel = nil
	}

	current.inFlight = false
}

func (r *Runner) Close() {
	for id := range r.entries {
		r.Invalidate(id)
	}
}

func (r *Runner) Snapshot(id ID) Snapshot {
	current := r.get(id)
	return Snapshot{Version: current.snapshot.Version, Rows: cloneRows(current.snapshot.Rows)}
}

func (r *Runner) Version(id ID) uint64 {
	return r.get(id).snapshot.Version
}

func (r *Runner) InFlight(id ID) bool {
	return r.get(id).inFlight
}

func (r *Runner) CurrentRequest(id ID) uint64 {
	return r.get(id).request
}

func (r *Runner) Cell(id ID, row, field string) layout.Cell {
	current := r.get(id)
	if current.snapshot.Version == 0 {
		return layout.Cell{Text: "", Role: "", Variants: nil, State: layout.Pending}
	}

	if fields, ok := current.snapshot.Rows[row]; ok {
		if cell, ok := fields[field]; ok {
			return cloneCell(cell)
		}
	}

	return layout.Cell{Text: "", Role: "", Variants: nil, State: layout.Absent}
}

func (r *Runner) get(id ID) *entry {
	if r.entries == nil {
		r.entries = make(map[ID]*entry)
	}

	if current, ok := r.entries[id]; ok {
		return current
	}

	current := &entry{request: 0, inFlight: false, cancel: nil, snapshot: Snapshot{Version: 0, Rows: nil}}
	r.entries[id] = current

	return current
}

func cloneRows(rows Rows) Rows {
	if rows == nil {
		return nil
	}

	copyRows := make(Rows, len(rows))
	for row, fields := range rows {
		copyFields := make(map[string]layout.Cell, len(fields))
		for field, cell := range fields {
			copyFields[field] = cloneCell(cell)
		}

		copyRows[row] = copyFields
	}

	return copyRows
}

func cloneCell(cell layout.Cell) layout.Cell {
	cell.Variants = slices.Clone(cell.Variants)
	return cell
}

func rowsEqual(a, b Rows) bool {
	return maps.EqualFunc(a, b, func(a, b map[string]layout.Cell) bool {
		return maps.EqualFunc(a, b, func(a, b layout.Cell) bool {
			return a.Text == b.Text && a.Role == b.Role && a.State == b.State && slices.Equal(a.Variants, b.Variants)
		})
	})
}
