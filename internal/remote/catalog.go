//go:build linux || darwin

package remote

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/samber/oops"

	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/session"
)

const (
	catalogName       = "catalog.json"
	catalogLockName   = "catalog.lock"
	stateDirComponent = "sesh"
	remoteComponent   = "remote"
)

const (
	catalogReserveBytes   = 64 << 10
	maxCatalogBytes       = MaxFrameBytes * 4
	CatalogAdmissionBytes = maxCatalogBytes - catalogReserveBytes
	minWatchInterval      = 50 * time.Millisecond
	catalogReadAttempts   = 4
	catalogReadBackoff    = time.Millisecond
)

var (
	ErrCatalogCapacity        = errors.New("remote catalog capacity exceeded")
	ErrCatalogOwnerRunning    = errors.New("remote catalog monitor is already running")
	ErrIncompleteDependencies = errors.New("remote dependencies are incomplete")
)

type catalogWatchState struct {
	dir         *privatefs.Dir
	yield       func(Catalog, error) error
	previous    []byte
	initialized bool
	failed      bool
	readFailed  bool
}

type ConnectionState struct {
	ID             string    `json:"id"`
	Label          string    `json:"label"`
	ConfigDigest   string    `json:"configDigest"`
	PeerID         string    `json:"peerId"`
	Hostname       string    `json:"hostname"`
	BridgeDir      string    `json:"bridgeDir"`
	StreamInstance string    `json:"streamInstance"`
	Sequence       uint64    `json:"sequence"`
	Connected      bool      `json:"connected"`
	LastSeen       time.Time `json:"lastSeen"`
	LastError      string    `json:"lastErrorCode"`
	LastAttempt    time.Time `json:"lastAttempt"`
	LastErrorAt    time.Time `json:"lastErrorAt"`
	Snapshot       Snapshot  `json:"snapshot"`
}

type Catalog struct {
	Version     int                        `json:"version"`
	GeneratedAt time.Time                  `json:"generatedAt"`
	Connections map[string]ConnectionState `json:"connections"`
}

func RemoteDir(cacheHome string) (string, error) {
	if cacheHome == "" {
		return "", ErrIncompleteDependencies
	}

	path := joinComponents(cacheHome, stateDirComponent, remoteComponent)
	if err := EnsureDir(path); err != nil {
		return "", err
	}

	return path, nil
}

func CatalogPath(cacheHome string) (string, error) {
	dir, err := RemoteDir(cacheHome)
	if err != nil {
		return "", err
	}

	return joinComponents(dir, catalogName), nil
}

func EmptyCatalog() Catalog {
	return Catalog{Version: ProtocolVersion, GeneratedAt: time.Time{}, Connections: make(map[string]ConnectionState)}
}

func LoadCatalog(dir *privatefs.Dir) (Catalog, error) {
	content, err := readCatalogBytes(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return EmptyCatalog(), nil
	}

	if err != nil {
		return Catalog{}, oops.In("remote").With("operation", "load_catalog").Wrap(err)
	}

	return decodeCatalog(content)
}

func WriteCatalog(dir *privatefs.Dir, catalog Catalog) error {
	catalog.Version = ProtocolVersion
	catalog.GeneratedAt = time.Now().UTC()

	if catalog.Connections == nil {
		catalog.Connections = make(map[string]ConnectionState)
	}

	encoded, err := json.Marshal(catalog)
	if err != nil {
		return oops.In("remote").With("operation", "write_catalog").Wrap(err)
	}

	if len(encoded) > maxCatalogBytes {
		return oops.In("remote").With("operation", "write_catalog").Wrap(ErrFrameTooLarge)
	}

	if err := writeFileAtomic(dir, catalogName, encoded); err != nil {
		return oops.In("remote").With("operation", "write_catalog").Wrap(err)
	}

	return nil
}

func AcquireCatalogOwner(dir *privatefs.Dir) (*os.File, error) {
	lock, err := dir.File(catalogLockName, unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return nil, oops.In("remote").With("operation", "acquire_catalog_owner").Wrap(err)
	}

	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, errors.Join(oops.In("remote").With("operation", "acquire_catalog_owner").Wrap(errors.Join(ErrCatalogOwnerRunning, err)), lock.Close())
	}

	return lock, nil
}

func WatchCatalog(ctx context.Context, dir *privatefs.Dir, interval time.Duration, yield func(Catalog, error) error) error {
	if yield == nil {
		return oops.In("remote").With("operation", "watch_catalog").Wrap(ErrIncompleteDependencies)
	}

	ticker := time.NewTicker(max(interval, minWatchInterval))
	defer ticker.Stop()

	state := catalogWatchState{
		dir:         dir,
		yield:       yield,
		previous:    nil,
		initialized: false,
		failed:      false,
		readFailed:  false,
	}

	if err := state.deliver(); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return oops.In("remote").With("operation", "watch_catalog").Wrap(ctx.Err())
		case <-ticker.C:
			if err := state.deliver(); err != nil {
				return err
			}
		}
	}
}

func CatalogAdmits(catalog Catalog, connectionID string, candidate ConnectionState) bool {
	connections := make(map[string]ConnectionState, len(catalog.Connections)+1)

	maps.Copy(connections, catalog.Connections)

	connections[connectionID] = candidate

	encoded, err := json.Marshal(Catalog{
		Version:     ProtocolVersion,
		GeneratedAt: catalog.GeneratedAt,
		Connections: connections,
	})

	return err == nil && len(encoded) <= CatalogAdmissionBytes
}

func ReconcileCatalog(catalog *Catalog, now time.Time, staleAfter, hideAfter time.Duration) (bool, map[string]ConnectionState) {
	if catalog == nil {
		return false, nil
	}

	changed := false

	var hidden map[string]ConnectionState

	for id, state := range catalog.Connections {
		if state.Connected && now.Sub(state.LastSeen) > staleAfter {
			state.Connected = false
			state.LastError = ConnectionErrorStale

			if state.LastErrorAt.IsZero() {
				state.LastErrorAt = now
			}

			catalog.Connections[id] = state
			changed = true
		}

		hideSince := state.LastSeen
		if hideSince.IsZero() {
			hideSince = state.LastErrorAt
		}

		if hideSince.IsZero() || now.Sub(hideSince) <= hideAfter {
			continue
		}

		if hidden == nil {
			hidden = make(map[string]ConnectionState)
		}

		hidden[id] = state

		delete(catalog.Connections, id)

		changed = true
	}

	return changed, hidden
}

func UnavailableHealth() SourceHealth {
	unavailable := SourceStatus{Status: HealthUnknown, Reason: HealthReasonUnavailable, CheckedAt: time.Time{}}

	return SourceHealth{Tmux: unavailable, Agents: unavailable, Snapshots: unavailable}
}

func NormalizeHealth(health SourceHealth) SourceHealth {
	if health.Snapshots == (SourceStatus{Status: "", Reason: "", CheckedAt: time.Time{}}) {
		health.Snapshots = SourceStatus{Status: HealthUnknown, Reason: HealthReasonUnavailable, CheckedAt: time.Time{}}
	}

	return health
}

func CatalogSessions(state ConnectionState) []session.Session {
	result := make([]session.Session, 0, len(state.Snapshot.Sessions))

	for _, row := range state.Snapshot.Sessions {
		row.Origin = session.OriginSSH
		row.IsCurrent = false
		row.RemoteStatus = state.remoteStatus()
		row.RuntimeIdentity.ConnectionID = state.ID
		row.RuntimeIdentity.ConnectionLabel = state.Label
		row.RuntimeIdentity.ConfigDigest = state.ConfigDigest
		row.RuntimeIdentity.PeerID = state.PeerID
		row.RuntimeIdentity.StreamInstance = state.StreamInstance
		row.RuntimeIdentity.TmuxServerSocket = row.TmuxServerSocket
		row.Agents = slices.Clone(row.Agents)

		result = append(result, row)
	}

	return result
}

func (state ConnectionState) remoteStatus() session.RemoteStatus {
	if state.Connected {
		return session.RemoteStatusAvailable
	}

	switch state.LastError {
	case ConnectionErrorStale:
		return session.RemoteStatusStale
	case ConnectionErrorIncompatibleProtocol:
		return session.RemoteStatusIncompatible
	case "":
		return session.RemoteStatusConnecting
	default:
		return session.RemoteStatusUnavailable
	}
}

func decodeCatalog(content []byte) (Catalog, error) {
	if len(content) == 0 || len(content) > maxCatalogBytes {
		return Catalog{}, oops.In("remote").With("operation", "validate_catalog").Wrap(ErrInvalidFrame)
	}

	catalog, err := decodeStrict[Catalog](bytes.TrimSpace(content))
	if err != nil {
		return Catalog{}, oops.In("remote").With("operation", "validate_catalog").Wrap(err)
	}

	if catalog.Version != ProtocolVersion || catalog.Connections == nil {
		return Catalog{}, oops.In("remote").With("operation", "validate_catalog").With("version", catalog.Version).Wrap(ErrUnsupportedProtocol)
	}

	for id, state := range catalog.Connections {
		state.Snapshot.Health = NormalizeHealth(state.Snapshot.Health)
		catalog.Connections[id] = state
	}

	for id, state := range catalog.Connections {
		if !validCatalogConnection(id, state) {
			return Catalog{}, oops.In("remote").With("operation", "validate_catalog").Wrap(ErrInvalidFrame)
		}
	}

	return catalog, nil
}

func validCatalogConnection(id string, state ConnectionState) bool {
	if id != state.ID || !ValidIdentifier(id) || !ValidText(state.Label) ||
		!ValidIdentifier(state.ConfigDigest) || !ValidText(state.PeerID) ||
		!ValidText(state.StreamInstance) || !validConnectionErrorCode(state.LastError) ||
		len(state.Snapshot.Sessions) > MaxSessions || !ValidSourceHealth(state.Snapshot.Health) {
		return false
	}

	return validCatalogSessionAgents(state.Snapshot.Sessions)
}

func validCatalogSessionAgents(sessions []session.Session) bool {
	agents := 0

	for _, remoteSession := range sessions {
		agents += len(remoteSession.Agents)
		if agents > MaxAgents || !ValidSession(remoteSession) {
			return false
		}
	}

	return true
}

func validConnectionErrorCode(code string) bool {
	if code == "" {
		return true
	}

	return slices.Contains(connectionErrorCodes, code)
}

func (state *catalogWatchState) deliver() error {
	content, err := readCatalogBytes(state.dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		if state.failed {
			return nil
		}

		state.failed = true
		state.readFailed = true

		return state.yield(Catalog{Version: 0, GeneratedAt: time.Time{}, Connections: nil},
			oops.In("remote").With("operation", "watch_catalog").Wrap(err))
	}

	if errors.Is(err, fs.ErrNotExist) {
		content = nil
	}

	// A reported read failure replaced the consumer's catalog, so the first successful
	// read must be delivered even when the catalog bytes are unchanged.
	recovered := state.readFailed
	state.readFailed = false

	if state.initialized && !recovered && bytes.Equal(state.previous, content) {
		return nil
	}

	state.initialized = true
	state.previous = append(state.previous[:0], content...)

	if len(content) == 0 {
		state.failed = false

		return state.yield(EmptyCatalog(), nil)
	}

	catalog, decodeErr := decodeCatalog(content)

	state.failed = decodeErr != nil

	return state.yield(catalog, decodeErr)
}

func readCatalogBytes(dir *privatefs.Dir) ([]byte, error) {
	var lastErr error

	for attempt := range catalogReadAttempts {
		content, err := dir.Read(catalogName, maxCatalogBytes)
		if err == nil {
			return content, nil
		}

		if errors.Is(err, fs.ErrNotExist) {
			return nil, oops.In("remote").With("operation", "read_catalog_bytes").Wrap(err)
		}

		if !errors.Is(err, privatefs.ErrUnsafe) {
			return nil, oops.In("remote").With("operation", "read_catalog_bytes").Wrap(err)
		}

		lastErr = err

		if attempt+1 < catalogReadAttempts {
			time.Sleep(catalogReadBackoff)
		}
	}

	return nil, oops.In("remote").With("operation", "read_catalog_bytes").Wrap(lastErr)
}

func joinComponents(components ...string) string {
	return filepath.Join(components...)
}
