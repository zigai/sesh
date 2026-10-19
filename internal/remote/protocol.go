//go:build linux || darwin

package remote

import (
	"bufio"
	"bytes"
	"encoding/json/v2"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/work"
)

const (
	ProtocolVersion    = 7
	MaxFrameBytes      = 1 << 20
	MaxSessions        = 4096
	MaxAgents          = 16384
	MaxStringBytes     = 16 << 10
	MaxIdentifierBytes = 256
	MaxActionBytes     = 64 << 10
	MaxResultBytes     = 1 << 20
	MaxActions         = 256
	MaxBootstrapBytes  = 64 << 10
	MaxCapabilityBytes = 4 << 10
	MaxStreamFrames    = 1 << 20
)

const (
	FrameHello    FrameType = "hello"
	FrameSnapshot FrameType = "snapshot"
	FrameHealth   FrameType = "health"
)

const (
	HealthOK                HealthStatus = "ok"
	HealthDegraded          HealthStatus = "degraded"
	HealthUnknown           HealthStatus = "unknown"
	HealthReasonNone        HealthReason = "none"
	HealthReasonUnavailable HealthReason = "unavailable"
	HealthReasonTimeout     HealthReason = "timeout"
	HealthReasonInvalidData HealthReason = "invalid_data"
)

var (
	ErrFrameTooLarge       = errors.New("remote frame exceeds size limit")
	ErrUnsupportedProtocol = errors.New("unsupported remote protocol version")
	ErrInvalidFrame        = errors.New("invalid remote frame")
	ErrPeerMismatch        = errors.New("remote peer identity changed")
	errNilFrameWriter      = errors.New("remote frame writer is not configured")
)

type FrameType string

type HealthStatus string

type HealthReason string

type SourceStatus struct {
	Status    HealthStatus `json:"status"`
	Reason    HealthReason `json:"reason"`
	CheckedAt time.Time    `json:"checkedAt"`
}

type SourceHealth struct {
	Tmux      SourceStatus `json:"tmux"`
	Agents    SourceStatus `json:"agents"`
	Snapshots SourceStatus `json:"snapshots"`
	Tasks     SourceStatus `json:"tasks"`
	Work      SourceStatus `json:"work"`
}

type Hello struct {
	PeerID         string    `json:"peerId"`
	StreamInstance string    `json:"streamInstance"`
	Hostname       string    `json:"hostname"`
	BridgeDir      string    `json:"bridgeDir"`
	StartedAt      time.Time `json:"startedAt"`
}

type Snapshot struct {
	GeneratedAt time.Time         `json:"generatedAt"`
	Sessions    []session.Session `json:"sessions"`
	Tasks       []TaskRow         `json:"tasks"`
	Work        []work.Item       `json:"work"`
	Actions     []action.ID       `json:"actions"`
	Health      SourceHealth      `json:"health"`
}

type Health struct {
	ObservedAt time.Time    `json:"observedAt"`
	Sources    SourceHealth `json:"sources"`
}

type FrameWriter struct {
	writer io.Writer
}

type FrameReader struct {
	reader *bufio.Reader
}

type Frame struct {
	Version        int       `json:"version"`
	Type           FrameType `json:"type"`
	PeerID         string    `json:"peerId"`
	StreamInstance string    `json:"streamInstance"`
	Sequence       uint64    `json:"sequence"`
	Hello          *Hello    `json:"hello,omitempty"`
	Snapshot       *Snapshot `json:"snapshot,omitempty"`
	Health         *Health   `json:"health,omitempty"`
}

func NewFrameWriter(writer io.Writer) *FrameWriter {
	return &FrameWriter{writer: writer}
}

func (w *FrameWriter) Write(frame Frame) error {
	if w == nil || w.writer == nil {
		return errNilFrameWriter
	}

	if err := ValidateFrame(frame); err != nil {
		return err
	}

	encoded, err := json.Marshal(frame)
	if err != nil {
		return oops.In("remote").With("operation", "write_frame").Wrap(err)
	}

	if len(encoded)+1 > MaxFrameBytes {
		return oops.In("remote").With("operation", "write_frame").Wrap(ErrFrameTooLarge)
	}

	encoded = append(encoded, '\n')

	written, err := w.writer.Write(encoded)
	if err != nil {
		return oops.In("remote").With("operation", "write_frame").Wrap(err)
	}

	if written != len(encoded) {
		return oops.In("remote").With("operation", "write_frame").Wrap(io.ErrShortWrite)
	}

	return nil
}

func NewFrameReader(reader io.Reader) *FrameReader {
	return &FrameReader{reader: bufio.NewReaderSize(reader, MaxFrameBytes)}
}

func (r *FrameReader) Read() (Frame, error) {
	if r == nil || r.reader == nil {
		return Frame{}, ErrInvalidFrame
	}

	line, err := r.reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return Frame{}, oops.In("remote").With("operation", "read_frame").Wrap(ErrFrameTooLarge)
	}

	if err != nil && !errors.Is(err, io.EOF) {
		return Frame{}, oops.In("remote").With("operation", "read_frame").Wrap(err)
	}

	if len(line) == 0 && errors.Is(err, io.EOF) {
		return Frame{}, io.EOF
	}

	if len(line) > MaxFrameBytes {
		return Frame{}, oops.In("remote").With("operation", "read_frame").Wrap(ErrFrameTooLarge)
	}

	if errors.Is(err, io.EOF) {
		return Frame{}, ErrInvalidFrame
	}

	line = bytes.TrimSuffix(line, []byte{'\n'})

	frame, decodeErr := decodeStrict[Frame](line)
	if decodeErr != nil {
		return Frame{}, decodeErr
	}

	if validateErr := ValidateFrame(frame); validateErr != nil {
		return Frame{}, validateErr
	}

	return frame, nil
}

func ValidateFrame(frame Frame) error {
	if frame.Version != ProtocolVersion {
		return oops.In("remote").With("operation", "validate_frame").With("version", frame.Version).Wrap(ErrUnsupportedProtocol)
	}

	if !validFrameEnvelope(frame) || framePayloadCount(frame) != 1 {
		return ErrInvalidFrame
	}

	switch frame.Type {
	case FrameHello:
		if !validHelloFrame(frame) {
			return ErrInvalidFrame
		}
	case FrameSnapshot:
		if !validSnapshotFrame(frame) {
			return ErrInvalidFrame
		}
	case FrameHealth:
		if frame.Health == nil || !ValidSourceHealth(frame.Health.Sources) {
			return ErrInvalidFrame
		}
	default:
		return ErrInvalidFrame
	}

	return nil
}

func ValidSession(value session.Session) bool {
	if !validRemoteSession(value) {
		return false
	}

	if value.Kind != session.KindLive && value.Kind != session.KindSaved {
		return false
	}

	if value.Kind == session.KindSaved {
		return len(value.Agents) == 0
	}

	for _, agent := range value.Agents {
		if !validRemoteAgent(agent) {
			return false
		}
	}

	return true
}

func ValidSourceHealth(health SourceHealth) bool {
	return validSourceStatus(health.Tmux) &&
		validSourceStatus(health.Agents) &&
		validSourceStatus(health.Snapshots) &&
		validSourceStatus(health.Tasks) &&
		validSourceStatus(health.Work)
}

func ValidIdentifier(value string) bool {
	return value != "" && len(value) <= MaxIdentifierBytes && validTerminalText(value)
}

func ValidText(value string) bool {
	return len(value) <= MaxStringBytes && validTerminalText(value)
}

func decodeStrict[T any](data []byte) (T, error) {
	var value T

	if err := json.Unmarshal(data, &value, json.RejectUnknownMembers(true)); err != nil {
		var zero T

		return zero, ErrInvalidFrame
	}

	return value, nil
}

func validFrameEnvelope(frame Frame) bool {
	return ValidIdentifier(frame.PeerID) &&
		ValidIdentifier(frame.StreamInstance) &&
		frame.Sequence != 0
}

func framePayloadCount(frame Frame) int {
	payloads := 0

	if frame.Hello != nil {
		payloads++
	}

	if frame.Snapshot != nil {
		payloads++
	}

	if frame.Health != nil {
		payloads++
	}

	return payloads
}

func validHelloFrame(frame Frame) bool {
	return frame.Hello != nil &&
		frame.Hello.PeerID == frame.PeerID &&
		frame.Hello.StreamInstance == frame.StreamInstance &&
		ValidText(frame.Hello.Hostname) &&
		ValidText(frame.Hello.BridgeDir)
}

func validSnapshotFrame(frame Frame) bool {
	if frame.Snapshot == nil || len(frame.Snapshot.Sessions) > MaxSessions {
		return false
	}

	agents := 0

	for _, remoteSession := range frame.Snapshot.Sessions {
		agents += len(remoteSession.Agents)
		if agents > MaxAgents || !ValidSession(remoteSession) {
			return false
		}
	}

	return validSnapshotRows(*frame.Snapshot) && ValidSourceHealth(frame.Snapshot.Health)
}

func validSnapshotRows(snapshot Snapshot) bool {
	return validTaskRows(snapshot.Tasks) && validWorkRows(snapshot.Work) && validActionIDs(snapshot.Actions)
}

func validActionIDs(ids []action.ID) bool {
	if len(ids) > MaxActions {
		return false
	}

	for _, id := range ids {
		if !id.Valid() {
			return false
		}
	}

	return true
}

func validRemoteSession(value session.Session) bool {
	if !ValidIdentifier(value.ID) || !ValidIdentifier(value.Name) {
		return false
	}

	if value.WindowCount < 0 || !validAgentCounts(value) {
		return false
	}

	if len(value.NameRaw) > MaxStringBytes || len(value.PathRaw) > MaxStringBytes {
		return false
	}

	texts := []string{
		value.TmuxServerSocket,
		value.Generation,
		value.RepositorySource,
		value.ActivePaneID,
		value.ActiveWindowName,
		value.ActiveWindowIndex,
		value.ActivePanePath,
		value.ActivePaneCommand,
		value.ActivePaneTitle,
	}

	for _, text := range texts {
		if !ValidText(text) {
			return false
		}
	}

	if value.RuntimeIdentity.TmuxServerPID < 0 {
		return false
	}

	return validAgentTimestamps(value)
}

func validAgentCounts(value session.Session) bool {
	return value.AgentTotal >= 0 &&
		value.AgentActive >= 0 &&
		value.AgentWaiting >= 0 &&
		value.AgentFailed >= 0 &&
		value.AgentInterrupted >= 0 &&
		value.AgentUnknown >= 0
}

func validAgentTimestamps(value session.Session) bool {
	return !negativeSpan(value.AgentIdleSince) &&
		!negativeSpan(value.AgentWaitingSince) &&
		!negativeSpan(value.AgentActivityChangedAt)
}

func negativeSpan(value time.Time) bool {
	return value.Before(time.Time{})
}

func validRemoteAgent(agent session.AgentSession) bool {
	if agent.WindowIndex < 0 || agent.PaneLeft < 0 || agent.PaneTop < 0 || agent.PID < 0 {
		return false
	}

	if agent.GitAhead < 0 || agent.GitBehind < 0 || agent.GitUnmerged < 0 {
		return false
	}

	if negativeSpan(agent.StatusSince) {
		return false
	}

	texts := []string{
		agent.RegistryID,
		agent.Harness,
		agent.HarnessSession,
		agent.SessionID,
		agent.PaneID,
		agent.CWD,
		agent.ProjectRoot,
		agent.GitBranch,
		agent.Worktree,
		agent.GitChanges,
		agent.SessionPath,
		agent.Executable,
		string(agent.Status),
	}

	for _, text := range texts {
		if !ValidText(text) {
			return false
		}
	}

	return validRemoteAgentStatus(agent.Status)
}

func validRemoteAgentStatus(status session.AgentStatus) bool {
	switch status {
	case "", session.AgentStatusRunning, session.AgentStatusWaiting, session.AgentStatusFailed,
		session.AgentStatusInterrupted, session.AgentStatusIdle, session.AgentStatusUnknown:
		return true
	case session.AgentStatusSleeping, session.AgentStatusMerged, session.AgentStatusClosed:
		return false
	default:
		return false
	}
}

func validSourceStatus(status SourceStatus) bool {
	if !ValidText(string(status.Status)) || !ValidText(string(status.Reason)) {
		return false
	}

	switch status.Status {
	case HealthOK, HealthDegraded, HealthUnknown:
	default:
		return false
	}

	switch status.Reason {
	case HealthReasonNone, HealthReasonUnavailable, HealthReasonTimeout, HealthReasonInvalidData:
	default:
		return false
	}

	return (status.Status == HealthOK) == (status.Reason == HealthReasonNone)
}

func validTerminalText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}

	for _, character := range value {
		if character <= '\x1f' || character == '\x7f' || character >= '\x80' && character <= '\x9f' {
			return false
		}
	}

	return true
}
