//go:build linux || darwin

package session

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const (
	maxCatalogInventory               = 4096
	StatusComplete      CatalogStatus = "complete"
	StatusPartial       CatalogStatus = "partial"
	StatusFailed        CatalogStatus = "failed"
)

var ErrCurrentSessionAmbiguous = errors.New("current pane belongs to multiple sessions")

var (
	ErrCatalogLimitExceeded  = errors.New("catalog inventory limit exceeded")
	ErrServerIdentityChanged = errors.New("tmux server runtime identity changed during observation")
	ErrEmptyRepositorySource = errors.New("repository source cannot be empty")
)

type CatalogStatus string

// SavedSnapshots reads the generations a catalog lists saved sessions from. Both a durable
// *store.Repository and a lock-free *store.View satisfy it.
type SavedSnapshots interface {
	ReadPracticalCurrent(ctx context.Context) (store.Generation, model.Manifest, error)
	Read(ctx context.Context, id string) (model.Manifest, error)
}

type CatalogOptions struct {
	Server           *gotmux.Server
	Repository       SavedSnapshots
	RepositorySource string
	GenerationID     string
	CurrentPane      string
	CurrentSessionID string
}

type CatalogResult struct {
	Sessions []Session     `json:"sessions"`
	Status   CatalogStatus `json:"status"`
	LiveErr  error         `json:"-"`
	SavedErr error         `json:"-"`
}

func ListCatalog(ctx context.Context, opts CatalogOptions) (CatalogResult, error) {
	result := CatalogResult{
		Sessions: nil,
		Status:   StatusComplete,
		LiveErr:  nil,
		SavedErr: nil,
	}

	var (
		liveSessions  []Session
		liveErr       error
		savedSessions []Session
		savedErr      error
		loaders       sync.WaitGroup
	)

	loaders.Go(func() {
		liveSessions, liveErr = fetchLiveSessions(ctx, opts.Server, opts.CurrentPane, opts.CurrentSessionID)
	})
	loaders.Go(func() {
		savedSessions, savedErr = fetchSavedSessions(ctx, opts.Repository, opts.RepositorySource, opts.GenerationID)
	})
	loaders.Wait()

	result.LiveErr = liveErr
	result.SavedErr = savedErr

	requested, failed := countConstituents(opts.Server, opts.Repository, result.LiveErr, result.SavedErr)
	combined, combinedExceeded := combineCatalogSessions(liveSessions, savedSessions)
	result.Sessions = combined

	limitExceeded := errors.Is(liveErr, ErrCatalogLimitExceeded) ||
		errors.Is(savedErr, ErrCatalogLimitExceeded) ||
		combinedExceeded

	if failed > 0 && failed == requested && !limitExceeded && len(result.Sessions) == 0 {
		result.Status = StatusFailed
		cause := selectCatalogError(result.LiveErr, result.SavedErr)

		return result, oops.In("session").Code("catalog_failed").With("operation", "list_catalog").Wrap(cause)
	}

	if failed > 0 || limitExceeded {
		result.Status = StatusPartial

		cause := selectCatalogError(result.LiveErr, result.SavedErr)
		if cause == nil && limitExceeded {
			cause = ErrCatalogLimitExceeded
		}

		return result, oops.In("session").Code("catalog_failed").With("operation", "list_catalog").Wrap(cause)
	}

	return result, nil
}

func ListLive(ctx context.Context, server *gotmux.Server, currentPane string) ([]Session, error) {
	return listLive(ctx, server, currentPane, "")
}

func ListSaved(ctx context.Context, repo SavedSnapshots, source, generationID string) ([]Session, error) {
	if repo == nil {
		return nil, nil
	}

	if strings.TrimSpace(source) == "" || !utf8.ValidString(source) {
		return nil, oops.In("session").Code("catalog_failed").With("operation", "list_saved").Wrap(ErrEmptyRepositorySource)
	}

	manifest, genID, err := loadSavedManifest(ctx, repo, generationID)
	if err != nil {
		return nil, oops.In("session").Code("catalog_failed").With("operation", "list_saved").Wrap(err)
	}

	windowCounts := make(map[string]int, len(manifest.Sessions))
	for _, link := range manifest.Links {
		windowCounts[link.Session]++
	}

	limitHit := len(manifest.Sessions) > maxCatalogInventory
	count := min(len(manifest.Sessions), maxCatalogInventory)
	result := make([]Session, 0, count)

	activityTime := time.Unix(0, manifest.Finished)
	if manifest.Finished == 0 && manifest.Started != 0 {
		activityTime = time.Unix(0, manifest.Started)
	}

	for i := range count {
		ms := manifest.Sessions[i]
		sess := Session{
			ID:                     ms.ID,
			Name:                   string(ms.Name),
			TmuxServerSocket:       "",
			Kind:                   KindSaved,
			Origin:                 OriginLocal,
			Generation:             genID,
			RepositorySource:       source,
			NameRaw:                RawBytes(slices.Clone(ms.Name)),
			PathRaw:                RawBytes(slices.Clone(ms.Directory)),
			RuntimeIdentity:        zeroRuntimeIdentity(),
			RemoteStatus:           RemoteStatusAvailable,
			WindowCount:            windowCounts[ms.ID],
			Attached:               false,
			Activity:               activityTime,
			IsCurrent:              false,
			ActivePaneID:           "",
			ActiveWindowName:       "",
			ActiveWindowIndex:      "",
			ActivePanePath:         string(ms.Directory),
			ActivePaneCommand:      "",
			ActivePaneTitle:        "",
			AgentTotal:             0,
			AgentActive:            0,
			AgentWaiting:           0,
			AgentFailed:            0,
			AgentInterrupted:       0,
			AgentUnknown:           0,
			AgentIdleSince:         time.Time{},
			AgentWaitingSince:      time.Time{},
			AgentActivityChangedAt: time.Time{},
			Agents:                 nil,
		}
		result = append(result, sess)
	}

	if limitHit {
		return result, ErrCatalogLimitExceeded
	}

	return result, nil
}

func listLive(ctx context.Context, server *gotmux.Server, currentPane, currentSessionID string) ([]Session, error) {
	if server == nil {
		return nil, nil
	}

	extraFields := []string{
		"session_name",
		"pane_id",
		"window_name",
		"window_index",
		"pane_current_path",
		"pane_current_command",
		"pane_title",
		"session_last_attached",
	}

	sessionInfos, err := server.SessionsWith(ctx, gotmux.QueryOptions{
		ExtraFields: extraFields,
		Filter:      "",
	})
	if err != nil {
		return nil, oops.In("session").Code("catalog_failed").With("operation", "list_live").Wrapf(err, "listing live sessions")
	}

	if len(sessionInfos) == 0 {
		return nil, nil
	}

	identity := sessionInfos[0].Handle().Identity()

	currentSession, invokingPane, currentErr := catalogInvokingPane(ctx, server, currentPane, currentSessionID)

	postProbe, err := server.Probe(ctx)
	if err != nil {
		return nil, oops.In("session").Code("catalog_failed").With("operation", "list_live").Wrapf(err, "post-probe server")
	}

	if !postProbe.Identity.Equal(identity) {
		return nil, oops.In("session").Code("catalog_failed").With("operation", "list_live").Wrap(ErrServerIdentityChanged)
	}

	socket := identity.ReportedSocket
	if socket == "" {
		socket = server.Endpoint().SocketPath
	}

	limitHit := len(sessionInfos) > maxCatalogInventory
	count := min(len(sessionInfos), maxCatalogInventory)

	result := buildLiveRows(sessionInfos[:count], socket, identity, currentSession, invokingPane)

	if limitHit {
		return result, errors.Join(ErrCatalogLimitExceeded, currentErr)
	}

	return result, currentErr
}

func catalogInvokingPane(ctx context.Context, server *gotmux.Server, currentPane, currentSessionID string) (string, string, error) {
	pane := strings.TrimSpace(currentPane)
	currentSession := currentSessionID

	var currentErr error

	if currentSession == "" && pane != "" {
		currentSession, currentErr = resolveCurrentSession(ctx, server, pane)
	}

	if pane == "" || currentSession == "" {
		return currentSession, "", currentErr
	}

	if currentSessionID != "" && !paneLinkedToSession(ctx, server, pane, currentSession) {
		return currentSession, "", currentErr
	}

	return currentSession, pane, currentErr
}

func buildLiveRows(infos []gotmux.SessionInfo, socket string, identity gotmux.ServerIdentity, currentSession, invokingPane string) []Session {
	result := make([]Session, 0, len(infos))
	for _, info := range infos {
		row := buildLiveSession(info, socket, identity, currentSession)
		if row.IsCurrent && invokingPane != "" {
			row.ActivePaneID = invokingPane
		}

		result = append(result, row)
	}

	return result
}

func fetchLiveSessions(ctx context.Context, server *gotmux.Server, currentPane, currentSessionID string) ([]Session, error) {
	if server == nil {
		return nil, nil
	}

	live, err := listLive(ctx, server, currentPane, currentSessionID)
	if err != nil {
		if serverAbsent(err) {
			return nil, nil
		}

		return live, err
	}

	return live, nil
}

func serverAbsent(err error) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}

		for _, cause := range causes {
			if !serverAbsent(cause) {
				return false
			}
		}

		return true
	}

	command, classified := errors.AsType[*gotmux.CommandError](err)
	if classified && command.Timeout != gotmux.NoTimeout {
		return false
	}

	cause := errors.Unwrap(err)
	if _, nested := errors.AsType[*gotmux.CommandError](cause); nested {
		return serverAbsent(cause)
	}

	if classified {
		return errors.Is(command.Err, gotmux.ErrNoServer) && absentCommandCauses(command.Err)
	}

	if cause != nil {
		return serverAbsent(cause)
	}

	return errors.Is(err, gotmux.ErrNoServer)
}

func absentCommandCauses(err error) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			if !absentCommandCauses(cause) {
				return false
			}
		}

		return true
	}

	if cause := errors.Unwrap(err); cause != nil {
		return absentCommandCauses(cause)
	}

	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return exit.ExitCode() == 1
	}

	return errors.Is(err, gotmux.ErrNoServer)
}

func fetchSavedSessions(ctx context.Context, repo SavedSnapshots, source, genID string) ([]Session, error) {
	if repo == nil {
		return nil, nil
	}

	if strings.TrimSpace(source) == "" || !utf8.ValidString(source) {
		return nil, oops.In("session").Code("catalog_failed").With("operation", "list_saved").Wrap(ErrEmptyRepositorySource)
	}

	saved, err := ListSaved(ctx, repo, source, genID)
	if err != nil {
		if genID == "" && onlySessionError(err, store.ErrUnavailable) {
			return nil, nil
		}

		return saved, err
	}

	return saved, nil
}

func countConstituents(srv *gotmux.Server, repo SavedSnapshots, liveErr, savedErr error) (int, int) {
	requested, failed := 0, 0
	if srv != nil {
		requested++

		if liveErr != nil && !errors.Is(liveErr, ErrCatalogLimitExceeded) {
			failed++
		}
	}

	if repo != nil {
		requested++

		if savedErr != nil && !errors.Is(savedErr, ErrCatalogLimitExceeded) {
			failed++
		}
	}

	return requested, failed
}

func selectCatalogError(liveErr, savedErr error) error {
	switch {
	case liveErr != nil && savedErr != nil:
		return errors.Join(liveErr, savedErr)
	case liveErr != nil:
		return liveErr
	default:
		return savedErr
	}
}

func combineCatalogSessions(live, saved []Session) ([]Session, bool) {
	totalLen := len(live) + len(saved)
	exceeded := totalLen > maxCatalogInventory
	count := min(totalLen, maxCatalogInventory)

	combined := make([]Session, 0, count)
	combined = append(combined, live...)

	remaining := maxCatalogInventory - len(combined)
	if remaining > 0 && len(saved) > 0 {
		if len(saved) > remaining {
			combined = append(combined, saved[:remaining]...)
		} else {
			combined = append(combined, saved...)
		}
	}

	return combined, exceeded
}

func resolveCurrentSession(ctx context.Context, server *gotmux.Server, currentPane string) (string, error) {
	pane := strings.TrimSpace(currentPane)
	if pane == "" {
		return "", nil
	}

	paneHandle, err := server.Pane(ctx, gotmux.PaneID(pane))
	if err != nil {
		return "", fmt.Errorf("lookup current pane: %w", err)
	}

	window, err := paneHandle.Window(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve current pane window: %w", err)
	}

	links, err := window.Links(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve current window links: %w", err)
	}

	current := ""

	for _, link := range links {
		id := string(link.Session().ID())
		if current != "" && id != current {
			return "", ErrCurrentSessionAmbiguous
		}

		current = id
	}

	return current, nil
}

func paneLinkedToSession(ctx context.Context, server *gotmux.Server, paneID, sessionID string) bool {
	pane, err := server.Pane(ctx, gotmux.PaneID(paneID))
	if err != nil {
		return false
	}

	window, err := pane.Window(ctx)
	if err != nil {
		return false
	}

	links, err := window.Links(ctx)
	if err != nil {
		return false
	}

	return slices.ContainsFunc(links, func(link gotmux.WindowLink) bool {
		return string(link.Session().ID()) == sessionID
	})
}

func loadSavedManifest(ctx context.Context, repo SavedSnapshots, generationID string) (model.Manifest, string, error) {
	if generationID == "" {
		gen, manifest, err := repo.ReadPracticalCurrent(ctx)
		if err != nil {
			return model.Manifest{}, "", fmt.Errorf("read current generation: %w", err)
		}

		return manifest, gen.ID, nil
	}

	manifest, err := repo.Read(ctx, generationID)
	if err != nil {
		return model.Manifest{}, "", fmt.Errorf("read generation: %w", err)
	}

	return manifest, generationID, nil
}

func zeroRuntimeIdentity() RuntimeIdentity {
	return RuntimeIdentity{
		ConnectionID:         "",
		ConnectionLabel:      "",
		ConfigDigest:         "",
		PeerID:               "",
		StreamInstance:       "",
		TmuxServerSocket:     "",
		TmuxServerPID:        0,
		TmuxServerStartedAt:  time.Time{},
		TmuxSessionCreatedAt: time.Time{},
	}
}

func buildLiveSession(
	s gotmux.SessionInfo,
	socket string,
	identity gotmux.ServerIdentity,
	currentSession string,
) Session {
	rawName, hasRawName := s.Raw("session_name")

	var nameRaw RawBytes
	if hasRawName && len(rawName) > 0 {
		nameRaw = slices.Clone(rawName)
	} else {
		nameRaw = slices.Clone([]byte(s.Name))
	}

	paneID, _ := s.Raw("pane_id")
	windowName, _ := s.Raw("window_name")
	windowIndex, _ := s.Raw("window_index")
	panePath, _ := s.Raw("pane_current_path")
	paneCommand, _ := s.Raw("pane_current_command")
	paneTitle, _ := s.Raw("pane_title")

	var pathRaw RawBytes
	if len(panePath) > 0 {
		pathRaw = slices.Clone(panePath)
	}

	isCurrent := currentSession != "" && string(s.ID) == currentSession

	return Session{
		ID:               string(s.ID),
		Name:             s.Name,
		TmuxServerSocket: socket,
		Kind:             KindLive,
		Origin:           OriginLocal,
		Generation:       "",
		RepositorySource: "",
		NameRaw:          nameRaw,
		PathRaw:          pathRaw,
		RuntimeIdentity: RuntimeIdentity{
			ConnectionID:         "",
			ConnectionLabel:      "",
			ConfigDigest:         "",
			PeerID:               "",
			StreamInstance:       "",
			TmuxServerSocket:     socket,
			TmuxServerPID:        identity.PID,
			TmuxServerStartedAt:  identity.Started,
			TmuxSessionCreatedAt: s.Created,
		},
		RemoteStatus:           RemoteStatusAvailable,
		WindowCount:            s.WindowCount,
		Attached:               s.Attached > 0,
		Activity:               s.Activity,
		IsCurrent:              isCurrent,
		ActivePaneID:           string(paneID),
		ActiveWindowName:       string(windowName),
		ActiveWindowIndex:      string(windowIndex),
		ActivePanePath:         string(panePath),
		ActivePaneCommand:      string(paneCommand),
		ActivePaneTitle:        string(paneTitle),
		AgentTotal:             0,
		AgentActive:            0,
		AgentWaiting:           0,
		AgentFailed:            0,
		AgentInterrupted:       0,
		AgentUnknown:           0,
		AgentIdleSince:         time.Time{},
		AgentWaitingSince:      time.Time{},
		AgentActivityChangedAt: time.Time{},
		Agents:                 nil,
	}
}
