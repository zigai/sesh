//go:build linux || darwin

package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/samber/oops"
	"golang.org/x/sync/errgroup"

	"github.com/zigai/sesh/internal/config"
)

const (
	defaultSSHArgCapacity = 16

	maxDiagnosticsBytes = 32 << 10
	defaultConnect      = 10 * time.Second
	defaultHeartbeat    = 15 * time.Second
	defaultAction       = 2 * time.Minute
	defaultParallel     = 4
)

const (
	ConnectionErrorAuthUnavailable       = "auth_unavailable"
	ConnectionErrorHostKeyRequired       = "host_key_required"
	ConnectionErrorTimeout               = "timeout"
	ConnectionErrorRemotePeerUnavailable = "remote_peer_unavailable"
	ConnectionErrorIncompatibleProtocol  = "incompatible_protocol"
	ConnectionErrorPeerMismatch          = "peer_mismatch"
	ConnectionErrorCacheCapacity         = "cache_capacity"
	ConnectionErrorStale                 = "stale"
	ConnectionErrorUnavailable           = "unavailable"
)

var (
	ErrConnectionUnavailable = errors.New("SSH connection unavailable")

	ErrRemoteDisabled = errors.New("remote sessions are disabled")

	ErrConnectionChanged = errors.New("remote connection configuration changed")

	ErrConnectionMissing = errors.New("remote connection is not configured")

	ErrSSHAuthUnavailable = errors.New("SSH authentication unavailable")

	ErrSSHHostKeyRequired = errors.New("SSH host key acceptance required")

	ErrRemotePeerUnavailable = errors.New("remote peer entry point unavailable")

	ErrForwardingFailed = errors.New("SSH reverse forwarding failed")

	errNilCommand = errors.New("SSH command is not configured")
)

var connectionErrorCodes = []string{
	ConnectionErrorAuthUnavailable,
	ConnectionErrorHostKeyRequired,
	ConnectionErrorTimeout,
	ConnectionErrorRemotePeerUnavailable,
	ConnectionErrorIncompatibleProtocol,
	ConnectionErrorPeerMismatch,
	ConnectionErrorCacheCapacity,
	ConnectionErrorStale,
	ConnectionErrorUnavailable,
}

type SocketForward struct {
	RemotePath string
	LocalPath  string
}

type SSHCommandOptions struct {
	Interactive bool
	Forward     []SocketForward
	Command     func(ctx context.Context, executable string, arguments ...string) *exec.Cmd
}

type ConnectionCheck struct {
	ID       string
	Host     string
	Reason   string
	Duration time.Duration
	Err      error
}

type DiagnosticsBuffer struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	maxBytes int
}

func NewDiagnosticsBuffer(maxBytes int) *DiagnosticsBuffer {
	return &DiagnosticsBuffer{mu: sync.Mutex{}, buffer: bytes.Buffer{}, maxBytes: maxBytes}
}

func (b *DiagnosticsBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if remaining := b.maxBytes - b.buffer.Len(); remaining > 0 {
		b.buffer.Write(data[:min(len(data), remaining)])
	}

	return len(data), nil
}

func (b *DiagnosticsBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buffer.String()
}

func ParsedDuration(value string, fallback time.Duration) time.Duration {
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}

	return parsed
}

func ConnectionDigest(connection config.RemoteConnectionConfig) string {
	encoded, err := json.Marshal(struct {
		ID         string `json:"id"`
		Host       string `json:"host"`
		PeerPath   string `json:"peerPath"`
		TmuxSocket string `json:"tmuxSocket"`
	}{
		ID:         connection.ID,
		Host:       connection.Host,
		PeerPath:   connection.ResolvedSeshPath(),
		TmuxSocket: connection.TmuxSocket,
	})
	if err != nil {
		panic(fmt.Sprintf("encoding string-only connection digest failed: %v", err))
	}

	digest := sha256.Sum256(encoded)

	return hex.EncodeToString(digest[:])
}

func FindConnection(remote config.RemoteConfig, id string) (config.RemoteConnectionConfig, error) {
	for _, connection := range remote.Connections {
		if connection.ID == id {
			return connection, nil
		}
	}

	return config.RemoteConnectionConfig{}, oops.In("remote").With("operation", "find_connection").With("connection_id", id).Wrap(ErrConnectionMissing)
}

func PeerEntryPoint(connection config.RemoteConnectionConfig, channel Channel) []string {
	return []string{connection.ResolvedSeshPath(), channelCommand, peerCommand, string(channel)}
}

func NewSSHCommand(ctx context.Context, remote config.RemoteConfig, connection config.RemoteConnectionConfig, options SSHCommandOptions, remoteCommand ...string) (*exec.Cmd, error) {
	if strings.HasPrefix(connection.Host, "-") || strings.TrimSpace(connection.Host) == "" {
		return nil, oops.In("remote").With("operation", "new_ssh_command").With("host", connection.Host).Wrap(ErrInvalidFrame)
	}

	arguments := make([]string, 0, defaultSSHArgCapacity)

	if options.Interactive {
		arguments = append(arguments, "-t")
	} else {
		arguments = append(arguments, "-T", "-o", "BatchMode=yes")
	}

	connectTimeout := ParsedDuration(remote.ConnectTimeout, defaultConnect)
	heartbeat := ParsedDuration(remote.HeartbeatInterval, defaultHeartbeat)

	arguments = append(
		arguments,
		"-o", "ConnectTimeout="+strconv.Itoa(max(int(connectTimeout.Seconds()), 1)),
		"-o", "ServerAliveInterval="+strconv.Itoa(max(int(heartbeat.Seconds()), 1)),
		"-o", "ServerAliveCountMax=3",
	)

	if len(options.Forward) == 0 {
		arguments = append(arguments, "-o", "ClearAllForwardings=yes")
	} else {
		arguments = append(
			arguments,
			"-o", "ExitOnForwardFailure=yes",
			"-o", "StreamLocalBindUnlink=no",
		)

		for _, forward := range options.Forward {
			if !validSocketPath(forward.RemotePath) || !validSocketPath(forward.LocalPath) {
				return nil, oops.In("remote").With("operation", "new_ssh_command").Wrap(ErrUnsafePath)
			}

			arguments = append(arguments, "-R", forward.RemotePath+":"+forward.LocalPath)
		}
	}

	arguments = append(arguments, "--", connection.Host, ShellJoin(remoteCommand))

	command := options.Command
	if command == nil {
		command = exec.CommandContext
	}

	executable := remote.SSHPath
	if executable == "" {
		executable = "ssh"
	}

	return command(ctx, executable, arguments...), nil
}

func ShellJoin(arguments []string) string {
	quoted := make([]string, len(arguments))

	for index, argument := range arguments {
		quoted[index] = "'" + strings.ReplaceAll(argument, "'", `'\''`) + "'"
	}

	return strings.Join(quoted, " ")
}

func CheckConnections(ctx context.Context, remote config.RemoteConfig) []ConnectionCheck {
	if !remote.Enabled {
		return nil
	}

	startedAt := time.Now()
	results := make([]ConnectionCheck, len(remote.Connections))

	limit := remote.MaxParallelConnects
	if limit <= 0 {
		limit = defaultParallel
	}

	var group errgroup.Group

	group.SetLimit(limit)

	for index, connection := range remote.Connections {
		group.Go(func() error {
			if ctx.Err() != nil {
				results[index] = ConnectionCheck{
					ID: connection.ID, Host: connection.Host,
					Reason: ConnectionErrorUnavailable, Duration: 0, Err: ctx.Err(),
				}

				return ctx.Err()
			}

			results[index] = checkConnection(ctx, remote, connection)

			return nil
		})
	}

	_ = group.Wait()

	_ = startedAt

	return results
}

func ClassifySSHFailure(ctxErr error, diagnostics string) error {
	if errors.Is(ctxErr, context.Canceled) {
		return context.Canceled
	}

	if errors.Is(ctxErr, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}

	message := strings.ToLower(diagnostics)

	switch {
	case strings.Contains(message, "host key verification failed"),
		strings.Contains(message, "remote host identification has changed"),
		strings.Contains(message, "authenticity of host"):
		return ErrSSHHostKeyRequired
	case strings.Contains(message, "permission denied"),
		strings.Contains(message, "authentication failed"),
		strings.Contains(message, "too many authentication failures"),
		strings.Contains(message, "no supported authentication methods"):
		return ErrSSHAuthUnavailable
	case strings.Contains(message, "connection timed out"),
		strings.Contains(message, "operation timed out"),
		strings.Contains(message, "connection refused"):
		return context.DeadlineExceeded
	case strings.Contains(message, "command not found"),
		strings.Contains(message, "no such file or directory"),
		strings.Contains(message, ": not found"):
		return ErrRemotePeerUnavailable
	case strings.Contains(message, "remote port forwarding failed"),
		strings.Contains(message, "cannot listen to port"),
		strings.Contains(message, "address already in use"):
		return ErrForwardingFailed
	default:
		return ErrConnectionUnavailable
	}
}

func ConnectionErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrInvocationNotFound):
		return ""
	case errors.Is(err, ErrPeerMismatch):
		return ConnectionErrorPeerMismatch
	case errors.Is(err, ErrCatalogCapacity):
		return ConnectionErrorCacheCapacity
	case errors.Is(err, ErrUnsupportedProtocol), errors.Is(err, ErrInvalidFrame), errors.Is(err, ErrFrameTooLarge):
		return ConnectionErrorIncompatibleProtocol
	case errors.Is(err, context.DeadlineExceeded):
		return ConnectionErrorTimeout
	case errors.Is(err, ErrSSHHostKeyRequired):
		return ConnectionErrorHostKeyRequired
	case errors.Is(err, ErrSSHAuthUnavailable):
		return ConnectionErrorAuthUnavailable
	case errors.Is(err, ErrRemotePeerUnavailable):
		return ConnectionErrorRemotePeerUnavailable
	default:
		return ConnectionErrorUnavailable
	}
}

func FinishCommand(ctx context.Context, command *exec.Cmd, stdout io.Closer, diagnostics *DiagnosticsBuffer) error {
	if command == nil {
		return errNilCommand
	}

	var result error

	if stdout != nil {
		if err := stdout.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			result = errors.Join(result, ErrConnectionUnavailable, oops.In("remote").With("operation", "finish_command").Wrap(err))
		}
	}

	if command.Process != nil {
		if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			result = errors.Join(result, ErrConnectionUnavailable, oops.In("remote").With("operation", "finish_command").Wrap(err))
		}
	}

	waitErr := command.Wait()

	if ctx.Err() != nil {
		return errors.Join(oops.In("remote").With("operation", "finish_command").Wrap(ctx.Err()), result)
	}

	if waitErr != nil && !errors.Is(waitErr, context.Canceled) && !errors.Is(waitErr, os.ErrProcessDone) {
		result = errors.Join(result, ClassifySSHFailure(nil, diagnostics.String()))
	}

	return result
}

func checkConnection(ctx context.Context, remote config.RemoteConfig, connection config.RemoteConnectionConfig) ConnectionCheck {
	startedAt := time.Now()

	checkContext, cancel := context.WithTimeout(ctx, ParsedDuration(remote.ConnectTimeout, defaultConnect))
	defer cancel()

	command, cmdErr := NewSSHCommand(checkContext, remote, connection, SSHCommandOptions{
		Interactive: false,
		Forward:     nil,
		Command:     nil,
	}, "true")
	if cmdErr != nil {
		cancel()

		return ConnectionCheck{
			ID:       connection.ID,
			Host:     connection.Host,
			Reason:   ConnectionErrorUnavailable,
			Duration: 0,
			Err:      cmdErr,
		}
	}

	diagnostics := NewDiagnosticsBuffer(maxDiagnosticsBytes)

	command.Stdout = diagnostics
	command.Stderr = diagnostics

	runErr := command.Run()

	contextErr := checkContext.Err()

	cancel()

	var classified error

	if runErr != nil {
		classified = ClassifySSHFailure(contextErr, diagnostics.String())
	}

	return ConnectionCheck{
		ID:       connection.ID,
		Host:     connection.Host,
		Reason:   ConnectionErrorCode(classified),
		Duration: time.Since(startedAt),
		Err:      classified,
	}
}
