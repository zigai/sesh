// Package inbox keeps what the inbox remembers between pickers: the rows the user snoozed or
// archived and the results the user marked as not seen.
package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/samber/oops"
	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/strictjson"
)

const (
	Version = 1

	maxFileBytes = 1 << 20
	maxEntries   = 4096
)

var (
	ErrPath      = errors.New("inbox state path must be absolute and clean")
	ErrVersion   = errors.New("unsupported inbox state version")
	ErrInvalid   = errors.New("inbox state is inconsistent")
	ErrTooBig    = errors.New("inbox state exceeds its size limit")
	ErrUncertain = errors.New("inbox state publication durability uncertain")
	ErrNilUpdate = errors.New("inbox state update is nil")
)

// Snooze hides the conversation Key from the inbox. It ends at Until, or when the agent finishes a
// turn or asks for input after Since; an Until of zero waits only for that.
type Snooze struct {
	Key   string `json:"key"`
	Until int64  `json:"until"`
	Since int64  `json:"since"`
}

// Unseen marks the latest result of the conversation Key as not seen, until its pane is viewed
// after At.
type Unseen struct {
	Key string `json:"key"`
	At  int64  `json:"at"`
}

// Archive hides the closed conversation Key from the inbox while its latest turn is not after
// TurnAt, in Unix seconds.
type Archive struct {
	Key    string `json:"key"`
	TurnAt int64  `json:"turnAt"`
}

type State struct {
	Version  int       `json:"version"`
	Snoozes  []Snooze  `json:"snoozes"`
	Unseen   []Unseen  `json:"unseen"`
	Archived []Archive `json:"archived"`
}

// Store reads and updates the inbox state file. Updates hold an exclusive lock, so concurrent sesh
// processes never lose each other's changes.
type Store struct {
	path string
}

func NewStore(path string) Store {
	return Store{path: path}
}

// Find returns the snooze of key.
func (s *State) Find(key string) (Snooze, bool) {
	for _, snooze := range s.Snoozes {
		if snooze.Key == key {
			return snooze, true
		}
	}

	return Snooze{Key: "", Until: 0, Since: 0}, false
}

// Put stores snooze, replacing an earlier snooze of the same key.
func (s *State) Put(snooze Snooze) {
	s.Remove(snooze.Key)
	s.Snoozes = append(s.Snoozes, snooze)
}

// Remove ends the snooze of key.
func (s *State) Remove(key string) {
	s.Snoozes = slices.DeleteFunc(s.Snoozes, func(snooze Snooze) bool { return snooze.Key == key })
}

// FindUnseen returns the unseen mark of key.
func (s *State) FindUnseen(key string) (Unseen, bool) {
	for _, mark := range s.Unseen {
		if mark.Key == key {
			return mark, true
		}
	}

	return Unseen{Key: "", At: 0}, false
}

// MarkUnseen marks the latest result of key as not seen since at.
func (s *State) MarkUnseen(key string, at time.Time) {
	s.ClearUnseen(key)
	s.Unseen = append(s.Unseen, Unseen{Key: key, At: at.UnixNano()})
}

// ClearUnseen removes the unseen mark of key.
func (s *State) ClearUnseen(key string) {
	s.Unseen = slices.DeleteFunc(s.Unseen, func(mark Unseen) bool { return mark.Key == key })
}

// Archive hides key until a turn after turnAt.
func (s *State) Archive(key string, turnAt int64) {
	s.Archived = slices.DeleteFunc(s.Archived, func(archive Archive) bool { return archive.Key == key })
	s.Archived = append(s.Archived, Archive{Key: key, TurnAt: turnAt})
}

// IsArchived reports whether key is archived and has had no turn after turnAt since.
func (s *State) IsArchived(key string, turnAt int64) bool {
	for _, archive := range s.Archived {
		if archive.Key == key {
			return turnAt <= archive.TurnAt
		}
	}

	return false
}

// Expire drops snoozes whose time has passed.
func (s *State) Expire(now time.Time) {
	s.Snoozes = slices.DeleteFunc(s.Snoozes, func(snooze Snooze) bool { return snooze.Until != 0 && snooze.Until <= now.Unix() })
}

// Load returns the current state; a missing file is an empty state.
func (s Store) Load(ctx context.Context) (State, error) {
	dir, name, err := s.open(false)
	if errors.Is(err, os.ErrNotExist) {
		return emptyState(), nil
	}

	if err != nil {
		return State{}, storeError("load_inbox", err)
	}

	state, err := read(dir, name)

	return state, storeError("load_inbox", errors.Join(err, dir.Close(), ctx.Err()))
}

// Update applies change to the locked state and publishes the result. A change that returns an
// error leaves the file as it was.
func (s Store) Update(ctx context.Context, change func(*State) error) (State, error) {
	if change == nil {
		return State{}, storeError("update_inbox", ErrNilUpdate)
	}

	dir, name, err := s.open(true)
	if err != nil {
		return State{}, storeError("update_inbox", err)
	}

	lock, err := dir.Lock(ctx, name+".lock")
	if err != nil {
		return State{}, storeError("update_inbox", errors.Join(err, dir.Close()))
	}

	state, err := update(ctx, dir, name, change)

	cleanupErr := errors.Join(lock.Close(), dir.Close())
	if err == nil && cleanupErr != nil {
		err = errors.Join(ErrUncertain, cleanupErr)
	} else if err != nil {
		err = errors.Join(err, cleanupErr)
	}

	return state, storeError("update_inbox", err)
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
		return State{}, fmt.Errorf("inbox state update canceled: %w", err)
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
		return State{}, fmt.Errorf("read inbox state: %w", err)
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
		return fmt.Errorf("encode inbox state: %w", err)
	}

	if len(data) > maxFileBytes {
		return ErrTooBig
	}

	temp := fmt.Sprintf(".%s.tmp.%d.%d", name, os.Getpid(), time.Now().UnixNano())

	file, err := dir.File(temp, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return fmt.Errorf("create temporary inbox state: %w", err)
	}

	_, writeErr := file.Write(data)
	syncErr := file.Sync()

	if err := errors.Join(writeErr, syncErr, file.Close()); err != nil {
		return errors.Join(fmt.Errorf("write temporary inbox state: %w", err), dir.Remove(temp))
	}

	if err := dir.Rename(temp, dir, name); err != nil {
		return errors.Join(fmt.Errorf("publish inbox state: %w", err), dir.Remove(temp))
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

	if len(s.Snoozes) > maxEntries || len(s.Unseen) > maxEntries || len(s.Archived) > maxEntries ||
		!s.validSnoozes() || !s.validUnseen() || !s.validArchived() {
		return ErrInvalid
	}

	return nil
}

func (s *State) validSnoozes() bool {
	keys := make(map[string]bool, len(s.Snoozes))

	for _, snooze := range s.Snoozes {
		if snooze.Key == "" || keys[snooze.Key] || snooze.Until < 0 || snooze.Since <= 0 {
			return false
		}

		keys[snooze.Key] = true
	}

	return true
}

func (s *State) validUnseen() bool {
	keys := make(map[string]bool, len(s.Unseen))

	for _, mark := range s.Unseen {
		if mark.Key == "" || keys[mark.Key] || mark.At <= 0 {
			return false
		}

		keys[mark.Key] = true
	}

	return true
}

func (s *State) validArchived() bool {
	keys := make(map[string]bool, len(s.Archived))

	for _, archive := range s.Archived {
		if archive.Key == "" || keys[archive.Key] || archive.TurnAt < 0 {
			return false
		}

		keys[archive.Key] = true
	}

	return true
}

func emptyState() State {
	return State{Version: Version, Snoozes: []Snooze{}, Unseen: []Unseen{}, Archived: []Archive{}}
}

func storeError(operation string, err error) error {
	if err == nil {
		return nil
	}

	return oops.In("inbox").Code("inbox_state_failed").With("operation", operation).Wrap(err)
}
