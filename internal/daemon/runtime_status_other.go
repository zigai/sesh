//go:build !linux

package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/samber/oops"
)

const (
	ServerManagementUnknown = "unknown"
)

const (
	RuntimePhaseInitializing RuntimePhase = "initializing"
	RuntimePhaseRunning      RuntimePhase = "running"
	RuntimePhaseFinalSave    RuntimePhase = "final_save"
	RuntimePhaseStopping     RuntimePhase = "stopping"
)

var (
	ErrRuntimeStatusUnavailable = errors.New("daemon runtime status is unavailable")
	ErrRuntimeStatusIdentity    = errors.New("daemon runtime identity could not be verified")
	ErrRuntimeStatusActive      = errors.New("daemon runtime status endpoint is already active")
	ErrRuntimeStatusStale       = errors.New("daemon runtime status endpoint is stale")
	ErrRuntimeStatusPath        = errors.New("daemon runtime status path is invalid")
)

type RuntimePhase string

type RuntimeStatus struct {
	Protocol          int          `json:"protocol"`
	Phase             RuntimePhase `json:"phase"`
	InhibitorsHeld    bool         `json:"inhibitorsHeld"`
	LogindDelayMillis int64        `json:"logindDelayMillis"`
	ServerManagement  string       `json:"serverManagement"`
	EndpointID        string       `json:"endpointId"`
	PID               int          `json:"pid"`
	BootID            string       `json:"bootId"`
	ProcessStartTicks uint64       `json:"processStartTicks"`
}

type RuntimeLiveStatus struct{ InhibitorsHeld bool }

type RuntimeStatusConfig struct {
	RuntimeDir  string
	Endpoint    string
	LogindDelay time.Duration
	LiveStatus  func(context.Context) (RuntimeLiveStatus, error)
}

type RuntimeReporter struct{}

func RuntimeStatusPath(string, string) (string, error) {
	return "", oops.In("daemon").Code("runtime_status_unavailable").With("operation", "runtime_status_path").Wrap(ErrRuntimeStatusUnavailable)
}

func StartRuntimeReporter(context.Context, RuntimeStatusConfig) (*RuntimeReporter, error) {
	return nil, oops.In("daemon").Code("runtime_status_unavailable").With("operation", "start_runtime_reporter").Wrap(ErrRuntimeStatusUnavailable)
}

func QueryRuntimeStatus(context.Context, string) (RuntimeStatus, error) {
	return RuntimeStatus{}, oops.In("daemon").Code("runtime_status_unavailable").With("operation", "query_runtime_status").Wrap(ErrRuntimeStatusUnavailable)
}

func (r *RuntimeReporter) Update(RuntimePhase) {}

func (r *RuntimeReporter) Close() error { return nil }
