//go:build linux || darwin

package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/samber/oops"
	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/strictjson"
)

const CurrentHistoryVersion = 1

const (
	maxHistoryFileSize     = 1 << 20
	maxSwitchHistory       = 1024
	maxRemoteSwitchHistory = 1024
	maxAgentViewHistory    = 512
)

var (
	ErrEmptyStatePath        = errors.New("history path is empty")
	ErrNilStateUpdate        = errors.New("state update is nil")
	ErrNilState              = errors.New("state is nil")
	ErrInvalidHistoryVersion = errors.New("invalid history version")
	ErrHistoryOversized      = errors.New("history file oversized")
	ErrUncertain             = errors.New("history publication durability uncertain")
	ErrDuplicateHistoryEntry = errors.New("duplicate history entry")
	ErrHistoryBoundExceeded  = errors.New("history bound exceeded")
)

type SwitchHistoryEntry struct {
	Session    Identity `json:"session"`
	LastSwitch int64    `json:"lastSwitch"`
}

type RemoteSwitchHistoryEntry struct {
	Session    Identity `json:"session"`
	LastSwitch int64    `json:"lastSwitch"`
}

type AgentViewHistoryEntry struct {
	Session  Identity `json:"session"`
	PaneID   string   `json:"paneId"`
	ViewedAt int64    `json:"viewedAt"`
}

type State struct {
	Version               int                        `json:"version"`
	SwitchHistory         []SwitchHistoryEntry       `json:"switchHistory"`
	RemoteSwitchHistory   []RemoteSwitchHistoryEntry `json:"remoteSwitchHistory"`
	AgentViewHistory      []AgentViewHistoryEntry    `json:"agentViewHistory"`
	LastAutosaveTimestamp int64                      `json:"lastAutosaveTimestamp"`
	PopupDisplayMode      string                     `json:"popupDisplayMode"`
}

type ServiceDeps struct {
	FaultHook func(phase string) error
}

type Service struct {
	faultHook func(phase string) error
}

type StateUpdate func(*State)

func NewService(deps ServiceDeps) *Service {
	return &Service{
		faultHook: deps.FaultHook,
	}
}

func (s *State) Validate() error {
	if s == nil {
		return ErrNilState
	}

	if s.Version != CurrentHistoryVersion {
		return ErrInvalidHistoryVersion
	}

	if len(s.SwitchHistory) > maxSwitchHistory || len(s.RemoteSwitchHistory) > maxRemoteSwitchHistory {
		return errors.Join(strictjson.ErrInvalid, ErrHistoryBoundExceeded)
	}

	if len(s.AgentViewHistory) > maxAgentViewHistory {
		return errors.Join(strictjson.ErrInvalid, ErrHistoryBoundExceeded)
	}

	if s.LastAutosaveTimestamp < 0 || !utf8.ValidString(s.PopupDisplayMode) {
		return strictjson.ErrInvalid
	}

	if err := validateSwitchHistory(s.SwitchHistory); err != nil {
		return err
	}

	if err := validateRemoteSwitchHistory(s.RemoteSwitchHistory); err != nil {
		return err
	}

	return validateAgentViewHistory(s.AgentViewHistory)
}

func LoadState(path string) (*State, error) {
	return NewService(ServiceDeps{FaultHook: nil}).LoadState(path)
}

func (s *Service) LoadState(path string) (*State, error) {
	if path == "" {
		return nil, oops.In("session").Code("history_failed").With("operation", "load_history").Wrap(ErrEmptyStatePath)
	}

	dirPath, fileName, err := validatePath(path)
	if err != nil {
		return nil, oops.In("session").Code("history_failed").With("operation", "load_history").Wrap(err)
	}

	dir, err := openHistoryDir(dirPath, false)
	if err != nil {
		if isPureNotExist(err) {
			return defaultState(), nil
		}

		return nil, oops.In("session").Code("history_failed").With("operation", "load_history").Wrap(err)
	}

	state, loadErr := s.loadStateFromDir(dir, fileName)

	closeErr := dir.Close()
	if closeErr != nil {
		return nil, oops.In("session").Code("history_failed").With("operation", "load_history").Wrap(errors.Join(loadErr, closeErr))
	}

	if loadErr != nil {
		return nil, oops.In("session").Code("history_failed").With("operation", "load_history").Wrap(loadErr)
	}

	return state, nil
}

func SaveState(ctx context.Context, path string, current *State) error {
	return NewService(ServiceDeps{FaultHook: nil}).SaveState(ctx, path, current)
}

func (s *Service) SaveState(ctx context.Context, path string, current *State) error {
	if err := ctx.Err(); err != nil {
		return oops.In("session").Code("history_failed").With("operation", "save_history").Wrap(err)
	}

	if path == "" {
		return oops.In("session").Code("history_failed").With("operation", "save_history").Wrap(ErrEmptyStatePath)
	}

	if current == nil {
		return oops.In("session").Code("history_failed").With("operation", "save_history").Wrap(ErrNilState)
	}

	if err := current.Validate(); err != nil {
		return oops.In("session").Code("history_failed").With("operation", "save_history").Wrap(err)
	}

	dirPath, fileName, err := validatePath(path)
	if err != nil {
		return oops.In("session").Code("history_failed").With("operation", "save_history").Wrap(err)
	}

	dir, err := openHistoryDir(dirPath, true)
	if err != nil {
		return oops.In("session").Code("history_failed").With("operation", "save_history").Wrap(err)
	}

	lockName := fileName + ".lock"
	lockFile, err := dir.Lock(ctx, lockName)
	if err != nil {
		closeErr := dir.Close()
		return oops.In("session").Code("history_failed").With("operation", "save_history").Wrap(errors.Join(err, closeErr))
	}

	if err := ctx.Err(); err != nil {
		cleanupErr := errors.Join(lockFile.Close(), dir.Close())
		return oops.In("session").Code("history_failed").With("operation", "save_history").Wrap(errors.Join(err, cleanupErr))
	}

	_, saveErr := s.loadStateFromDir(dir, fileName)
	if saveErr == nil {
		saveErr = s.saveStateToDir(ctx, dir, fileName, current)
	}

	lockCloseErr := lockFile.Close()
	dirCloseErr := dir.Close()

	cleanupErr := errors.Join(lockCloseErr, dirCloseErr)
	if saveErr != nil {
		return oops.In("session").Code("history_failed").With("operation", "save_history").Wrap(errors.Join(saveErr, cleanupErr))
	}

	if cleanupErr != nil {
		return oops.In("session").Code("history_failed").With("operation", "save_history").Wrap(errors.Join(ErrUncertain, cleanupErr))
	}

	return nil
}

func UpdateState(ctx context.Context, path string, update StateUpdate) (*State, error) {
	return NewService(ServiceDeps{FaultHook: nil}).UpdateState(ctx, path, update)
}

func (s *Service) UpdateState(
	ctx context.Context,
	path string,
	update StateUpdate,
) (*State, error) {
	if err := ctx.Err(); err != nil {
		return nil, oops.In("session").Code("history_failed").With("operation", "update_history").Wrap(err)
	}

	if path == "" {
		return nil, oops.In("session").Code("history_failed").With("operation", "update_history").Wrap(ErrEmptyStatePath)
	}

	if update == nil {
		return nil, oops.In("session").Code("history_failed").With("operation", "update_history").Wrap(ErrNilStateUpdate)
	}

	dirPath, fileName, err := validatePath(path)
	if err != nil {
		return nil, oops.In("session").Code("history_failed").With("operation", "update_history").Wrap(err)
	}

	dir, err := openHistoryDir(dirPath, true)
	if err != nil {
		return nil, oops.In("session").Code("history_failed").With("operation", "update_history").Wrap(err)
	}

	lockName := fileName + ".lock"
	lockFile, err := dir.Lock(ctx, lockName)
	if err != nil {
		closeErr := dir.Close()
		return nil, oops.In("session").Code("history_failed").With("operation", "update_history").Wrap(errors.Join(err, closeErr))
	}

	if err := ctx.Err(); err != nil {
		cleanupErr := errors.Join(lockFile.Close(), dir.Close())
		return nil, oops.In("session").Code("history_failed").With("operation", "update_history").Wrap(errors.Join(err, cleanupErr))
	}

	mutated, updateErr := s.executeStateUpdate(ctx, dir, fileName, update)
	lockCloseErr := lockFile.Close()
	dirCloseErr := dir.Close()

	cleanupErr := errors.Join(lockCloseErr, dirCloseErr)
	if updateErr != nil {
		return nil, oops.In("session").Code("history_failed").With("operation", "update_history").Wrap(errors.Join(updateErr, cleanupErr))
	}

	if cleanupErr != nil {
		return nil, oops.In("session").Code("history_failed").With("operation", "update_history").Wrap(errors.Join(ErrUncertain, cleanupErr))
	}

	return mutated, nil
}

func (s *Service) executeStateUpdate(
	ctx context.Context,
	dir *privatefs.Dir,
	fileName string,
	update StateUpdate,
) (*State, error) {
	current, loadErr := s.loadStateFromDir(dir, fileName)
	if loadErr != nil {
		return nil, loadErr
	}

	mutated := cloneState(current)
	update(mutated)
	mutated.Version = CurrentHistoryVersion

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("history update canceled: %w", err)
	}

	if err := mutated.Validate(); err != nil {
		return nil, err
	}

	if err := s.saveStateToDir(ctx, dir, fileName, mutated); err != nil {
		return nil, err
	}

	return mutated, nil
}

func (s *Service) loadStateFromDir(dir *privatefs.Dir, fileName string) (*State, error) {
	data, err := dir.Read(fileName, maxHistoryFileSize)
	if err != nil {
		if isPureNotExist(err) {
			return defaultState(), nil
		}

		if errors.Is(err, privatefs.ErrUnsafe) {
			return nil, errors.Join(ErrHistoryOversized, err)
		}

		return nil, fmt.Errorf("read history file: %w", err)
	}

	state, err := strictjson.Decode[State](data)
	if err != nil {
		return nil, errors.Join(strictjson.ErrInvalid, err)
	}

	if err := state.Validate(); err != nil {
		return nil, err
	}

	return &state, nil
}

func encodeState(current *State) ([]byte, error) {
	toWrite := State{
		Version:               CurrentHistoryVersion,
		SwitchHistory:         cloneSwitchHistory(current.SwitchHistory),
		RemoteSwitchHistory:   cloneRemoteSwitchHistory(current.RemoteSwitchHistory),
		AgentViewHistory:      cloneAgentViewHistory(current.AgentViewHistory),
		LastAutosaveTimestamp: current.LastAutosaveTimestamp,
		PopupDisplayMode:      current.PopupDisplayMode,
	}

	if toWrite.SwitchHistory == nil {
		toWrite.SwitchHistory = []SwitchHistoryEntry{}
	}

	if toWrite.RemoteSwitchHistory == nil {
		toWrite.RemoteSwitchHistory = []RemoteSwitchHistoryEntry{}
	}

	if toWrite.AgentViewHistory == nil {
		toWrite.AgentViewHistory = []AgentViewHistoryEntry{}
	}

	data, err := json.MarshalIndent(toWrite, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding state: %w", err)
	}

	if int64(len(data)) > maxHistoryFileSize {
		return nil, ErrHistoryOversized
	}

	return data, nil
}

func (s *Service) saveStateToDir(ctx context.Context, dir *privatefs.Dir, fileName string, current *State) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context check: %w", err)
	}

	data, err := encodeState(current)
	if err != nil {
		return err
	}

	tempName := fmt.Sprintf(".%s.tmp.%d.%d", fileName, os.Getpid(), time.Now().UnixNano())
	if err := s.writeTempState(dir, tempName, data); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		removeErr := dir.Remove(tempName)
		return errors.Join(fmt.Errorf("context canceled: %w", err), removeErr)
	}

	return s.publishTempState(ctx, dir, tempName, fileName)
}

func (s *Service) publishTempState(ctx context.Context, dir *privatefs.Dir, tempName, fileName string) error {
	if s.faultHook != nil {
		if fault := s.faultHook("rename"); fault != nil {
			removeErr := dir.Remove(tempName)
			return errors.Join(fault, removeErr)
		}
	}

	if err := ctx.Err(); err != nil {
		return errors.Join(err, dir.Remove(tempName))
	}

	if err := dir.Rename(tempName, dir, fileName); err != nil {
		removeErr := dir.Remove(tempName)
		return errors.Join(fmt.Errorf("rename history file: %w", err), removeErr)
	}

	if s.faultHook != nil {
		if fault := s.faultHook("sync_dir"); fault != nil {
			return errors.Join(ErrUncertain, fault)
		}
	}

	if err := dir.Sync(); err != nil {
		return errors.Join(ErrUncertain, err)
	}

	return nil
}

func (s *Service) writeTempState(dir *privatefs.Dir, tempName string, data []byte) error {
	tempFile, err := dir.File(tempName, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return fmt.Errorf("create temp history file: %w", err)
	}

	cleanup := func() error {
		closeErr := tempFile.Close()
		removeErr := dir.Remove(tempName)

		return errors.Join(closeErr, removeErr)
	}

	if s.faultHook != nil {
		if fault := s.faultHook("write"); fault != nil {
			return errors.Join(fault, cleanup())
		}
	}

	if _, err := tempFile.Write(data); err != nil {
		return errors.Join(fmt.Errorf("write temp history file: %w", err), cleanup())
	}

	if s.faultHook != nil {
		if fault := s.faultHook("sync_file"); fault != nil {
			return errors.Join(fault, cleanup())
		}
	}

	if err := tempFile.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync temp history file: %w", err), cleanup())
	}

	if s.faultHook != nil {
		if fault := s.faultHook("close_file"); fault != nil {
			return errors.Join(fault, cleanup())
		}
	}

	if err := tempFile.Close(); err != nil {
		removeErr := dir.Remove(tempName)
		return errors.Join(fmt.Errorf("close temp history file: %w", err), removeErr)
	}

	return nil
}

func validatePath(path string) (string, string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", "", privatefs.ErrUnsafe
	}

	dirPath := filepath.Dir(path)
	fileName := filepath.Base(path)

	if fileName == "." || fileName == ".." || fileName == "" {
		return "", "", privatefs.ErrUnsafe
	}

	return dirPath, fileName, nil
}

func defaultState() *State {
	return &State{
		Version:               CurrentHistoryVersion,
		SwitchHistory:         []SwitchHistoryEntry{},
		RemoteSwitchHistory:   []RemoteSwitchHistoryEntry{},
		AgentViewHistory:      []AgentViewHistoryEntry{},
		LastAutosaveTimestamp: 0,
		PopupDisplayMode:      "",
	}
}

func isPureNotExist(err error) bool {
	return onlySessionError(err, os.ErrNotExist)
}

func onlySessionError(err, target error) bool {
	if err == nil {
		return false
	}

	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}

		for _, cause := range causes {
			if !onlySessionError(cause, target) {
				return false
			}
		}

		return true
	}

	if cause := errors.Unwrap(err); cause != nil {
		return onlySessionError(cause, target)
	}

	return errors.Is(err, target)
}

func openHistoryDir(dirPath string, create bool) (*privatefs.Dir, error) {
	if !filepath.IsAbs(dirPath) || filepath.Clean(dirPath) != dirPath || dirPath == "/" {
		return nil, privatefs.ErrUnsafe
	}

	if create {
		d, err := privatefs.Ensure(dirPath)
		if err != nil {
			return nil, fmt.Errorf("ensure private history dir: %w", err)
		}

		return d, nil
	}

	d, err := privatefs.Open(dirPath, false)
	if err != nil {
		return nil, fmt.Errorf("open private history dir: %w", err)
	}

	return d, nil
}

func (s *State) RecordSwitch(ident Identity) {
	s.RecordSwitchIdentity(ident)
}

func (s *State) RecordSwitchIdentity(ident Identity) {
	if ident.Name == "" {
		return
	}

	now := time.Now().Unix()

	for index := range s.SwitchHistory {
		if s.SwitchHistory[index].Session == ident {
			s.SwitchHistory[index].LastSwitch = now
			return
		}
	}

	s.SwitchHistory = append(s.SwitchHistory, SwitchHistoryEntry{
		Session:    ident,
		LastSwitch: now,
	})

	if len(s.SwitchHistory) > maxSwitchHistory {
		oldest := 0
		for index := 1; index < len(s.SwitchHistory); index++ {
			if s.SwitchHistory[index].LastSwitch < s.SwitchHistory[oldest].LastSwitch {
				oldest = index
			}
		}

		s.SwitchHistory = slices.Delete(s.SwitchHistory, oldest, oldest+1)
	}
}

func (s *State) GetLastSwitch(ident Identity) int64 {
	return s.GetLastSwitchIdentity(ident)
}

func (s *State) GetLastSwitchIdentity(ident Identity) int64 {
	if ident.Name == "" {
		return 0
	}

	for _, entry := range s.SwitchHistory {
		if entry.Session == ident {
			return entry.LastSwitch
		}
	}

	for _, entry := range s.RemoteSwitchHistory {
		if entry.Session == ident {
			return entry.LastSwitch
		}
	}

	return 0
}

func (s *State) RecordRemoteSwitch(ident Identity) {
	if ident.Name == "" {
		return
	}

	now := time.Now().Unix()

	for index := range s.RemoteSwitchHistory {
		if s.RemoteSwitchHistory[index].Session == ident {
			s.RemoteSwitchHistory[index].LastSwitch = now
			s.RecordSwitchIdentity(ident)

			return
		}
	}

	s.RemoteSwitchHistory = append(s.RemoteSwitchHistory, RemoteSwitchHistoryEntry{
		Session:    ident,
		LastSwitch: now,
	})

	if len(s.RemoteSwitchHistory) > maxRemoteSwitchHistory {
		oldest := 0
		for index := 1; index < len(s.RemoteSwitchHistory); index++ {
			if s.RemoteSwitchHistory[index].LastSwitch < s.RemoteSwitchHistory[oldest].LastSwitch {
				oldest = index
			}
		}

		s.RemoteSwitchHistory = slices.Delete(s.RemoteSwitchHistory, oldest, oldest+1)
	}

	s.RecordSwitchIdentity(ident)
}

func (s *State) GetLastRemoteSwitch(ident Identity) int64 {
	if ident.Name == "" {
		return 0
	}

	for _, entry := range s.RemoteSwitchHistory {
		if entry.Session == ident {
			return entry.LastSwitch
		}
	}

	return 0
}

func (s *State) RecordAgentView(ident Identity, paneID string) {
	s.RecordAgentViewAt(ident, paneID, time.Now())
}

func (s *State) RecordAgentViewAt(ident Identity, paneID string, viewedAt time.Time) {
	if ident.Name == "" || paneID == "" || viewedAt.IsZero() {
		return
	}

	timestamp := viewedAt.UnixNano()

	for index := range s.AgentViewHistory {
		entry := &s.AgentViewHistory[index]
		if entry.Session == ident && entry.PaneID == paneID {
			if timestamp > entry.ViewedAt {
				entry.ViewedAt = timestamp
			}

			return
		}
	}

	s.AgentViewHistory = append(s.AgentViewHistory, AgentViewHistoryEntry{
		Session:  ident,
		PaneID:   paneID,
		ViewedAt: timestamp,
	})

	if len(s.AgentViewHistory) > maxAgentViewHistory {
		oldest := 0
		for index := 1; index < len(s.AgentViewHistory); index++ {
			if s.AgentViewHistory[index].ViewedAt < s.AgentViewHistory[oldest].ViewedAt {
				oldest = index
			}
		}

		s.AgentViewHistory = slices.Delete(s.AgentViewHistory, oldest, oldest+1)
	}
}

func (s *State) GetLastAgentView(ident Identity, paneID string) int64 {
	if ident.Name == "" || paneID == "" {
		return 0
	}

	for _, entry := range s.AgentViewHistory {
		if entry.Session == ident && entry.PaneID == paneID {
			return entry.ViewedAt
		}
	}

	return 0
}

func (s *State) LastUsedSession() string {
	var (
		selected  string
		timestamp int64
	)

	for _, entry := range s.SwitchHistory {
		if entry.LastSwitch > timestamp {
			selected = entry.Session.Name
			timestamp = entry.LastSwitch
		}
	}

	return selected
}

func (s *State) DeleteSession(ident Identity) {
	s.DeleteSessionIdentity(ident)
}

func (s *State) DeleteSessionIdentity(ident Identity) {
	if ident.Name == "" {
		return
	}

	s.SwitchHistory = slices.DeleteFunc(s.SwitchHistory, func(entry SwitchHistoryEntry) bool {
		return entry.Session == ident
	})

	s.RemoteSwitchHistory = slices.DeleteFunc(s.RemoteSwitchHistory, func(entry RemoteSwitchHistoryEntry) bool {
		return entry.Session == ident
	})

	s.AgentViewHistory = slices.DeleteFunc(s.AgentViewHistory, func(entry AgentViewHistoryEntry) bool {
		return entry.Session == ident
	})
}

func (s *State) RenameSession(oldIdent Identity, newIdent Identity) {
	s.RenameSessionIdentity(oldIdent, newIdent)
}

func (s *State) RenameSessionIdentity(oldIdent Identity, newIdent Identity) {
	if oldIdent.Name == "" || newIdent.Name == "" || oldIdent == newIdent {
		return
	}

	for index := range s.SwitchHistory {
		entry := &s.SwitchHistory[index]
		if entry.Session == oldIdent {
			entry.Session = newIdent
		}
	}

	for index := range s.RemoteSwitchHistory {
		entry := &s.RemoteSwitchHistory[index]
		if entry.Session == oldIdent {
			entry.Session = newIdent
		}
	}

	for index := range s.AgentViewHistory {
		entry := &s.AgentViewHistory[index]
		if entry.Session == oldIdent {
			entry.Session = newIdent
		}
	}
}

func cloneState(s *State) *State {
	if s == nil {
		return nil
	}

	return &State{
		Version:               s.Version,
		SwitchHistory:         cloneSwitchHistory(s.SwitchHistory),
		RemoteSwitchHistory:   cloneRemoteSwitchHistory(s.RemoteSwitchHistory),
		AgentViewHistory:      cloneAgentViewHistory(s.AgentViewHistory),
		LastAutosaveTimestamp: s.LastAutosaveTimestamp,
		PopupDisplayMode:      s.PopupDisplayMode,
	}
}

func cloneSwitchHistory(in []SwitchHistoryEntry) []SwitchHistoryEntry {
	if in == nil {
		return nil
	}

	out := make([]SwitchHistoryEntry, len(in))
	copy(out, in)

	return out
}

func cloneRemoteSwitchHistory(in []RemoteSwitchHistoryEntry) []RemoteSwitchHistoryEntry {
	if in == nil {
		return nil
	}

	out := make([]RemoteSwitchHistoryEntry, len(in))
	copy(out, in)

	return out
}

func cloneAgentViewHistory(in []AgentViewHistoryEntry) []AgentViewHistoryEntry {
	if in == nil {
		return nil
	}

	out := make([]AgentViewHistoryEntry, len(in))
	copy(out, in)

	return out
}

func validateSwitchHistory(entries []SwitchHistoryEntry) error {
	seenSwitch := make(map[Identity]struct{}, len(entries))

	for _, entry := range entries {
		if err := entry.Session.Validate(); err != nil {
			return errors.Join(strictjson.ErrInvalid, err)
		}

		if entry.LastSwitch < 0 {
			return strictjson.ErrInvalid
		}

		if _, exists := seenSwitch[entry.Session]; exists {
			return errors.Join(strictjson.ErrInvalid, ErrDuplicateHistoryEntry)
		}

		seenSwitch[entry.Session] = struct{}{}
	}

	return nil
}

func validateRemoteSwitchHistory(entries []RemoteSwitchHistoryEntry) error {
	seenRemote := make(map[Identity]struct{}, len(entries))

	for _, entry := range entries {
		if err := entry.Session.Validate(); err != nil {
			return errors.Join(strictjson.ErrInvalid, err)
		}

		if entry.LastSwitch < 0 {
			return strictjson.ErrInvalid
		}

		if _, exists := seenRemote[entry.Session]; exists {
			return errors.Join(strictjson.ErrInvalid, ErrDuplicateHistoryEntry)
		}

		seenRemote[entry.Session] = struct{}{}
	}

	return nil
}

func validateAgentViewHistory(entries []AgentViewHistoryEntry) error {
	type agentKey struct {
		session Identity
		paneID  string
	}

	seenAgent := make(map[agentKey]struct{}, len(entries))

	for _, entry := range entries {
		if err := entry.Session.Validate(); err != nil {
			return errors.Join(strictjson.ErrInvalid, err)
		}

		if entry.PaneID == "" || !utf8.ValidString(entry.PaneID) || entry.ViewedAt < 0 {
			return strictjson.ErrInvalid
		}

		k := agentKey{session: entry.Session, paneID: entry.PaneID}
		if _, exists := seenAgent[k]; exists {
			return errors.Join(strictjson.ErrInvalid, ErrDuplicateHistoryEntry)
		}

		seenAgent[k] = struct{}{}
	}

	return nil
}
