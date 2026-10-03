//go:build linux

package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	"golang.org/x/sys/unix"

	processintegration "github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/privatefs"
)

const (
	runtimeStatusProtocol      = 1
	runtimeStatusSocketBytes   = 16
	runtimeStatusMaximumBytes  = 4 * 1024
	runtimeStatusProbeTimeout  = 250 * time.Millisecond
	runtimeStatusWriteDeadline = time.Second
	linuxUnixPathMaximum       = 107
)

const (
	RuntimePhaseInitializing RuntimePhase = "initializing"
	RuntimePhaseRunning      RuntimePhase = "running"
	RuntimePhaseDraining     RuntimePhase = "draining"
	RuntimePhaseFinalSave    RuntimePhase = "final_save"
	RuntimePhaseStopping     RuntimePhase = "stopping"
)

const (
	ServerManagementUnknown = "unknown"
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
	Protocol             int          `json:"protocol"`
	Phase                RuntimePhase `json:"phase"`
	InhibitorsHeld       bool         `json:"inhibitorsHeld"`
	LogindDelayMillis    int64        `json:"logindDelayMillis"`
	AttemptTimeoutMillis int64        `json:"attemptTimeoutMillis"`
	DrainGraceMillis     int64        `json:"drainGraceMillis"`
	ServerManagement     string       `json:"serverManagement"`
	EndpointID           string       `json:"endpointId"`
	PID                  int          `json:"pid"`
	BootID               string       `json:"bootId"`
	ProcessStartTicks    uint64       `json:"processStartTicks"`
}

type RuntimeStatusConfig struct {
	RuntimeDir     string
	Endpoint       string
	LogindDelay    time.Duration
	AttemptTimeout time.Duration
	DrainGrace     time.Duration
	LiveStatus     func(context.Context) (RuntimeLiveStatus, error)
}

type RuntimeLiveStatus struct {
	InhibitorsHeld bool
}

type RuntimeReporter struct {
	listener *net.UnixListener
	dir      *privatefs.Dir
	lock     *os.File
	done     chan struct{}
	name     string
	socket   privatefs.EntryIdentity

	mu      sync.RWMutex
	status  RuntimeStatus
	live    func(context.Context) (RuntimeLiveStatus, error)
	version uint64
}

type runtimeIdentity struct {
	pid        int
	uid        uint32
	bootID     string
	startTicks uint64
}

func RuntimeStatusPath(runtimeDir, endpoint string) (string, error) {
	if !filepath.IsAbs(runtimeDir) || filepath.Clean(runtimeDir) != runtimeDir ||
		!filepath.IsAbs(endpoint) || filepath.Clean(endpoint) != endpoint {
		return "", oops.In("daemon").Code("runtime_status_failed").With("operation", "runtime_status_path").Wrap(ErrRuntimeStatusPath)
	}

	name := "daemon-" + runtimeEndpointID(endpoint) + ".sock"

	path := filepath.Join(runtimeDir, "sesh", name)
	if len(path) > linuxUnixPathMaximum {
		return "", oops.In("daemon").Code("runtime_status_failed").With("operation", "runtime_status_path").Wrap(ErrRuntimeStatusPath)
	}

	return path, nil
}

func StartRuntimeReporter(ctx context.Context, config RuntimeStatusConfig) (*RuntimeReporter, error) {
	if config.LiveStatus == nil {
		return nil, oops.In("daemon").Code("runtime_status_unavailable").With("operation", "start_runtime_reporter").Wrap(ErrRuntimeStatusUnavailable)
	}

	_, name, dir, lock, err := prepareRuntimeEndpoint(ctx, config)
	if err != nil {
		return nil, err
	}

	identity, err := currentRuntimeIdentity(ctx)
	if err != nil {
		return nil, oops.In("daemon").Code("runtime_status_failed").With("operation", "start_runtime_reporter").Wrap(errors.Join(err, lock.Close(), dir.Close()))
	}

	listener, err := listenRuntimeEndpoint(dir, name)
	if err != nil {
		return nil, oops.In("daemon").Code("runtime_status_failed").With("operation", "start_runtime_reporter").Wrap(errors.Join(err, lock.Close(), dir.Close()))
	}

	listener.SetUnlinkOnClose(false)

	pinnedPath, err := dir.PinnedPath(name)
	if err != nil {
		return nil, oops.In("daemon").Code("runtime_status_failed").With("operation", "start_runtime_reporter").Wrap(errors.Join(err, listener.Close(), lock.Close(), dir.Close()))
	}

	if err := os.Chmod(pinnedPath, 0o600); err != nil {
		return nil, oops.In("daemon").Code("runtime_status_failed").With("operation", "start_runtime_reporter").Wrap(errors.Join(err, listener.Close(), lock.Close(), dir.Close()))
	}

	socketIdentity, err := dir.Inspect(name)

	uid, uidErr := effectiveUID()
	if err != nil || uidErr != nil || socketIdentity.Mode&uint64(unix.S_IFMT) != uint64(unix.S_IFSOCK) || socketIdentity.UID != uid {
		return nil, oops.In("daemon").Code("runtime_status_failed").With("operation", "start_runtime_reporter").Wrap(errors.Join(ErrRuntimeStatusIdentity, err, uidErr, listener.Close(), lock.Close(), dir.Close()))
	}

	reporter := &RuntimeReporter{
		listener: listener,
		dir:      dir,
		lock:     lock,
		done:     make(chan struct{}),
		name:     name,
		socket:   socketIdentity,
		mu:       sync.RWMutex{},
		live:     config.LiveStatus,
		status: RuntimeStatus{
			Protocol:             runtimeStatusProtocol,
			Phase:                RuntimePhaseInitializing,
			InhibitorsHeld:       false,
			LogindDelayMillis:    config.LogindDelay.Milliseconds(),
			AttemptTimeoutMillis: config.AttemptTimeout.Milliseconds(),
			DrainGraceMillis:     config.DrainGrace.Milliseconds(),
			ServerManagement:     ServerManagementUnknown,
			EndpointID:           runtimeEndpointID(config.Endpoint),
			PID:                  identity.pid,
			BootID:               identity.bootID,
			ProcessStartTicks:    identity.startTicks,
		},
		version: 1,
	}

	go reporter.serve(context.WithoutCancel(ctx))

	return reporter, nil
}

func (r *RuntimeReporter) Update(phase RuntimePhase) {
	r.mu.Lock()
	r.status.Phase = phase
	r.version++
	r.mu.Unlock()
}

func (r *RuntimeReporter) Close() error {
	r.Update(RuntimePhaseStopping)
	listenErr := r.listener.Close()
	<-r.done
	removeErr := r.removeSocket()

	if err := errors.Join(listenErr, removeErr, r.lock.Close(), r.dir.Close()); err != nil {
		return oops.In("daemon").Code("runtime_status_failed").With("operation", "close_runtime_reporter").Wrap(err)
	}

	return nil
}

func QueryRuntimeStatus(ctx context.Context, path string) (RuntimeStatus, error) {
	var status RuntimeStatus

	unixConnection, peer, err := openRuntimeConnection(ctx, path)
	if err != nil {
		return status, err
	}

	deadline := time.Now().Add(runtimeStatusWriteDeadline)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}

	if err := unixConnection.SetReadDeadline(deadline); err != nil {
		return status, oops.In("daemon").Code("runtime_status_unavailable").With("operation", "query_runtime_status").Wrap(errors.Join(ErrRuntimeStatusUnavailable, err, unixConnection.Close()))
	}

	stopCancel := context.AfterFunc(ctx, func() { _ = unixConnection.Close() })
	defer stopCancel()

	data, readErr := io.ReadAll(io.LimitReader(unixConnection, runtimeStatusMaximumBytes+1))

	closeErr := unixConnection.Close()
	if readErr != nil || closeErr != nil {
		return status, oops.In("daemon").Code("runtime_status_unavailable").With("operation", "query_runtime_status").Wrap(errors.Join(ErrRuntimeStatusUnavailable, runtimeContextCause(ctx), readErr, closeErr))
	}

	if len(data) > runtimeStatusMaximumBytes {
		return status, oops.In("daemon").Code("runtime_status_unavailable").With("operation", "query_runtime_status").Wrap(ErrRuntimeStatusUnavailable)
	}

	if err := json.Unmarshal(data, &status, json.RejectUnknownMembers(true)); err != nil {
		return RuntimeStatus{}, oops.In("daemon").Code("runtime_status_unavailable").With("operation", "query_runtime_status").Wrap(errors.Join(ErrRuntimeStatusUnavailable, err))
	}

	if err := verifyRuntimeStatus(ctx, status, peer.pid, path); err != nil {
		return RuntimeStatus{}, err
	}

	return status, nil
}

func runtimeContextCause(ctx context.Context) error {
	if cause := context.Cause(ctx); cause != nil {
		return oops.In("daemon").Code("operation_canceled").With("operation", "context_cause").Wrap(cause)
	}

	deadline, ok := ctx.Deadline()
	if ok && !time.Now().Before(deadline) {
		return oops.In("daemon").Code("operation_timed_out").With("operation", "context_cause").Wrap(context.DeadlineExceeded)
	}

	return nil
}

func listenRuntimeEndpoint(dir *privatefs.Dir, name string) (*net.UnixListener, error) {
	pinnedPath, err := dir.PinnedPath(name)
	if err != nil {
		return nil, oops.In("daemon").Code("runtime_status_failed").With("operation", "listen_runtime_endpoint").Wrap(err)
	}

	if len(pinnedPath) > linuxUnixPathMaximum {
		return nil, oops.In("daemon").Code("runtime_status_failed").With("operation", "listen_runtime_endpoint").Wrap(ErrRuntimeStatusPath)
	}

	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: pinnedPath, Net: "unix"})
	if err != nil {
		return nil, oops.In("daemon").Code("runtime_status_failed").With("operation", "listen_runtime_endpoint").Wrap(err)
	}

	return listener, nil
}

func prepareRuntimeEndpoint(
	ctx context.Context,
	config RuntimeStatusConfig,
) (string, string, *privatefs.Dir, *os.File, error) {
	path, err := RuntimeStatusPath(config.RuntimeDir, config.Endpoint)
	if err != nil {
		return "", "", nil, nil, err
	}

	dir, err := privatefs.Ensure(filepath.Dir(path))
	if err != nil {
		return "", "", nil, nil, oops.In("daemon").Code("runtime_status_failed").With("operation", "prepare_runtime_endpoint").Wrap(err)
	}

	name := filepath.Base(path)
	lockCtx, lockCancel := context.WithTimeout(ctx, runtimeStatusProbeTimeout)
	lock, lockErr := dir.Lock(lockCtx, name+".lock")

	lockCancel()

	if lockErr != nil {
		if errors.Is(lockErr, context.DeadlineExceeded) {
			lockErr = ErrRuntimeStatusActive
		}

		return "", "", nil, nil, oops.In("daemon").Code("runtime_status_failed").With("operation", "prepare_runtime_endpoint").Wrap(errors.Join(lockErr, dir.Close()))
	}

	if err := checkRuntimeSocketAvailable(ctx, dir, name, path); err != nil {
		return "", "", nil, nil, oops.In("daemon").Code("runtime_status_failed").With("operation", "prepare_runtime_endpoint").Wrap(errors.Join(err, lock.Close(), dir.Close()))
	}

	return path, name, dir, lock, nil
}

func openRuntimeConnection(ctx context.Context, path string) (*net.UnixConn, runtimeIdentity, error) {
	var dialer net.Dialer

	connection, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, runtimeIdentity{}, oops.In("daemon").Code("runtime_status_unavailable").With("operation", "open_runtime_connection").Wrap(errors.Join(ErrRuntimeStatusUnavailable, err))
	}

	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return nil, runtimeIdentity{}, oops.In("daemon").Code("runtime_status_failed").With("operation", "open_runtime_connection").Wrap(errors.Join(ErrRuntimeStatusIdentity, connection.Close()))
	}

	peer, err := unixPeerIdentity(unixConnection)
	if err != nil {
		return nil, runtimeIdentity{}, oops.In("daemon").Code("runtime_status_failed").With("operation", "open_runtime_connection").Wrap(errors.Join(err, unixConnection.Close()))
	}

	uid, err := effectiveUID()
	if err != nil || peer.uid != uid {
		return nil, runtimeIdentity{}, oops.In("daemon").Code("runtime_status_failed").With("operation", "open_runtime_connection").Wrap(errors.Join(ErrRuntimeStatusIdentity, err, unixConnection.Close()))
	}

	return unixConnection, peer, nil
}

func (r *RuntimeReporter) serve(ctx context.Context) {
	defer close(r.done)

	for {
		connection, err := r.listener.AcceptUnix()
		if err != nil {
			r.mu.RLock()
			stopping := r.status.Phase == RuntimePhaseStopping
			r.mu.RUnlock()

			if !stopping {
				zerolog.Ctx(ctx).Error().Str("component", "daemon").Str("operation", "serve_runtime_status").
					Str("status", "failed").Str("reason", "listener_failed").Msg("runtime status listener stopped unexpectedly")
			}

			return
		}

		r.respond(ctx, connection)
	}
}

func (r *RuntimeReporter) respond(ctx context.Context, connection *net.UnixConn) {
	defer func() { _ = connection.Close() }()

	peer, err := unixPeerIdentity(connection)

	uid, uidErr := effectiveUID()
	if err != nil || uidErr != nil || peer.uid != uid {
		return
	}

	r.mu.RLock()
	status := r.status
	version := r.version
	r.mu.RUnlock()

	probeCtx, cancel := context.WithTimeout(ctx, runtimeStatusProbeTimeout)
	live, err := r.live(probeCtx)

	cancel()

	if err != nil {
		return
	}

	r.mu.RLock()
	unchanged := r.version == version
	r.mu.RUnlock()

	if !unchanged {
		return
	}

	status.InhibitorsHeld = live.InhibitorsHeld

	data, err := json.Marshal(status)
	if err != nil || len(data) > runtimeStatusMaximumBytes {
		return
	}

	_ = connection.SetWriteDeadline(time.Now().Add(runtimeStatusWriteDeadline))
	_, _ = connection.Write(data)
}

func runtimeEndpointID(endpoint string) string {
	digest := sha256.Sum256([]byte(endpoint))

	return hex.EncodeToString(digest[:runtimeStatusSocketBytes])
}

func checkRuntimeSocketAvailable(ctx context.Context, dir *privatefs.Dir, name, path string) error {
	entry, err := dir.Inspect(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return oops.In("daemon").Code("runtime_status_failed").With("operation", "check_runtime_socket").Wrap(err)
	}

	uid, err := effectiveUID()
	if err != nil || entry.Mode&uint64(unix.S_IFMT) != uint64(unix.S_IFSOCK) || entry.UID != uid {
		return ErrRuntimeStatusActive
	}

	_, queryErr := QueryRuntimeStatus(ctx, path)
	if queryErr == nil {
		return ErrRuntimeStatusActive
	}

	if !errors.Is(queryErr, unix.ECONNREFUSED) && !errors.Is(queryErr, os.ErrNotExist) {
		return errors.Join(ErrRuntimeStatusActive, queryErr)
	}

	return ErrRuntimeStatusStale
}

func (r *RuntimeReporter) removeSocket() error {
	identity, err := r.dir.Inspect(r.name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return oops.In("daemon").Code("runtime_status_failed").With("operation", "remove_socket").Wrap(err)
	}

	if identity != r.socket {
		return ErrRuntimeStatusIdentity
	}

	if err := r.dir.Remove(r.name); err != nil {
		return oops.In("daemon").Code("runtime_status_failed").With("operation", "remove_socket").Wrap(err)
	}

	return nil
}

func currentRuntimeIdentity(ctx context.Context) (runtimeIdentity, error) {
	pid := os.Getpid()

	identity, err := processintegration.ReadIdentity(ctx, pid)
	if err != nil {
		return runtimeIdentity{}, oops.In("daemon").Code("runtime_status_failed").With("operation", "current_runtime_identity").Wrap(err)
	}

	uid, err := effectiveUID()
	if err != nil {
		return runtimeIdentity{}, err
	}

	return runtimeIdentity{pid: pid, uid: uid, bootID: identity.BootID, startTicks: identity.StartTime}, nil
}

func unixPeerIdentity(connection *net.UnixConn) (runtimeIdentity, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return runtimeIdentity{}, oops.In("daemon").Code("runtime_status_failed").With("operation", "peer_identity").Wrap(errors.Join(ErrRuntimeStatusIdentity, err))
	}

	var (
		credentials *unix.Ucred
		socketErr   error
	)

	if err := raw.Control(func(fd uintptr) {
		credentials, socketErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return runtimeIdentity{}, oops.In("daemon").Code("runtime_status_failed").With("operation", "peer_identity").Wrap(errors.Join(ErrRuntimeStatusIdentity, err))
	}

	if socketErr != nil || credentials == nil {
		return runtimeIdentity{}, oops.In("daemon").Code("runtime_status_failed").With("operation", "peer_identity").Wrap(errors.Join(ErrRuntimeStatusIdentity, socketErr))
	}

	return runtimeIdentity{pid: int(credentials.Pid), uid: credentials.Uid, bootID: "", startTicks: 0}, nil
}

func effectiveUID() (uint32, error) {
	value := os.Geteuid()
	if value < 0 || uint64(value) > uint64(^uint32(0)) {
		return 0, oops.In("daemon").Code("runtime_status_failed").With("operation", "effective_uid").Wrap(ErrRuntimeStatusIdentity)
	}

	return uint32(value), nil
}

func verifyRuntimeStatus(ctx context.Context, status RuntimeStatus, peerPID int, path string) error {
	expectedEndpoint := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "daemon-"), ".sock")
	if err := validateRuntimeStatus(status, peerPID, expectedEndpoint); err != nil {
		return oops.In("daemon").Code("runtime_status_failed").With("operation", "verify_runtime_status").Wrap(err)
	}

	identity, err := processintegration.ReadIdentity(ctx, peerPID)
	if err != nil {
		return oops.In("daemon").Code("runtime_status_failed").With("operation", "verify_runtime_status").Wrap(errors.Join(ErrRuntimeStatusIdentity, err))
	}

	if identity.BootID != status.BootID || identity.StartTime != status.ProcessStartTicks {
		return oops.In("daemon").Code("runtime_status_failed").With("operation", "verify_runtime_status").Wrap(ErrRuntimeStatusIdentity)
	}

	return nil
}

func validateRuntimeStatus(status RuntimeStatus, peerPID int, expectedEndpoint string) error {
	if status.Protocol != runtimeStatusProtocol || status.PID != peerPID {
		return ErrRuntimeStatusIdentity
	}

	if status.ServerManagement != ServerManagementUnknown {
		return ErrRuntimeStatusIdentity
	}

	if len(expectedEndpoint) != runtimeStatusSocketBytes*2 || status.EndpointID != expectedEndpoint {
		return ErrRuntimeStatusIdentity
	}

	switch status.Phase {
	case RuntimePhaseInitializing, RuntimePhaseRunning, RuntimePhaseDraining, RuntimePhaseFinalSave, RuntimePhaseStopping:
	default:
		return ErrRuntimeStatusIdentity
	}

	if status.LogindDelayMillis <= 0 || status.AttemptTimeoutMillis <= 0 || status.DrainGraceMillis <= 0 {
		return ErrRuntimeStatusIdentity
	}

	return nil
}
