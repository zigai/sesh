//go:build linux || darwin

package doctor

import (
	"context"
	"errors"
	"strings"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"
)

const (
	StatusOK   Status = "ok"
	StatusInfo Status = "info"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
	StatusSkip Status = "skip"
)

const (
	ReasonUnavailable       = "unavailable"
	ReasonIncompatible      = "incompatible"
	ReasonNotFound          = "not_found"
	ReasonNotRunning        = "not_running"
	ReasonUninitialized     = "uninitialized"
	ReasonUnsafe            = "unsafe"
	ReasonRecoveryRequired  = "recovery_required"
	ReasonCorrupt           = "corrupt"
	ReasonIncomplete        = "incomplete"
	ReasonEmpty             = "empty"
	ReasonInspectionFailed  = "inspection_failed"
	ReasonStale             = "stale"
	ReasonUnverifiable      = "unverifiable"
	ReasonTimedOut          = "timed_out"
	ReasonCanceled          = "canceled"
	ReasonInvalid           = "invalid"
	ReasonDuplicate         = "duplicate"
	ReasonInhibitorReleased = "inhibitor_released"
	ReasonSaveUnfinished    = "latest_save_unfinished"
)

const (
	CheckInstallPath        = "install.path"
	CheckConfigFile         = "config.file"
	CheckTmuxClient         = "tmux.client"
	CheckTmuxServer         = "tmux.server"
	CheckFields             = "fields.custom"
	CheckSnapshotRepository = "snapshots.repository"
	CheckSnapshotCurrent    = "snapshots.current"
	CheckActiveRestores     = "snapshots.restores"
	CheckHistoryFile        = "history.file"
	CheckLogFile            = "logs.file"
	CheckServiceUnits       = "services.units"
	CheckServiceOOMPolicy   = "services.oom-policy"
	CheckAutosaveDaemon     = "autosave.daemon"
	CheckSmokeSessions      = "smoke.sessions"
)

const (
	ConfigLoaded    = "loaded"
	ConfigNotFound  = "not_found"
	ConfigNotLoaded = "not_loaded"
	ConfigSkipped   = "skipped"
)

const (
	DaemonReady        = "ready"
	DaemonNotReady     = "not_ready"
	DaemonAbsent       = "absent"
	DaemonStale        = "stale"
	DaemonUnverifiable = "unverifiable"
)

const (
	flagConfig     = "--config"
	flagNoConfig   = "--no-config"
	flagSocket     = "--socket"
	flagRepository = "--repository"
	flagHistory    = "--history"
)

// Statuses lists every status in the order the summary reports them.
var Statuses = []Status{StatusOK, StatusInfo, StatusWarn, StatusFail, StatusSkip}

var groups = map[string]string{
	"install":   "Install",
	"config":    "Config",
	"tmux":      "Tmux",
	"fields":    "Fields",
	"snapshots": "Snapshots",
	"history":   "History",
	"logs":      "Logs",
	"services":  "Services",
	"autosave":  "Autosave",
	"smoke":     "Smoke",
}

type Status string

// Result is one check's finding. Every warn and fail result carries a Fix; Needs names the
// check whose failure kept this one from running.
type Result struct {
	ID      string   `json:"id"`
	Group   string   `json:"group"`
	Status  Status   `json:"status"`
	Reason  string   `json:"reason,omitempty"`
	Message string   `json:"message"`
	Details []string `json:"details,omitempty"`
	Needs   string   `json:"needs,omitempty"`
	Fix     *Fix     `json:"fix"`
}

// Fix is either a command to run or, when no command can do it, a manual step.
type Fix struct {
	Command string `json:"command,omitempty"`
	Manual  string `json:"manual,omitempty"`
}

type Summary struct {
	OK   int `json:"ok"`
	Info int `json:"info"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
	Skip int `json:"skip"`
}

type CustomField struct {
	Name    string
	Command string
}

// ConfigFile is one configuration source in precedence order. Source names the flag or
// environment variable that selected it.
type ConfigFile struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Source string `json:"source,omitempty"`
}

type Daemon struct {
	State          string
	Phase          string
	InhibitorsHeld bool
	// RuntimePath is the coordinator's runtime status socket for the selected tmux server.
	RuntimePath string
}

type Options struct {
	// PathEnv is the PATH searched for copies of sesh, tmux and custom field commands.
	PathEnv    string
	Executable string

	ConfigFiles []ConfigFile
	// ConfigError describes why the configuration failed to load; the other checks then run on
	// built-in settings.
	ConfigError     string
	AutosaveEnabled bool
	CustomFields    []CustomField

	// Flags are the doctor's own configuration and location flags, by name, that fix commands
	// repeat so they act on the same configuration, server and repository. A flag without a
	// value is a switch.
	Flags map[string]string

	Socket string
	// DefaultSocket is the socket a plain tmux command uses.
	DefaultSocket string
	Server        *gotmux.Server
	// ServerError is why the tmux server handle could not be opened.
	ServerError error

	SnapshotDir string
	HistoryPath string
	LogPath     string

	Daemon Daemon
	// ServiceDirs is the systemd user unit search path, highest precedence first; nil skips the
	// service checks.
	ServiceDirs []string
	// ValidateCommand reports whether this sesh accepts a command line, given without the
	// program; nil skips the unit command check.
	ValidateCommand func(args []string) error
}

type Service struct {
	opts        Options
	unitOf      func(pid int) (string, error)
	oomPolicyOf func(ctx context.Context, unit string) (string, error)

	serverPID    int
	serverStatus Status
	sessions     []string
	listErr      error
	repoStatus   Status
}

// FieldCheck returns the id of the check for the custom display field name.
func FieldCheck(name string) string {
	return "fields." + name
}

// Name is the check id without its group, as shown under the group heading.
func (r Result) Name() string {
	_, name, _ := strings.Cut(r.ID, ".")

	return name
}

func Summarize(results []Result) Summary {
	var summary Summary

	for _, result := range results {
		switch result.Status {
		case StatusOK:
			summary.OK++
		case StatusInfo:
			summary.Info++
		case StatusWarn:
			summary.Warn++
		case StatusFail:
			summary.Fail++
		case StatusSkip:
			summary.Skip++
		}
	}

	return summary
}

func (s Summary) Count(status Status) int {
	switch status {
	case StatusOK:
		return s.OK
	case StatusInfo:
		return s.Info
	case StatusWarn:
		return s.Warn
	case StatusFail:
		return s.Fail
	case StatusSkip:
		return s.Skip
	default:
		return 0
	}
}

func NewService(opts Options) *Service {
	return &Service{
		opts:         opts,
		unitOf:       serviceUnitOf,
		oomPolicyOf:  serviceOOMPolicyOf,
		serverPID:    0,
		serverStatus: "",
		sessions:     nil,
		listErr:      nil,
		repoStatus:   "",
	}
}

func Run(ctx context.Context, opts Options) ([]Result, error) {
	return NewService(opts).Run(ctx)
}

// Run executes every check in report order. It stops early only when ctx ends, returning the
// results gathered so far.
func (s *Service) Run(ctx context.Context) ([]Result, error) {
	steps := []func(context.Context) ([]Result, error){
		func(context.Context) ([]Result, error) { return []Result{s.checkInstallPath()}, nil },
		func(context.Context) ([]Result, error) { return []Result{s.checkConfig()}, nil },
		s.checkTmux,
		func(context.Context) ([]Result, error) { return s.checkCustomFields(), nil },
		s.checkSnapshot,
		func(ctx context.Context) ([]Result, error) { return []Result{s.checkActiveRestores(ctx)}, nil },
		func(context.Context) ([]Result, error) { return []Result{s.checkHistory()}, nil },
		func(context.Context) ([]Result, error) { return []Result{s.checkLogFile()}, nil },
		func(context.Context) ([]Result, error) { return []Result{s.checkServiceUnits()}, nil },
		func(ctx context.Context) ([]Result, error) { return []Result{s.checkServiceOOMPolicy(ctx)}, nil },
		func(context.Context) ([]Result, error) { return []Result{s.checkDaemon()}, nil },
		func(context.Context) ([]Result, error) { return []Result{s.checkSmoke()}, nil },
	}

	var results []Result

	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			return results, doctorError(err)
		}

		stepResults, err := step(ctx)
		results = append(results, stepResults...)

		if err != nil {
			return results, doctorError(err)
		}
	}

	if err := ctx.Err(); err != nil {
		return results, doctorError(err)
	}

	return results, nil
}

func HasFailures(results []Result) bool {
	return Summarize(results).Fail > 0
}

func newResult(id string, status Status, message string) Result {
	group, _, _ := strings.Cut(id, ".")

	return Result{ID: id, Group: groups[group], Status: status, Reason: "", Message: message, Details: nil, Needs: "", Fix: nil}
}

func (r Result) withReason(reason string) Result {
	r.Reason = reason

	return r
}

func (r Result) withCommand(command string) Result {
	r.Fix = &Fix{Command: command, Manual: ""}

	return r
}

func (r Result) withManual(step string) Result {
	r.Fix = &Fix{Command: "", Manual: step}

	return r
}

func (r Result) withDetails(details ...string) Result {
	r.Details = append(r.Details, details...)

	return r
}

func skipped(id, needs, message string) Result {
	result := newResult(id, StatusSkip, message)
	result.Needs = needs

	return result
}

// seshCommand renders a sesh command line that repeats the configuration flags and the given
// location flags the doctor ran with.
func (s *Service) seshCommand(command string, flags []string, args ...string) string {
	parts := []string{programName, command}

	for _, name := range append([]string{flagConfig, flagNoConfig}, flags...) {
		value, given := s.opts.Flags[name]
		if !given {
			continue
		}

		parts = append(parts, name)
		if value != "" {
			parts = append(parts, shellArg(value))
		}
	}

	for _, arg := range args {
		parts = append(parts, shellArg(arg))
	}

	return strings.Join(parts, " ")
}

func (s *Service) doctorCommand() string {
	return s.seshCommand("doctor", []string{flagSocket, flagRepository, flagHistory})
}

func doctorError(err error) error {
	code := "doctor_failed"

	switch {
	case errors.Is(err, context.Canceled):
		code = "operation_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		code = "operation_timed_out"
	}

	return oops.In("doctor").Code(code).With("operation", "run").Wrap(err)
}
