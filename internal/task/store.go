package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/samber/oops"
	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/strictjson"
)

const (
	LedgerVersion = 1

	KindProject Kind = "project"
	KindLoose   Kind = "loose"

	StateCreating  State = "creating"
	StateActive    State = "active"
	StateSleeping  State = "sleeping"
	StateWaking    State = "waking"
	StateFinishing State = "finishing"
	StateFailed    State = "failed"

	PROpen   PRState = "open"
	PRMerged PRState = "merged"
	PRClosed PRState = "closed"

	RunRunning RunStatus = "running"
	RunOK      RunStatus = "ok"
	RunFailed  RunStatus = "failed"

	maxLedgerBytes = 8 << 20
	idPrefix       = "t"
)

var (
	ErrLedgerPath    = errors.New("task ledger path must be absolute and clean")
	ErrLedgerVersion = errors.New("unsupported task ledger version")
	ErrLedgerInvalid = errors.New("task ledger is inconsistent")
	ErrLedgerTooBig  = errors.New("task ledger exceeds its size limit")
	ErrUncertain     = errors.New("task ledger publication durability uncertain")
	ErrNilUpdate     = errors.New("task ledger update is nil")
)

type (
	Kind      string
	State     string
	PRState   string
	RunStatus string
)

// Record is one task. A project task works in a worktree or the main checkout of its project; a
// loose task runs in a plain directory. Number counts a project's tasks and is never reused.
type Record struct {
	ID        string `json:"id"`
	Kind      Kind   `json:"kind"`
	State     State  `json:"state"`
	Project   string `json:"project"`
	Number    int    `json:"number"`
	Harness   string `json:"harness"`
	Directory string `json:"directory"`
	Branch    string `json:"branch"`
	Base      string `json:"base"`
	// OwnsWorktree and OwnsBranch record what sesh created for the task, which is all that
	// finishing it removes.
	OwnsWorktree bool `json:"ownsWorktree"`
	OwnsBranch   bool `json:"ownsBranch"`
	// Upstream is the remote branch an owned branch was created to track; finishing compares the
	// branch with it instead of the base.
	Upstream  string `json:"upstream"`
	CreatedAt int64  `json:"createdAt"`
	// Title is the window name sesh last gave the task; a different window name means the
	// user renamed it and sesh leaves it alone.
	Title  string      `json:"title"`
	Resume Resume      `json:"resume"`
	PR     PullRequest `json:"pr"`
	Runs   []Run       `json:"runs"`
	// Owner is the sesh process that put the task in its creating or finishing state; once it
	// is gone the task is stalled there.
	Owner Process `json:"owner"`
}

// Process identifies a process across PID reuse by its birth identity.
type Process struct {
	PID   int    `json:"pid"`
	Birth string `json:"birth"`
}

type Resume struct {
	Harness string   `json:"harness"`
	Session string   `json:"session"`
	Command []string `json:"command"`
}

type PullRequest struct {
	Number int     `json:"number"`
	State  PRState `json:"state"`
	URL    string  `json:"url"`
}

// Run is the latest execution of a named task action, in a window of the task's popup session.
type Run struct {
	Action     string    `json:"action"`
	Window     string    `json:"window"`
	Status     RunStatus `json:"status"`
	ExitCode   int       `json:"exitCode"`
	StartedAt  int64     `json:"startedAt"`
	FinishedAt int64     `json:"finishedAt"`
}

type ProjectState struct {
	Root       string `json:"root"`
	LastNumber int    `json:"lastNumber"`
}

type Ledger struct {
	Version  int            `json:"version"`
	LastID   int            `json:"lastId"`
	Tasks    []Record       `json:"tasks"`
	Projects []ProjectState `json:"projects"`
}

// Store reads and updates the ledger file. Updates hold an exclusive lock, so concurrent sesh
// processes never lose each other's records.
type Store struct {
	path string
}

func NewStore(path string) Store {
	return Store{path: path}
}

// Load returns the current ledger; a missing file is an empty ledger.
func (s Store) Load(ctx context.Context) (Ledger, error) {
	dir, name, err := s.open(false)
	if errors.Is(err, os.ErrNotExist) {
		return emptyLedger(), nil
	}

	if err != nil {
		return Ledger{}, ledgerError("load_tasks", err)
	}

	ledger, err := read(dir, name)

	return ledger, ledgerError("load_tasks", errors.Join(err, dir.Close(), ctx.Err()))
}

// Update applies change to the locked ledger and publishes the result. A change that returns
// an error leaves the file as it was.
func (s Store) Update(ctx context.Context, change func(*Ledger) error) (Ledger, error) {
	if change == nil {
		return Ledger{}, ledgerError("update_tasks", ErrNilUpdate)
	}

	dir, name, err := s.open(true)
	if err != nil {
		return Ledger{}, ledgerError("update_tasks", err)
	}

	lock, err := dir.Lock(ctx, name+".lock")
	if err != nil {
		return Ledger{}, ledgerError("update_tasks", errors.Join(err, dir.Close()))
	}

	ledger, err := update(ctx, dir, name, change)

	cleanupErr := errors.Join(lock.Close(), dir.Close())
	if err == nil && cleanupErr != nil {
		err = errors.Join(ErrUncertain, cleanupErr)
	} else if err != nil {
		err = errors.Join(err, cleanupErr)
	}

	return ledger, ledgerError("update_tasks", err)
}

func (l *Ledger) Find(id string) (Record, bool) {
	for _, record := range l.Tasks {
		if record.ID == id {
			return record, true
		}
	}

	return EmptyRecord(), false
}

func (r Record) PREnded() bool {
	return r.PR.State == PRMerged || r.PR.State == PRClosed
}

func (l *Ledger) Remove(id string) {
	l.Tasks = slices.DeleteFunc(l.Tasks, func(record Record) bool { return record.ID == id })
}

func EmptyRecord() Record {
	return Record{
		ID: "", Kind: "", State: "", Project: "", Number: 0, Harness: "", Directory: "", Branch: "", Base: "",
		OwnsWorktree: false, OwnsBranch: false, Upstream: "", CreatedAt: 0,
		Title: "", Resume: Resume{Harness: "", Session: "", Command: []string{}}, PR: PullRequest{Number: 0, State: "", URL: ""}, Runs: []Run{},
		Owner: Process{PID: 0, Birth: ""},
	}
}

func ValidateRecords(records []Record) error {
	ids := make(map[string]bool, len(records))

	for _, record := range records {
		if record.ID == "" || ids[record.ID] || (record.Kind != KindProject && record.Kind != KindLoose) {
			return ErrLedgerInvalid
		}

		if !validState(record.State) {
			return ErrLedgerInvalid
		}

		ids[record.ID] = true
	}

	return nil
}

func validState(state State) bool {
	switch state {
	case StateCreating, StateActive, StateSleeping, StateWaking, StateFinishing, StateFailed:
		return true
	default:
		return false
	}
}

func (l *Ledger) Project(root string) ProjectState {
	for _, state := range l.Projects {
		if state.Root == root {
			return state
		}
	}

	return ProjectState{Root: root, LastNumber: 0}
}

func (l *Ledger) NextID() string {
	l.LastID++

	return idPrefix + strconv.Itoa(l.LastID)
}

func (l *Ledger) SetProject(state ProjectState) {
	for index := range l.Projects {
		if l.Projects[index].Root == state.Root {
			l.Projects[index] = state

			return
		}
	}

	l.Projects = append(l.Projects, state)
}

func (l *Ledger) Put(record Record) {
	for index := range l.Tasks {
		if l.Tasks[index].ID == record.ID {
			l.Tasks[index] = record

			return
		}
	}

	l.Tasks = append(l.Tasks, record)
}

func (l *Ledger) validate() error {
	if l.Version != LedgerVersion {
		return ErrLedgerVersion
	}

	if err := ValidateRecords(l.Tasks); err != nil {
		return err
	}

	roots := make(map[string]bool, len(l.Projects))

	for _, state := range l.Projects {
		if roots[state.Root] || state.LastNumber < 0 {
			return ErrLedgerInvalid
		}

		roots[state.Root] = true
	}

	return nil
}

func (s Store) open(create bool) (*privatefs.Dir, string, error) {
	if !filepath.IsAbs(s.path) || filepath.Clean(s.path) != s.path {
		return nil, "", ErrLedgerPath
	}

	if create {
		dir, err := privatefs.Ensure(filepath.Dir(s.path))

		return dir, filepath.Base(s.path), err
	}

	dir, err := privatefs.Open(filepath.Dir(s.path), false)

	return dir, filepath.Base(s.path), err
}

func update(ctx context.Context, dir *privatefs.Dir, name string, change func(*Ledger) error) (Ledger, error) {
	ledger, err := read(dir, name)
	if err != nil {
		return Ledger{}, err
	}

	if err := change(&ledger); err != nil {
		return Ledger{}, err
	}

	ledger.Version = LedgerVersion
	if err := ledger.validate(); err != nil {
		return Ledger{}, err
	}

	if err := ctx.Err(); err != nil {
		return Ledger{}, fmt.Errorf("task ledger update canceled: %w", err)
	}

	return ledger, write(dir, name, ledger)
}

func read(dir *privatefs.Dir, name string) (Ledger, error) {
	data, err := dir.Read(name, maxLedgerBytes)
	if errors.Is(err, os.ErrNotExist) {
		return emptyLedger(), nil
	}

	if errors.Is(err, privatefs.ErrUnsafe) {
		return Ledger{}, errors.Join(ErrLedgerTooBig, err)
	}

	if err != nil {
		return Ledger{}, fmt.Errorf("read task ledger: %w", err)
	}

	ledger, err := strictjson.Decode[Ledger](data)
	if err != nil {
		return Ledger{}, errors.Join(ErrLedgerInvalid, err)
	}

	return ledger, ledger.validate()
}

func write(dir *privatefs.Dir, name string, ledger Ledger) error {
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return fmt.Errorf("encode task ledger: %w", err)
	}

	if len(data) > maxLedgerBytes {
		return ErrLedgerTooBig
	}

	temp := fmt.Sprintf(".%s.tmp.%d.%d", name, os.Getpid(), time.Now().UnixNano())

	file, err := dir.File(temp, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return fmt.Errorf("create temporary task ledger: %w", err)
	}

	_, writeErr := file.Write(data)
	syncErr := file.Sync()

	if err := errors.Join(writeErr, syncErr, file.Close()); err != nil {
		return errors.Join(fmt.Errorf("write temporary task ledger: %w", err), dir.Remove(temp))
	}

	if err := dir.Rename(temp, dir, name); err != nil {
		return errors.Join(fmt.Errorf("publish task ledger: %w", err), dir.Remove(temp))
	}

	if err := dir.Sync(); err != nil {
		return errors.Join(ErrUncertain, err)
	}

	return nil
}

func emptyLedger() Ledger {
	return Ledger{Version: LedgerVersion, LastID: 0, Tasks: []Record{}, Projects: []ProjectState{}}
}

func ledgerError(operation string, err error) error {
	if err == nil {
		return nil
	}

	return oops.In("task").Code("task_ledger_failed").With("operation", operation).Wrap(err)
}
