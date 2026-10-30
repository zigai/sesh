//go:build linux || darwin

package session

import (
	"context"
	"errors"
	"os"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/integration/tmuxserver"
	"github.com/zigai/sesh/internal/logging"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const (
	defaultHookSource = "cli"
)

const (
	EnterNone EnterAction = iota
	EnterSwitch
	EnterAttach
)

var (
	ErrSessionAlreadyExists      = errors.New("session already exists")
	ErrSessionNotFound           = errors.New("session not found")
	ErrSessionAttached           = errors.New("session is attached")
	ErrInvalidSessionName        = errors.New("invalid session name")
	ErrNotInsideTmux             = errors.New("not running inside tmux")
	ErrInvokingClientUnavailable = errors.New("no attached client displays the invoking session")
	ErrInvokingClientAmbiguous   = errors.New("multiple attached clients display the invoking session")
	ErrSwitchUnverified          = errors.New("client switch could not be verified")
	ErrTerminalRequired          = errors.New("attach requires an interactive terminal")
	ErrSavedGenerationsProtected = errors.New("saved generations containing the session are protected")
	ErrSavedSessionScopeConflict = errors.New("saved session endpoint scope is missing")
	ErrSessionReplaced           = errors.New("session identity changed or could not be verified during deletion")
	errServerRequired            = errors.New("tmux server required")
	errRepositoryRequired        = errors.New("snapshot repository required")
)

type WorkflowOptions struct {
	Server      *gotmux.Server
	Repository  *store.Repository
	LeaseRoot   string
	HistoryPath string
	Hooks       config.HookConfig
	HookRunner  *HookRunner
	Getenv      func(string) string
	Streams     gotmux.Streams
	Source      string
	// DeferSwitchHistory leaves recording a completed switch to the caller, which records
	// SwitchHistoryIdentity after its own follow-up work.
	DeferSwitchHistory bool
}

type Workflow struct {
	server             *gotmux.Server
	repo               *store.Repository
	leaseRoot          string
	historyPath        string
	hooks              config.HookConfig
	hookRunner         *HookRunner
	getenv             func(string) string
	streams            gotmux.Streams
	source             string
	deferSwitchHistory bool
}

type EnterAction uint8

type HookSummary struct {
	Attempted int
	Completed int
	Failed    int
	TimedOut  int
}

type EnterOutcome struct {
	SessionName string
	SessionID   string
	Action      EnterAction
	Created     bool
	Started     bool
	Client      string
	Hooks       HookSummary
}

type DeleteOutcome struct {
	LivePresent          bool
	LiveKilled           bool
	LiveSessionID        string
	SavedFound           bool
	SavedDeleted         bool
	SavedGenerations     int
	SavedSessions        int
	PreservedGenerations []string
	PendingRewrites      []string
	HistoryUpdated       bool
}

type RenameOutcome struct {
	PreviousName   string
	Name           string
	Renamed        bool
	HistoryUpdated bool
}

type loggedOutcomeError struct{ err error }

func (e loggedOutcomeError) Error() string       { return e.err.Error() }
func (e loggedOutcomeError) Unwrap() error       { return e.err }
func (e loggedOutcomeError) OutcomeLogged() bool { return true }

func NewWorkflow(server *gotmux.Server, repo *store.Repository, historyPath string) *Workflow {
	return NewWorkflowWithOptions(WorkflowOptions{
		Server:      server,
		Repository:  repo,
		LeaseRoot:   "",
		HistoryPath: historyPath,
		Hooks: config.HookConfig{
			Timeout:           "",
			SessionSwitchPre:  nil,
			SessionSwitchPost: nil,
			SessionAttachPre:  nil,
			SessionAttachPost: nil,
			TmuxStartupPost:   nil,
		},
		HookRunner:         nil,
		Getenv:             nil,
		Streams:            gotmux.Streams{In: nil, Out: nil, Err: nil},
		Source:             "",
		DeferSwitchHistory: false,
	})
}

func NewWorkflowWithOptions(opts WorkflowOptions) *Workflow {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}

	runner := opts.HookRunner
	if runner == nil {
		runner = NewHookRunner(HookRunnerDeps{Now: nil, Getenv: nil})
	}

	source := opts.Source
	if source == "" {
		source = defaultHookSource
	}

	return &Workflow{
		server:             opts.Server,
		repo:               opts.Repository,
		leaseRoot:          opts.LeaseRoot,
		historyPath:        opts.HistoryPath,
		hooks:              opts.Hooks,
		hookRunner:         runner,
		getenv:             getenv,
		streams:            opts.Streams,
		source:             source,
		deferSwitchHistory: opts.DeferSwitchHistory,
	}
}

func (w *Workflow) SwitchHistoryIdentity(name string) (Identity, bool) {
	return w.localIdentity(name)
}

func (w *Workflow) SetRepository(repo *store.Repository) {
	if w != nil {
		w.repo = repo
	}
}

func (w *Workflow) runHookBoundary(ctx context.Context, event HookEvent) (HookSummary, error) {
	result, err := w.hookRunner.Run(ctx, w.hooks, event)

	summary := HookSummary{
		Attempted: result.Attempted,
		Completed: result.Completed,
		Failed:    result.Failed,
		TimedOut:  result.TimedOut,
	}

	if err != nil {
		return summary, oops.In("session").Code("hook_failed").With("operation", "run_hooks").With("hook_event", event.Name).Wrap(err)
	}

	return summary, nil
}

func (w *Workflow) fail(ctx context.Context, operation string, err error) error {
	if err == nil {
		return nil
	}

	zerolog.Ctx(ctx).Error().
		Str("component", "session").
		Str("operation", operation).
		Str("status", "failed").
		Str("reason", sessionFailureReason(err)).
		Func(logging.Cause(err)).Msg("session operation failed")

	if marked, ok := err.(interface{ OutcomeLogged() bool }); ok && marked.OutcomeLogged() {
		return err
	}

	return loggedOutcomeError{err: err}
}

func (a EnterAction) String() string {
	switch a {
	case EnterNone:
		return "none"
	case EnterSwitch:
		return "switch"
	case EnterAttach:
		return "attach"
	default:
		return "unknown"
	}
}

func isAbsentSession(err error) bool {
	return errors.Is(err, gotmux.ErrNotFound) || errors.Is(err, gotmux.ErrNoServer)
}

func lookupSessionError(err error) error {
	if isAbsentSession(err) {
		return oops.In("session").Code("not_found").Wrap(ErrSessionNotFound)
	}

	return oops.In("session").Code("tmux_failed").With("operation", "find_session").Wrap(err)
}

func (w *Workflow) localIdentity(name string) (Identity, bool) {
	if w.historyPath == "" || w.server == nil || name == "" {
		return Identity{Origin: "", ConnectionID: "", Socket: "", RepositorySource: "", Name: ""}, false
	}

	return Identity{
		Origin:           "local",
		ConnectionID:     "",
		Socket:           w.server.Endpoint().SocketPath,
		RepositorySource: "",
		Name:             name,
	}, true
}

func sessionFailureReason(err error) string {
	if reason, ok := sessionSentinelReason(err); ok {
		return reason
	}

	if reason, ok := repositorySentinelReason(err); ok {
		return reason
	}

	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed_out"
	default:
		return "operation_failed"
	}
}

func sessionSentinelReason(err error) (string, bool) {
	if reason, ok := clientSentinelReason(err); ok {
		return reason, true
	}

	if reason, ok := hookSentinelReason(err); ok {
		return reason, true
	}

	switch {
	case err == nil:
		return "", true
	case errors.Is(err, ErrSessionNotFound):
		return "session_not_found", true
	case errors.Is(err, ErrSessionAlreadyExists):
		return "session_exists", true
	case errors.Is(err, ErrSessionAttached):
		return "session_attached", true
	case errors.Is(err, ErrSavedGenerationsProtected):
		return "saved_generations_protected", true
	case errors.Is(err, ErrSavedSessionScopeConflict):
		return "saved_session_scope_conflict", true
	case errors.Is(err, ErrSessionReplaced):
		return "session_replaced", true
	default:
		return "", false
	}
}

func clientSentinelReason(err error) (string, bool) {
	switch {
	case errors.Is(err, ErrNotInsideTmux):
		return "not_inside_tmux", true
	case errors.Is(err, ErrInvokingClientUnavailable):
		return "client_unavailable", true
	case errors.Is(err, ErrInvokingClientAmbiguous):
		return "client_ambiguous", true
	case errors.Is(err, ErrSwitchUnverified):
		return "switch_unverified", true
	case errors.Is(err, ErrTerminalRequired):
		return "terminal_required", true
	case errors.Is(err, tmuxserver.ErrLeaseRoot):
		return "endpoint_required", true
	default:
		return "", false
	}
}

func hookSentinelReason(err error) (string, bool) {
	switch {
	case errors.Is(err, ErrInvalidHookEvent):
		return "hook_event_invalid", true
	case errors.Is(err, ErrInvalidHookConfig):
		return "hook_config_invalid", true
	case errors.Is(err, ErrHookTimeoutInvalid):
		return "hook_timeout_invalid", true
	case errors.Is(err, ErrHookLimitExceeded):
		return "hook_limit_exceeded", true
	default:
		return "", false
	}
}

func repositorySentinelReason(err error) (string, bool) {
	switch {
	case errors.Is(err, store.ErrRecoveryRequired):
		return "recovery_required", true
	case errors.Is(err, store.ErrUncertain):
		return "publication_uncertain", true
	case errors.Is(err, store.ErrConflict):
		return "repository_conflict", true
	case errors.Is(err, store.ErrUnavailable):
		return "snapshot_unavailable", true
	case errors.Is(err, store.ErrCorrupt):
		return "repository_corrupt", true
	default:
		return "", false
	}
}
