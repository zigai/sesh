//go:build linux || darwin

package remote

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/privatefs"
)

const (
	monitorReconcileInterval = time.Second
	monitorFlushInterval     = 100 * time.Millisecond
	monitorDefaultStale      = 45 * time.Second
	monitorDefaultHide       = 5 * time.Minute
	monitorDefaultParallel   = 4
)

var (
	emptyFrame               Frame
	errStreamAttemptCanceled = errors.New("remote stream attempt canceled")
)

type streamSupervisor struct {
	updates        chan<- workerUpdate
	command        func(ctx context.Context, executable string, arguments ...string) *exec.Cmd
	workers        map[string]streamWorkerHandle
	limiter        chan struct{}
	limiterSize    int
	nextGeneration uint64
	wait           sync.WaitGroup
	stopOnce       sync.Once
}

type streamWorkerHandle struct {
	cancel      context.CancelFunc
	done        <-chan struct{}
	fingerprint string
	generation  uint64
}

type MonitorOptions struct {
	CatalogDir *privatefs.Dir
	Remote     config.RemoteConfig
	Command    func(ctx context.Context, executable string, arguments ...string) *exec.Cmd
	Now        func() time.Time
	Reloads    <-chan config.RemoteConfig
}

type workerUpdate struct {
	connectionID string
	generation   uint64
	frame        Frame
	err          error
	attempted    bool
	result       chan<- error
}

type monitorState struct {
	dir               *privatefs.Dir
	now               func() time.Time
	catalog           Catalog
	configured        map[string]ConnectionState
	hidden            map[string]ConnectionState
	activeGenerations map[string]uint64
	staleAfter        time.Duration
	hideAfter         time.Duration
	dirty             bool
	configApplied     bool
}

func RunMonitor(ctx context.Context, options MonitorOptions) (resultErr error) {
	if options.CatalogDir == nil {
		return ErrIncompleteDependencies
	}

	if err := config.ValidateRemoteConfig(options.Remote); err != nil {
		return oops.In("remote").With("operation", "run_monitor").Wrapf(err, "validating remote monitor configuration")
	}

	ownerLock, err := AcquireCatalogOwner(options.CatalogDir)
	if err != nil {
		return err
	}

	defer func() {
		resultErr = errors.Join(resultErr, ownerLock.Close())
	}()

	catalog, loadErr := LoadCatalog(options.CatalogDir)

	recovered := loadErr != nil
	if recovered {
		catalog = EmptyCatalog()
	}

	now := options.Now
	if now == nil {
		now = time.Now
	}

	updates := make(chan workerUpdate)

	supervisor := newStreamSupervisor(updates, options.Command)

	defer supervisor.stop()

	state := &monitorState{
		dir:               options.CatalogDir,
		now:               now,
		catalog:           catalog,
		configured:        make(map[string]ConnectionState),
		hidden:            make(map[string]ConnectionState),
		activeGenerations: make(map[string]uint64),
		staleAfter:        0,
		hideAfter:         0,
		dirty:             recovered,
		configApplied:     false,
	}

	state.applyConfig(ctx, options.Remote, supervisor)

	if err := state.flush(); err != nil {
		return err
	}

	return state.run(ctx, options, updates, supervisor)
}

func (state *monitorState) applyConfig(ctx context.Context, remote config.RemoteConfig, supervisor *streamSupervisor) {
	connections := remote.Connections
	if !remote.Enabled {
		connections = nil
	}

	changed := state.reconcileConfigured(connections)

	state.staleAfter = ParsedDuration(remote.StaleAfter, monitorDefaultStale)
	state.hideAfter = ParsedDuration(remote.HideAfter, monitorDefaultHide)

	changed = state.reconcileHidden(connections) || changed

	state.activeGenerations = supervisor.apply(ctx, remote, connections)
	state.dirty = state.dirty || changed

	if changed || !state.configApplied {
		state.configApplied = true

		zerolog.Ctx(ctx).Info().
			Str("component", "remote_monitor").
			Str("operation", "apply_config").
			Str("status", "ok").
			Int("connection_count", len(connections)).
			Msg("remote monitor configuration applied")
	}
}

func (state *monitorState) reconcileConfigured(connections []config.RemoteConnectionConfig) bool {
	desired := make(map[string]struct{}, len(connections))

	for _, connection := range connections {
		desired[connection.ID] = struct{}{}
	}

	changed := false

	for id := range state.catalog.Connections {
		if _, exists := desired[id]; exists {
			continue
		}

		delete(state.catalog.Connections, id)

		changed = true
	}

	configured := make(map[string]ConnectionState, len(connections))

	for _, connection := range connections {
		current, connectionChanged := configuredConnectionState(state.catalog.Connections[connection.ID], connection)

		if !CatalogAdmits(state.catalog, connection.ID, current) {
			current.Label = ""
			current.Connected = false
			current.LastError = ConnectionErrorCacheCapacity

			if !CatalogAdmits(state.catalog, connection.ID, current) {
				current = newConnectionState(connection, ConnectionDigest(connection))
				current.LastError = ConnectionErrorCacheCapacity
			}

			if !CatalogAdmits(state.catalog, connection.ID, current) {
				configured[connection.ID] = current

				delete(state.catalog.Connections, connection.ID)

				changed = true

				continue
			}

			connectionChanged = true
		}

		configured[connection.ID] = current
		state.catalog.Connections[connection.ID] = current
		changed = changed || connectionChanged
	}

	state.configured = configured

	return changed
}

func (state *monitorState) reconcileHidden(connections []config.RemoteConnectionConfig) bool {
	byID := make(map[string]config.RemoteConnectionConfig, len(connections))

	for _, connection := range connections {
		byID[connection.ID] = connection
	}

	changed := false

	for id, hiddenState := range state.hidden {
		connection, exists := byID[id]
		if !exists {
			delete(state.hidden, id)

			continue
		}

		reconciled, _ := configuredConnectionState(hiddenState, connection)
		if reconciled.ConfigDigest != hiddenState.ConfigDigest {
			delete(state.hidden, id)

			continue
		}

		state.hidden[id] = reconciled
		state.configured[id] = reconciled

		if _, visible := state.catalog.Connections[id]; visible {
			delete(state.catalog.Connections, id)

			changed = true
		}
	}

	return changed
}

func (state *monitorState) run(
	ctx context.Context,
	options MonitorOptions,
	updates <-chan workerUpdate,
	supervisor *streamSupervisor,
) error {
	reconcileTicker := time.NewTicker(monitorReconcileInterval)
	writeTicker := time.NewTicker(monitorFlushInterval)

	defer reconcileTicker.Stop()
	defer writeTicker.Stop()

	reloadChannel := options.Reloads
	reloadFailed := false

	for {
		select {
		case <-ctx.Done():
			supervisor.stop()

			if err := state.flush(); err != nil {
				return err
			}

			return oops.In("remote").With("operation", "run_monitor").Wrapf(ctx.Err(), "remote monitor canceled")
		case update := <-updates:
			state.handleUpdate(ctx, update)
		case reload, ok := <-reloadChannel:
			if !ok {
				reloadChannel = nil

				continue
			}

			if err := config.ValidateRemoteConfig(reload); err != nil {
				if !reloadFailed {
					zerolog.Ctx(ctx).Warn().
						Str("component", "remote_monitor").
						Str("operation", "reload_config").
						Str("status", "degraded").
						Str("reason", "invalid_configuration").
						Msg("remote monitor configuration reload rejected")
				}

				reloadFailed = true

				continue
			}

			reloadFailed = false

			state.applyConfig(ctx, reload, supervisor)
		case <-writeTicker.C:
			if err := state.flush(); err != nil {
				return err
			}
		case <-reconcileTicker.C:
			state.reconcile(ctx)
		}
	}
}

func (state *monitorState) flush() error {
	if !state.dirty {
		return nil
	}

	if err := WriteCatalog(state.dir, state.catalog); err != nil {
		return err
	}

	state.dirty = false

	return nil
}

func (state *monitorState) handleUpdate(ctx context.Context, update workerUpdate) {
	if state.activeGenerations[update.connectionID] != update.generation {
		if update.result != nil {
			update.result <- ErrConnectionChanged
		}

		return
	}

	current, hidden := state.connectionState(update.connectionID)

	if update.attempted {
		state.handleAttempt(update.connectionID, current, hidden)

		return
	}

	if update.err != nil {
		state.handleConnectionError(ctx, update.connectionID, current, hidden, update.err)

		return
	}

	previous := current

	acceptErr := acceptFrame(&current, update.frame, state.now().UTC())

	if acceptErr == nil && !CatalogAdmits(state.catalog, update.connectionID, current) {
		acceptErr = ErrCatalogCapacity
		state.handleConnectionError(ctx, update.connectionID, previous, hidden, acceptErr)
	}

	state.storeAcceptedFrame(ctx, update.connectionID, current, hidden, update.frame.Type, acceptErr)

	if update.result != nil {
		update.result <- acceptErr
	}
}

func (state *monitorState) connectionState(connectionID string) (ConnectionState, bool) {
	current, visible := state.catalog.Connections[connectionID]

	hiddenState, hidden := state.hidden[connectionID]
	if !visible && hidden {
		return hiddenState, true
	}

	if !visible {
		return state.configured[connectionID], false
	}

	return current, hidden
}

func (state *monitorState) handleAttempt(connectionID string, current ConnectionState, hidden bool) {
	if hidden {
		return
	}

	current.LastAttempt = state.now().UTC()

	if !CatalogAdmits(state.catalog, connectionID, current) {
		return
	}

	state.catalog.Connections[connectionID] = current
	state.dirty = true
}

func (state *monitorState) handleConnectionError(ctx context.Context, connectionID string, current ConnectionState, hidden bool, connectionErr error) {
	if hidden {
		return
	}

	previous := current
	current.Connected = false
	current.LastError = ConnectionErrorCode(connectionErr)

	if current.LastErrorAt.IsZero() {
		current.LastErrorAt = state.now().UTC()
	}

	if previous.Connected {
		zerolog.Ctx(ctx).Warn().
			Str("component", "remote_monitor").
			Str("operation", "connection").
			Str("status", "degraded").
			Str("error_code", current.LastError).
			Msg("remote monitor connection degraded")
	}

	if !CatalogAdmits(state.catalog, connectionID, current) {
		state.configured[connectionID] = current

		return
	}

	state.catalog.Connections[connectionID] = current
	state.dirty = true
}

func (state *monitorState) storeAcceptedFrame(ctx context.Context, connectionID string, current ConnectionState, hidden bool, frameType FrameType, acceptErr error) {
	previous, _ := state.connectionState(connectionID)

	switch {
	case acceptErr == nil && frameType == FrameSnapshot:
		current.LastError = ""
		current.LastErrorAt = time.Time{}
		state.catalog.Connections[connectionID] = current
		delete(state.hidden, connectionID)

		state.dirty = true

		if !previous.Connected {
			zerolog.Ctx(ctx).Info().
				Str("component", "remote_monitor").
				Str("operation", "connection").
				Str("status", "recovered").
				Str("reason", "connection_available").
				Msg("remote monitor connection recovered")
		}
	case hidden && acceptErr == nil:
		state.hidden[connectionID] = current
	case acceptErr == nil:
		state.catalog.Connections[connectionID] = current
		state.dirty = true
	}
}

func (state *monitorState) reconcile(ctx context.Context) {
	changed, removed := ReconcileCatalog(&state.catalog, state.now().UTC(), state.staleAfter, state.hideAfter)
	if !changed {
		return
	}

	if len(removed) > 0 {
		zerolog.Ctx(ctx).Warn().
			Str("component", "remote_monitor").
			Str("operation", "reconcile").
			Str("status", "degraded").
			Str("reason", "connections_stale").
			Int("stale_count", len(removed)).
			Msg("remote monitor connections stale")
	}

	maps.Copy(state.hidden, removed)

	state.dirty = true
}

func newConnectionState(connection config.RemoteConnectionConfig, digest string) ConnectionState {
	return ConnectionState{
		ID:             connection.ID,
		Label:          connection.Label,
		ConfigDigest:   digest,
		PeerID:         "",
		Hostname:       "",
		BridgeDir:      "",
		StreamInstance: "",
		Sequence:       0,
		Connected:      false,
		LastSeen:       time.Time{},
		LastError:      "",
		LastAttempt:    time.Time{},
		LastErrorAt:    time.Time{},
		Snapshot:       Snapshot{GeneratedAt: time.Time{}, Sessions: nil, Actions: nil, Health: UnavailableHealth()},
	}
}

func configuredConnectionState(current ConnectionState, connection config.RemoteConnectionConfig) (ConnectionState, bool) {
	original := current
	current.ID = connection.ID
	current.Label = connection.Label

	digest := ConnectionDigest(connection)

	if current.ConfigDigest != "" && current.ConfigDigest != digest {
		current = newConnectionState(connection, digest)
	} else {
		current.ConfigDigest = digest

		if !ValidSourceHealth(current.Snapshot.Health) {
			current.Snapshot.Health = UnavailableHealth()
		}
	}

	changed := original.ID == "" ||
		original.Label != current.Label ||
		original.ConfigDigest != current.ConfigDigest ||
		!ValidSourceHealth(original.Snapshot.Health)

	return current, changed
}

func acceptFrame(state *ConnectionState, frame Frame, observedAt time.Time) error {
	if frame.Type == FrameHello {
		if state.PeerID != "" && state.PeerID != frame.PeerID {
			return ErrPeerMismatch
		}

		if state.StreamInstance == frame.StreamInstance && state.Sequence != 0 {
			return ErrInvalidFrame
		}

		state.PeerID = frame.PeerID
		state.Hostname = frame.Hello.Hostname
		state.BridgeDir = frame.Hello.BridgeDir
		state.StreamInstance = frame.StreamInstance
		state.Sequence = frame.Sequence
		state.Connected = true
		state.LastSeen = observedAt

		return nil
	}

	if state.StreamInstance != frame.StreamInstance || state.PeerID != frame.PeerID || frame.Sequence <= state.Sequence {
		return ErrInvalidFrame
	}

	state.Sequence = frame.Sequence
	state.Connected = true
	state.LastSeen = observedAt

	if frame.Snapshot != nil {
		state.Snapshot = *frame.Snapshot
	}

	if frame.Health != nil {
		state.Snapshot.Health = frame.Health.Sources
	}

	return nil
}

func newStreamSupervisor(updates chan<- workerUpdate, command func(ctx context.Context, executable string, arguments ...string) *exec.Cmd) *streamSupervisor {
	return &streamSupervisor{
		updates:        updates,
		command:        command,
		workers:        make(map[string]streamWorkerHandle),
		limiter:        nil,
		limiterSize:    0,
		nextGeneration: 0,
		wait:           sync.WaitGroup{},
		stopOnce:       sync.Once{},
	}
}

func (supervisor *streamSupervisor) apply(ctx context.Context, remote config.RemoteConfig, connections []config.RemoteConnectionConfig) map[string]uint64 {
	limiterSize := min(
		max(remote.MaxParallelConnects, 1),
		max(len(connections), 1),
		config.MaxRemoteConnections,
	)

	waitFor := make(map[string][]<-chan struct{})

	if supervisor.limiter == nil || supervisor.limiterSize != limiterSize {
		allDone := make([]<-chan struct{}, 0, len(supervisor.workers))

		for _, worker := range supervisor.workers {
			worker.cancel()
			allDone = append(allDone, worker.done)
		}

		for _, connection := range connections {
			waitFor[connection.ID] = allDone
		}

		supervisor.workers = make(map[string]streamWorkerHandle)
		supervisor.limiter = make(chan struct{}, limiterSize)
		supervisor.limiterSize = limiterSize
	}

	configured := make(map[string]config.RemoteConnectionConfig, len(connections))

	for _, connection := range connections {
		configured[connection.ID] = connection
	}

	for id, worker := range supervisor.workers {
		connection, exists := configured[id]

		if exists && worker.fingerprint == workerFingerprint(remote, connection) {
			continue
		}

		worker.cancel()

		waitFor[id] = append(waitFor[id], worker.done)

		delete(supervisor.workers, id)
	}

	for _, connection := range connections {
		if _, exists := supervisor.workers[connection.ID]; exists {
			continue
		}

		supervisor.workers[connection.ID] = supervisor.start(ctx, remote, connection, waitFor[connection.ID])
	}

	generations := make(map[string]uint64, len(supervisor.workers))

	for id, worker := range supervisor.workers {
		generations[id] = worker.generation
	}

	return generations
}

func (supervisor *streamSupervisor) start(ctx context.Context, remote config.RemoteConfig, connection config.RemoteConnectionConfig, waitFor []<-chan struct{}) streamWorkerHandle {
	supervisor.nextGeneration++

	workerContext, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	handle := streamWorkerHandle{
		cancel:      cancel,
		done:        done,
		fingerprint: workerFingerprint(remote, connection),
		generation:  supervisor.nextGeneration,
	}

	limiter := supervisor.limiter
	generation := handle.generation

	supervisor.wait.Go(func() {
		defer close(done)

		for _, priorDone := range waitFor {
			select {
			case <-priorDone:
			case <-workerContext.Done():
				return
			}
		}

		runStreamWorker(workerContext, remote, connection, supervisor.command, limiter, generation, supervisor.updates)
	})

	return handle
}

func (supervisor *streamSupervisor) stop() {
	supervisor.stopOnce.Do(func() {
		for _, worker := range supervisor.workers {
			worker.cancel()
		}

		supervisor.wait.Wait()
	})
}

func runStreamWorker(
	ctx context.Context,
	remote config.RemoteConfig,
	connection config.RemoteConnectionConfig,
	command func(ctx context.Context, executable string, arguments ...string) *exec.Cmd,
	limiter chan struct{},
	generation uint64,
	updates chan<- workerUpdate,
) {
	var (
		attemptContext atomic.Value
		cancelAttempt  context.CancelCauseFunc
		release        func()
	)

	attemptContext.Store(ctx)

	defer func() {
		if cancelAttempt != nil {
			cancelAttempt(context.Canceled)
		}

		if release != nil {
			release()
		}
	}()

	_ = Stream(ctx, StreamOptions{
		Remote:       remote,
		Connection:   connection,
		Bootstrap:    StreamBootstrapFor(remote, connection),
		Command:      command,
		HelloTimeout: ParsedDuration(remote.ConnectTimeout, defaultConnect),
		FrameTimeout: ParsedDuration(remote.StaleAfter, streamFrameGrace),
		Yield: func(frame Frame, streamErr error, attempted bool) error {
			if attempted {
				if !acquireWorkerSlot(ctx, limiter) {
					return oops.In("remote").With("operation", "acquire_worker_slot").Wrap(ctx.Err())
				}

				attemptCtx, cancel := context.WithCancelCause(ctx)
				attemptContext.Store(attemptCtx)

				cancelAttempt = cancel

				var releaseOnce sync.Once

				release = func() {
					releaseOnce.Do(func() { <-limiter })
				}

				if !sendWorkerUpdate(ctx, updates, workerUpdate{
					connectionID: connection.ID,
					generation:   generation,
					frame:        emptyFrame,
					err:          nil,
					attempted:    true,
					result:       nil,
				}) {
					release()
					cancel(ctx.Err())

					return oops.In("remote").With("operation", "send_worker_update").Wrap(ctx.Err())
				}

				return nil
			}

			if streamErr != nil {
				return handleStreamError(ctx, updates, connection.ID, generation, streamErr, cancelAttempt, release)
			}

			var attemptDone <-chan struct{}
			if actx, ok := attemptContext.Load().(context.Context); ok {
				attemptDone = actx.Done()
			}

			return forwardFrame(ctx, attemptDone, updates, workerUpdate{
				connectionID: connection.ID,
				generation:   generation,
				frame:        frame,
				err:          nil,
				attempted:    false,
				result:       nil,
			}, release)
		},
	})
}

func forwardFrame(ctx context.Context, attemptDone <-chan struct{}, updates chan<- workerUpdate, update workerUpdate, release func()) error {
	result := make(chan error, 1)
	update.result = result

	if !sendWorkerUpdate(ctx, updates, update) {
		release()

		return oops.In("remote").With("operation", "send_stream_frame").Wrap(ctx.Err())
	}

	select {
	case acceptErr := <-result:
		if acceptErr == nil && update.frame.Type == FrameHello {
			release()
		}

		return acceptErr
	case <-attemptDone:
		release()

		return errStreamAttemptCanceled
	case <-ctx.Done():
		release()

		return oops.In("remote").With("operation", "accept_stream_frame").Wrap(ctx.Err())
	}
}

func handleStreamError(
	ctx context.Context,
	updates chan<- workerUpdate,
	connectionID string,
	generation uint64,
	streamErr error,
	cancelAttempt context.CancelCauseFunc,
	release func(),
) error {
	if cancelAttempt != nil {
		cancelAttempt(streamErr)
	}

	if release != nil {
		release()
	}

	if !sendWorkerUpdate(ctx, updates, workerUpdate{
		connectionID: connectionID,
		generation:   generation,
		frame:        emptyFrame,
		err:          streamErr,
		attempted:    false,
		result:       nil,
	}) {
		return oops.In("remote").With("operation", "send_stream_error").Wrap(ctx.Err())
	}

	return nil
}

func acquireWorkerSlot(ctx context.Context, limiter chan struct{}) bool {
	select {
	case limiter <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func sendWorkerUpdate(ctx context.Context, updates chan<- workerUpdate, update workerUpdate) bool {
	select {
	case updates <- update:
		return true
	case <-ctx.Done():
		return false
	}
}

func workerFingerprint(remote config.RemoteConfig, connection config.RemoteConnectionConfig) string {
	const fingerprintCapacity = 256

	encoded := make([]byte, 0, fingerprintCapacity)

	values := []string{
		ConnectionDigest(connection),
		remote.SSHPath,
		remote.TmuxPollInterval,
		remote.ConnectTimeout,
		remote.HeartbeatInterval,
		remote.StaleAfter,
	}

	for _, value := range values {
		encodedValue, err := json.Marshal(value)
		if err != nil {
			panic(fmt.Sprintf("marshal string for connection fingerprint: %v", err))
		}

		encoded = append(encoded, encodedValue...)
		encoded = append(encoded, 0)
	}

	return string(encoded)
}
