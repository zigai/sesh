package tui

import (
	"slices"
	"time"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/task"
)

const (
	RemoteHealthOK       = "ok"
	RemoteHealthDegraded = "degraded"
	RemoteHealthUnknown  = "unknown"
)

const (
	RemoteErrorIncompatibleProtocol = "incompatible_protocol"
	RemoteErrorPeerMismatch         = "peer_mismatch"
	RemoteErrorStale                = "stale"
)

type RemoteConnection struct {
	ID             string
	Label          string
	ConfigDigest   string
	PeerID         string
	StreamInstance string
	Connected      bool
	LastSeen       time.Time
	LastAttempt    time.Time
	LastError      string
	LastErrorAt    time.Time

	HealthTmux      string
	HealthAgents    string
	HealthSnapshots string

	Sessions []session.Session
	Tasks    []task.Live
}

type RemoteCatalog struct {
	GeneratedAt time.Time
	Connections []RemoteConnection
}

func remoteSessions(connection RemoteConnection) []session.Session {
	result := make([]session.Session, 0, len(connection.Sessions))

	for _, row := range connection.Sessions {
		row.Origin = session.OriginSSH
		row.Current = false
		row.RemoteStatus = session.RemoteStatusAvailable
		row.RuntimeIdentity.ConnectionID = connection.ID
		row.RuntimeIdentity.ConnectionLabel = connection.Label
		row.RuntimeIdentity.ConfigDigest = connection.ConfigDigest
		row.RuntimeIdentity.PeerID = connection.PeerID
		row.RuntimeIdentity.StreamInstance = connection.StreamInstance
		row.RuntimeIdentity.TmuxServerSocket = row.TmuxServerSocket
		row.Agents = slices.Clone(row.Agents)

		result = append(result, row)
	}

	return result
}

func mergeRemoteSessionRows(
	localRows []session.Session,
	catalog RemoteCatalog,
	cfg *config.Config,
	st *session.State,
	now time.Time,
) []session.Session {
	if cfg == nil || !cfg.Remote.Enabled || len(catalog.Connections) == 0 {
		return localRows
	}

	connections := make(map[string]config.RemoteConnectionConfig, len(cfg.Remote.Connections))
	for _, connection := range cfg.Remote.Connections {
		connections[connection.ID] = connection
	}

	total := len(localRows)
	for _, connection := range catalog.Connections {
		total += len(connection.Sessions)
	}

	rows := make([]session.Session, 0, total)
	rows = append(rows, localRows...)

	for _, connection := range catalog.Connections {
		if _, ok := connections[connection.ID]; !ok {
			continue
		}

		status := remoteConnectionStatus(connection, cfg.Remote.StaleAfter, now)
		for _, row := range remoteSessions(connection) {
			row.RemoteStatus = status
			rows = append(rows, row)
		}
	}

	session.SortSessions(rows, st)

	return rows
}

func remoteConnectionStatus(
	connection RemoteConnection,
	staleAfterValue string,
	now time.Time,
) session.RemoteStatus {
	switch connection.LastError {
	case RemoteErrorIncompatibleProtocol, RemoteErrorPeerMismatch:
		return session.RemoteStatusIncompatible
	case RemoteErrorStale:
		return session.RemoteStatusStale
	}

	if connection.Connected && remoteConnectionIsStale(connection, staleAfterValue, now) {
		return session.RemoteStatusStale
	}

	if !connection.Connected {
		if connection.LastSeen.IsZero() && connection.LastError == "" {
			return session.RemoteStatusConnecting
		}

		return session.RemoteStatusUnavailable
	}

	if remoteSourceDegraded(connection.HealthTmux) ||
		remoteSourceDegraded(connection.HealthAgents) ||
		remoteSourceDegraded(connection.HealthSnapshots) {
		return session.RemoteStatusDegraded
	}

	return session.RemoteStatusAvailable
}

func remoteConnectionIsStale(
	connection RemoteConnection,
	staleAfterValue string,
	now time.Time,
) bool {
	if connection.LastSeen.IsZero() {
		return false
	}

	staleAfter, err := time.ParseDuration(staleAfterValue)
	if err != nil || staleAfter <= 0 {
		return false
	}

	return now.Sub(connection.LastSeen) > staleAfter
}

func remoteSourceDegraded(source string) bool {
	return source == RemoteHealthDegraded || source == RemoteHealthUnknown
}

func remoteSessionsEnabled(cfg *config.Config, deps Services) bool {
	return cfg != nil && cfg.Remote.Enabled && deps.Remote != nil
}
