package work

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
	"github.com/zigai/sesh/internal/task"
)

const (
	Version = 3

	FinishedByPR   FinishReason = "pr"
	FinishedByUser FinishReason = "user"

	LinkBranch    LinkSource = "branch"
	LinkUser      LinkSource = "user"
	LinkDismissed LinkSource = "dismissed"

	maxFileBytes = 8 << 20
	idPrefix     = "w"
)

var (
	ErrPath      = errors.New("work state path must be absolute and clean")
	ErrVersion   = errors.New("unsupported work state version")
	ErrInvalid   = errors.New("work state is inconsistent")
	ErrTooBig    = errors.New("work state exceeds its size limit")
	ErrUncertain = errors.New("work state publication durability uncertain")
	ErrNilUpdate = errors.New("work state update is nil")
	ErrNotFound  = errors.New("no work item has that ID")
	ErrNotLinked = errors.New("the pull request is not linked to the work item")
)

type (
	FinishReason string
	LinkSource   string
)

// Item is continued by a new conversation in the same pane, such as after the agent clears its
// context, until the item finishes.
type Item struct {
	ID           string   `json:"id"`
	Harness      string   `json:"harness"`
	Conversation string   `json:"conversation"`
	SessionPath  string   `json:"sessionPath"`
	Resume       []string `json:"resume"`
	Title        string   `json:"title"`
	Directory    string   `json:"directory"`
	Project      string   `json:"project"`
	Branch       string   `json:"branch"`
	Task         string   `json:"task"`
	PRs          []PRLink `json:"prs"`
	Server       string   `json:"server"`
	Pane         string   `json:"pane"`
	PanePID      int      `json:"panePid"`
	// CreatedAt, TurnAt (last activity change) and PromptAt (last user prompt, as far as the
	// harness reports it) are Unix seconds.
	CreatedAt int64 `json:"createdAt"`
	TurnAt    int64 `json:"turnAt"`
	PromptAt  int64 `json:"promptAt"`
	// PinnedAt, FinishedAt, ReopenedAt and UnseenAt are Unix seconds, zero when unset.
	PinnedAt   int64        `json:"pinnedAt"`
	FinishedAt int64        `json:"finishedAt"`
	FinishedBy FinishReason `json:"finishedBy"`
	ReopenedAt int64        `json:"reopenedAt"`
	UnseenAt   int64        `json:"unseenAt"`
}

type PRLink struct {
	Number int          `json:"number"`
	State  task.PRState `json:"state"`
	URL    string       `json:"url"`
	Issues []int        `json:"issues"`
	Source LinkSource   `json:"source"`
	// CreatedAt and EndedAt are Unix seconds, zero when unknown or still open.
	CreatedAt int64 `json:"createdAt"`
	EndedAt   int64 `json:"endedAt"`
}

type State struct {
	Version int    `json:"version"`
	LastID  int    `json:"lastId"`
	Items   []Item `json:"items"`
}

// Store updates hold an exclusive lock, so concurrent sesh processes never lose each other's
// changes.
type Store struct {
	path string
}

func NewStore(path string) Store {
	return Store{path: path}
}

func (s *State) Find(id string) (Item, bool) {
	for _, item := range s.Items {
		if item.ID == id {
			return item, true
		}
	}

	return EmptyItem(), false
}

func (s *State) Put(item Item) {
	for index := range s.Items {
		if s.Items[index].ID == item.ID {
			s.Items[index] = item

			return
		}
	}

	s.Items = append(s.Items, item)
}

func (s *State) Remove(id string) {
	s.Items = slices.DeleteFunc(s.Items, func(item Item) bool { return item.ID == id })
}

func (s *State) NextID() string {
	s.LastID++

	return idPrefix + strconv.Itoa(s.LastID)
}

func (s *State) Finish(id string, by FinishReason, now time.Time) error {
	return s.change(id, func(item *Item) error {
		item.FinishedAt, item.FinishedBy = now.Unix(), by

		return nil
	})
}

func (s *State) FinishTask(id string, by FinishReason, directory string, now time.Time) {
	for index := range s.Items {
		item := &s.Items[index]
		if item.Task != id {
			continue
		}

		if !item.Finished() {
			item.FinishedAt, item.FinishedBy = now.Unix(), by
		}

		item.Task = ""

		if directory != "" {
			item.Directory = directory
		}
	}
}

func (s *State) Reopen(id string, now time.Time) error {
	return s.change(id, func(item *Item) error {
		*item = item.reopened(now)

		return nil
	})
}

func (s *State) Pin(id string, now time.Time) error {
	return s.change(id, func(item *Item) error {
		item.PinnedAt = now.Unix()

		return nil
	})
}

func (s *State) MarkUnseen(id string, now time.Time) error {
	return s.change(id, func(item *Item) error {
		item.UnseenAt = now.Unix()

		return nil
	})
}

func (s *State) ClearUnseen(id string) error {
	return s.change(id, func(item *Item) error {
		item.UnseenAt = 0

		return nil
	})
}

func (s *State) Prune(cutoff time.Time) bool {
	count := len(s.Items)
	s.Items = slices.DeleteFunc(s.Items, func(item Item) bool { return item.Finished() && item.FinishedAt < cutoff.Unix() })

	return len(s.Items) != count
}

func (s *State) Unpin(id string) error {
	return s.change(id, func(item *Item) error {
		item.PinnedAt = 0

		return nil
	})
}

func (s *State) Link(id string, pr PRLink) error {
	return s.change(id, func(item *Item) error {
		pr.Source = LinkUser
		item.PRs = slices.DeleteFunc(item.PRs, func(linked PRLink) bool { return linked.Number == pr.Number })
		item.PRs = append(item.PRs, pr)

		return nil
	})
}

func (s *State) Unlink(id string, number int) error {
	return s.change(id, func(item *Item) error {
		index := slices.IndexFunc(item.PRs, func(linked PRLink) bool { return linked.Number == number && linked.Source != LinkDismissed })
		if index < 0 {
			return ErrNotLinked
		}

		item.PRs[index].Source = LinkDismissed

		return nil
	})
}

func (s *State) change(id string, apply func(*Item) error) error {
	index := slices.IndexFunc(s.Items, func(item Item) bool { return item.ID == id })
	if index < 0 {
		return ErrNotFound
	}

	return apply(&s.Items[index])
}

func (i Item) Finished() bool {
	return i.FinishedAt != 0
}

func (i Item) Pinned() bool {
	return i.PinnedAt != 0
}

func (i Item) Linked() []PRLink {
	return slices.DeleteFunc(slices.Clone(i.PRs), func(pr PRLink) bool { return pr.Source == LinkDismissed })
}

func (i Item) PRsEnded() bool {
	linked := i.Linked()

	return len(linked) > 0 && !slices.ContainsFunc(linked, func(pr PRLink) bool { return !pr.Ended() })
}

func (i Item) Issues() []int {
	var issues []int
	for _, pr := range i.Linked() {
		issues = append(issues, pr.Issues...)
	}

	slices.Sort(issues)

	return slices.Compact(issues)
}

func (i Item) reopened(now time.Time) Item {
	i.FinishedAt, i.FinishedBy, i.ReopenedAt = 0, "", now.Unix()

	return i
}

func (pr PRLink) Ended() bool {
	return pr.State == task.PRMerged || pr.State == task.PRClosed
}

func EmptyItem() Item {
	return Item{
		ID: "", Harness: "", Conversation: "", SessionPath: "", Resume: []string{}, Title: "", Directory: "",
		Project: "", Branch: "", Task: "", PRs: []PRLink{}, Server: "", Pane: "", PanePID: 0, CreatedAt: 0, TurnAt: 0,
		PromptAt: 0, PinnedAt: 0, FinishedAt: 0, FinishedBy: "", ReopenedAt: 0, UnseenAt: 0,
	}
}

func (s Store) Load(ctx context.Context) (State, error) {
	dir, name, err := s.open(false)
	if errors.Is(err, os.ErrNotExist) {
		return emptyState(), nil
	}

	if err != nil {
		return State{}, storeError("load_work", err)
	}

	state, err := read(dir, name)

	return state, storeError("load_work", errors.Join(err, dir.Close(), ctx.Err()))
}

func (s Store) Update(ctx context.Context, change func(*State) error) (State, error) {
	if change == nil {
		return State{}, storeError("update_work", ErrNilUpdate)
	}

	dir, name, err := s.open(true)
	if err != nil {
		return State{}, storeError("update_work", err)
	}

	lock, err := dir.Lock(ctx, name+".lock")
	if err != nil {
		return State{}, storeError("update_work", errors.Join(err, dir.Close()))
	}

	state, err := update(ctx, dir, name, change)

	cleanupErr := errors.Join(lock.Close(), dir.Close())
	if err == nil && cleanupErr != nil {
		err = errors.Join(ErrUncertain, cleanupErr)
	} else if err != nil {
		err = errors.Join(err, cleanupErr)
	}

	return state, storeError("update_work", err)
}

func (s Store) open(create bool) (*privatefs.Dir, string, error) {
	if !filepath.IsAbs(s.path) || filepath.Clean(s.path) != s.path {
		return nil, "", ErrPath
	}

	if create {
		dir, err := privatefs.Ensure(filepath.Dir(s.path))

		return dir, filepath.Base(s.path), err
	}

	dir, err := privatefs.Open(filepath.Dir(s.path), false)

	return dir, filepath.Base(s.path), err
}

func update(ctx context.Context, dir *privatefs.Dir, name string, change func(*State) error) (State, error) {
	state, err := read(dir, name)
	if err != nil {
		return State{}, err
	}

	if err := change(&state); err != nil {
		return State{}, err
	}

	state.Version = Version
	if err := state.validate(); err != nil {
		return State{}, err
	}

	if err := ctx.Err(); err != nil {
		return State{}, fmt.Errorf("work state update canceled: %w", err)
	}

	return state, write(dir, name, state)
}

func read(dir *privatefs.Dir, name string) (State, error) {
	data, err := dir.Read(name, maxFileBytes)
	if errors.Is(err, os.ErrNotExist) {
		return emptyState(), nil
	}

	if errors.Is(err, privatefs.ErrUnsafe) {
		return State{}, errors.Join(ErrTooBig, err)
	}

	if err != nil {
		return State{}, fmt.Errorf("read work state: %w", err)
	}

	state, err := strictjson.Decode[State](data)
	if err != nil {
		return State{}, errors.Join(ErrInvalid, err)
	}

	return state, state.validate()
}

func write(dir *privatefs.Dir, name string, state State) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode work state: %w", err)
	}

	if len(data) > maxFileBytes {
		return ErrTooBig
	}

	temp := fmt.Sprintf(".%s.tmp.%d.%d", name, os.Getpid(), time.Now().UnixNano())

	file, err := dir.File(temp, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return fmt.Errorf("create temporary work state: %w", err)
	}

	_, writeErr := file.Write(data)
	syncErr := file.Sync()

	if err := errors.Join(writeErr, syncErr, file.Close()); err != nil {
		return errors.Join(fmt.Errorf("write temporary work state: %w", err), dir.Remove(temp))
	}

	if err := dir.Rename(temp, dir, name); err != nil {
		return errors.Join(fmt.Errorf("publish work state: %w", err), dir.Remove(temp))
	}

	if err := dir.Sync(); err != nil {
		return errors.Join(ErrUncertain, err)
	}

	return nil
}

func (s *State) validate() error {
	if s.Version != Version {
		return ErrVersion
	}

	if s.LastID < 0 {
		return ErrInvalid
	}

	ids := make(map[string]bool, len(s.Items))

	for _, item := range s.Items {
		if item.ID == "" || ids[item.ID] || item.Conversation == "" || len(item.Resume) == 0 || !filepath.IsAbs(item.Directory) ||
			!item.validLifecycle() {
			return ErrInvalid
		}

		ids[item.ID] = true
	}

	return nil
}

func (i Item) validLifecycle() bool {
	switch i.FinishedBy {
	case "":
		if i.FinishedAt != 0 {
			return false
		}
	case FinishedByPR, FinishedByUser:
		if i.FinishedAt == 0 {
			return false
		}
	default:
		return false
	}

	numbers := make(map[int]bool, len(i.PRs))

	for _, pr := range i.PRs {
		if pr.Number <= 0 || numbers[pr.Number] || (pr.Source != LinkBranch && pr.Source != LinkUser && pr.Source != LinkDismissed) {
			return false
		}

		numbers[pr.Number] = true
	}

	return true
}

func emptyState() State {
	return State{Version: Version, LastID: 0, Items: []Item{}}
}

func storeError(operation string, err error) error {
	if err == nil {
		return nil
	}

	return oops.In("work").Code("work_state_failed").With("operation", operation).Wrap(err)
}
