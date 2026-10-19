//go:build linux || darwin

package remote

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"reflect"
	"slices"
	"time"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/work"
)

const (
	peerIDRandomBytes         = 16
	streamInstanceRandomBytes = 16

	peerIDName    = "peer-id"
	bridgeDirName = "bridge"
	defaultPoll   = time.Second
)

var errNilStreamOutput = errors.New("remote stream output is not configured")

type InventoryOptions struct {
	PeerID            string
	Hostname          string
	BridgeDir         string
	TmuxSocket        string
	PollInterval      time.Duration
	HeartbeatInterval time.Duration
	Sessions          func(ctx context.Context) ([]session.Session, error)
	Agents            func(ctx context.Context) ([]session.AgentSummary, error)
	SavedSessions     func(ctx context.Context) ([]session.Session, error)
	Tasks             func(ctx context.Context) ([]TaskRow, error)
	Work              func(ctx context.Context) ([]work.Item, error)
	Actions           []action.ID
}

type streamPublisher struct {
	writer         *FrameWriter
	peerID         string
	streamInstance string
	sequence       uint64
	lastPublished  *Snapshot
}

type streamState struct {
	options       InventoryOptions
	sessions      []session.Session
	savedSessions []session.Session
	agents        []session.AgentSummary
	tasks         []TaskRow
	work          []work.Item
	health        SourceHealth
}

func PeerStateDir(stateHome string) (string, error) {
	path, err := PeerStatePath(stateHome)
	if err != nil {
		return "", err
	}

	if err := EnsureDir(path); err != nil {
		return "", err
	}

	return path, nil
}

func PeerStatePath(stateHome string) (string, error) {
	if stateHome == "" {
		return "", ErrIncompleteDependencies
	}

	path := joinComponents(stateHome, stateDirComponent, remoteComponent)

	return path, nil
}

func BridgeDir(stateHome string) (string, error) {
	dir, err := PeerStateDir(stateHome)
	if err != nil {
		return "", err
	}

	path := joinComponents(dir, bridgeDirName)
	if err := EnsureDir(path); err != nil {
		return "", err
	}

	return path, nil
}

func LoadOrCreatePeerID(dir *privatefs.Dir) (string, error) {
	peerID, err := LoadPeerID(dir)
	if err == nil {
		return peerID, nil
	}

	if !errors.Is(err, fs.ErrNotExist) {
		return "", oops.In("remote").With("operation", "load_or_create_peer_id").Wrap(err)
	}

	peerID, err = randomHex(peerIDRandomBytes)
	if err != nil {
		return "", err
	}

	file, err := dir.File(peerIDName, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return LoadOrCreatePeerID(dir)
		}

		return "", oops.In("remote").With("operation", "load_or_create_peer_id").Wrap(err)
	}

	_, writeErr := file.WriteString(peerID)
	closeErr := file.Close()

	if writeErr != nil {
		return "", errors.Join(oops.In("remote").With("operation", "load_or_create_peer_id").Wrap(writeErr), closeErr)
	}

	if closeErr != nil {
		return "", oops.In("remote").With("operation", "load_or_create_peer_id").Wrap(closeErr)
	}

	return peerID, nil
}

func LoadPeerID(dir *privatefs.Dir) (string, error) {
	if dir == nil {
		return "", ErrIncompleteDependencies
	}

	content, err := dir.Read(peerIDName, MaxIdentifierBytes)
	if err != nil {
		return "", oops.In("remote").With("operation", "read_peer_id").Wrap(err)
	}

	peerID := string(content)
	if !ValidIdentifier(peerID) {
		return "", oops.In("remote").With("operation", "read_peer_id").Wrap(ErrInvalidFrame)
	}

	return peerID, nil
}

func RunInventoryStream(ctx context.Context, opts InventoryOptions, output io.Writer) error {
	if output == nil {
		return oops.In("remote").With("operation", "inventory_stream").Wrap(errNilStreamOutput)
	}

	if !ValidIdentifier(opts.PeerID) || opts.Sessions == nil {
		return oops.In("remote").With("operation", "inventory_stream").Wrap(ErrIncompleteDependencies)
	}

	streamInstance, err := randomHex(streamInstanceRandomBytes)
	if err != nil {
		return err
	}

	publisher := &streamPublisher{
		writer:         NewFrameWriter(output),
		peerID:         opts.PeerID,
		streamInstance: streamInstance,
		sequence:       1,
		lastPublished:  nil,
	}

	if err := publisher.writeHello(streamHostname(opts.Hostname), opts.BridgeDir); err != nil {
		return err
	}

	state := streamState{
		options:       opts,
		sessions:      nil,
		savedSessions: nil,
		agents:        nil,
		tasks:         nil,
		work:          nil,
		health:        UnavailableHealth(),
	}

	state.scan(ctx)

	if err := publisher.publish(&state, true); err != nil {
		return err
	}

	return runStreamLoop(ctx, opts, &state, publisher)
}

func (publisher *streamPublisher) writeHello(hostname, bridgeDir string) error {
	err := publisher.writer.Write(Frame{
		Version:        ProtocolVersion,
		Type:           FrameHello,
		PeerID:         publisher.peerID,
		StreamInstance: publisher.streamInstance,
		Sequence:       publisher.sequence,
		Hello: &Hello{
			PeerID:         publisher.peerID,
			StreamInstance: publisher.streamInstance,
			Hostname:       hostname,
			BridgeDir:      bridgeDir,
			StartedAt:      time.Now().UTC(),
		},
		Snapshot: nil,
		Health:   nil,
	})
	if err != nil {
		return err
	}

	publisher.sequence++

	return nil
}

func (publisher *streamPublisher) publish(state *streamState, force bool) error {
	snapshot, err := state.snapshot()
	if err != nil {
		return err
	}

	if !force && publisher.lastPublished != nil && snapshotsSemanticallyEqual(*publisher.lastPublished, snapshot) {
		return nil
	}

	if err := publisher.writer.Write(Frame{
		Version:        ProtocolVersion,
		Type:           FrameSnapshot,
		PeerID:         publisher.peerID,
		StreamInstance: publisher.streamInstance,
		Sequence:       publisher.sequence,
		Hello:          nil,
		Snapshot:       &snapshot,
		Health:         nil,
	}); err != nil {
		return err
	}

	publisher.sequence++
	publisher.lastPublished = &snapshot

	return nil
}

func (publisher *streamPublisher) writeHealth(observedAt time.Time, health SourceHealth) error {
	if err := publisher.writer.Write(Frame{
		Version:        ProtocolVersion,
		Type:           FrameHealth,
		PeerID:         publisher.peerID,
		StreamInstance: publisher.streamInstance,
		Sequence:       publisher.sequence,
		Hello:          nil,
		Snapshot:       nil,
		Health: &Health{
			ObservedAt: observedAt.UTC(),
			Sources:    health,
		},
	}); err != nil {
		return err
	}

	publisher.sequence++

	return nil
}

func runStreamLoop(ctx context.Context, opts InventoryOptions, state *streamState, publisher *streamPublisher) error {
	pollTicker := time.NewTicker(positiveDuration(opts.PollInterval, defaultPoll))
	heartbeatTicker := time.NewTicker(positiveDuration(opts.HeartbeatInterval, defaultHeartbeat))

	defer pollTicker.Stop()
	defer heartbeatTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return oops.In("remote").With("operation", "inventory_stream").Wrapf(ctx.Err(), "running remote stream")
		case <-pollTicker.C:
			state.scan(ctx)

			if err := publisher.publish(state, false); err != nil {
				return err
			}
		case observedAt := <-heartbeatTicker.C:
			if err := publisher.writeHealth(observedAt, state.health); err != nil {
				return err
			}
		}
	}
}

func (state *streamState) scan(ctx context.Context) {
	state.scanSessions(ctx)
	state.scanAgents(ctx)
	state.scanSavedSessions(ctx)
	state.scanTasks(ctx)
	state.scanWork(ctx)
}

func (state *streamState) scanTasks(ctx context.Context) {
	if state.options.Tasks == nil {
		return
	}

	rows, err := state.options.Tasks(ctx)
	if err == nil && len(rows) <= MaxTasks {
		state.tasks = rows
	}
}

func (state *streamState) scanWork(ctx context.Context) {
	if state.options.Work == nil {
		return
	}

	items, err := state.options.Work(ctx)
	if err == nil {
		state.work = NewWorkRows(items)
	}
}

func (state *streamState) scanSessions(ctx context.Context) {
	now := time.Now().UTC()

	models, err := state.options.Sessions(ctx)

	state.health.Tmux = sourceStatus(err, now)

	if err == nil {
		state.sessions = cloneSessions(models)
	}
}

func (state *streamState) scanAgents(ctx context.Context) {
	now := time.Now().UTC()

	if state.options.Agents == nil {
		state.health.Agents = SourceStatus{Status: HealthUnknown, Reason: HealthReasonUnavailable, CheckedAt: now}

		return
	}

	summaries, err := state.options.Agents(ctx)

	state.health.Agents = sourceStatus(err, now)

	if err == nil {
		state.agents = summaries
	}
}

func (state *streamState) scanSavedSessions(ctx context.Context) {
	now := time.Now().UTC()

	if state.options.SavedSessions == nil {
		state.health.Snapshots = SourceStatus{Status: HealthUnknown, Reason: HealthReasonUnavailable, CheckedAt: now}

		return
	}

	saved, err := state.options.SavedSessions(ctx)

	state.health.Snapshots = sourceStatus(err, now)

	if err != nil {
		return
	}

	fresh := make([]session.Session, 0, len(saved))

	for _, savedSession := range saved {
		row := savedSession
		row.Kind = session.KindSaved
		row.Agents = []session.AgentSession{}
		row.TmuxServerSocket = state.options.TmuxSocket

		fresh = append(fresh, row)
	}

	state.savedSessions = fresh
}

func (state *streamState) snapshot() (Snapshot, error) {
	if len(state.sessions)+len(state.savedSessions) > MaxSessions {
		return Snapshot{}, oops.In("remote").With("operation", "peer_snapshot").Wrap(ErrFrameTooLarge)
	}

	sessions := cloneSessions(state.sessions)
	mergeAgentSummaries(sessions, state.agents)

	liveNames := make(map[string]struct{}, len(sessions))
	for _, live := range sessions {
		liveNames[live.Name] = struct{}{}
	}

	for _, saved := range state.savedSessions {
		if _, live := liveNames[saved.Name]; live {
			continue
		}

		sessions = append(sessions, saved)
	}

	return Snapshot{
		GeneratedAt: time.Now().UTC(),
		Sessions:    sessions,
		Tasks:       slices.Clone(state.tasks),
		Work:        slices.Clone(state.work),
		Actions:     slices.Clone(state.options.Actions),
		Health:      state.health,
	}, nil
}

func streamHostname(configured string) string {
	if configured != "" {
		return configured
	}

	hostname, err := os.Hostname()
	if err != nil {
		return ""
	}

	return hostname
}

func sourceStatus(err error, checkedAt time.Time) SourceStatus {
	if err == nil {
		return SourceStatus{Status: HealthOK, Reason: HealthReasonNone, CheckedAt: checkedAt}
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return SourceStatus{Status: HealthDegraded, Reason: HealthReasonTimeout, CheckedAt: checkedAt}
	}

	return SourceStatus{Status: HealthDegraded, Reason: HealthReasonUnavailable, CheckedAt: checkedAt}
}

func cloneSessions(source []session.Session) []session.Session {
	result := make([]session.Session, len(source))
	copy(result, source)

	for index := range result {
		result[index].Agents = append([]session.AgentSession(nil), source[index].Agents...)
	}

	return result
}

func mergeAgentSummaries(sessions []session.Session, summaries []session.AgentSummary) {
	byID := make(map[string]session.AgentSummary, len(summaries))
	byName := make(map[string]session.AgentSummary, len(summaries))

	for _, summary := range summaries {
		byID[summary.TmuxServerSocket+"\x00"+summary.TmuxSessionID] = summary
		byName[summary.TmuxServerSocket+"\x00"+summary.TmuxSessionName] = summary
	}

	for index := range sessions {
		summary, ok := byID[sessions[index].TmuxServerSocket+"\x00"+sessions[index].ID]
		if !ok {
			summary, ok = byName[sessions[index].TmuxServerSocket+"\x00"+sessions[index].Name]
		}

		if !ok {
			continue
		}

		sessions[index].AgentTotal = summary.Total
		sessions[index].AgentActive = summary.Active
		sessions[index].AgentWaiting = summary.Waiting
		sessions[index].AgentFailed = summary.Failed
		sessions[index].AgentInterrupted = summary.Interrupted
		sessions[index].AgentUnknown = summary.Unknown
		sessions[index].AgentIdleSince = summary.IdleSince
		sessions[index].AgentWaitingSince = summary.WaitingSince
		sessions[index].AgentActivityChangedAt = summary.ActivityChangedAt
		sessions[index].Agents = append([]session.AgentSession(nil), summary.Agents...)
	}
}

func snapshotsSemanticallyEqual(left, right Snapshot) bool {
	left.GeneratedAt = time.Time{}
	right.GeneratedAt = time.Time{}
	left.Health.Tmux.CheckedAt = time.Time{}
	left.Health.Agents.CheckedAt = time.Time{}
	left.Health.Snapshots.CheckedAt = time.Time{}
	right.Health.Tmux.CheckedAt = time.Time{}
	right.Health.Agents.CheckedAt = time.Time{}
	right.Health.Snapshots.CheckedAt = time.Time{}

	return reflect.DeepEqual(left, right)
}

func positiveDuration(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}

	return fallback
}
