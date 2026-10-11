package tui

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/fields"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/task"
	"github.com/zigai/sesh/internal/tui/providers"
)

const (
	originDiscoveryTimeout        = 200 * time.Millisecond
	originInitialDiscoveryTimeout = 5 * time.Second
)

type originSessionsMsg struct {
	rows        []session.Session
	observation uint64
	err         error
}

type sessionLoadResultMsg struct {
	sessions          []session.Session
	originQueried     bool
	originErr         error
	status            session.CatalogStatus
	catalogErr        error
	agentSummaryErr   error
	agentSummaries    []session.AgentSummary
	agentObservation  uint64
	tasks             []task.Live
	tasksLoaded       bool
	tasksErr          error
	agentGit          map[string]agentGitCacheEntry
	customFieldCache  map[string]map[string]fields.CacheEntry
	remoteCatalog     RemoteCatalog
	remoteErr         error
	remoteObservation uint64
	timing            sessionLoadTiming
}

// sessionLoadTiming records how long the parallel catalog and agent loaders took.
type sessionLoadTiming struct {
	catalog time.Duration
	agents  time.Duration
}

type refreshMsg struct{}

func (m *Model) loadSessions() error {
	result := loadSessionRows(m.operationContext(), m.config, m.deps)
	result.remoteObservation = m.remoteCatalogObservation
	result.agentObservation = m.agentSummaryObservation

	if result.catalogErr != nil && len(result.sessions) == 0 {
		logTUILoadTransition(
			m.operationContext(),
			tuiOperationLoadCatalog,
			m.sessionLoadErr,
			result.catalogErr,
			tuiLogOutcomeFailure,
		)
		m.sessionLoadErr = result.catalogErr

		return result.catalogErr
	}

	if result.catalogErr != nil {
		logTUILoadTransition(
			m.operationContext(),
			tuiOperationLoadCatalog,
			m.sessionLoadErr,
			result.catalogErr,
			tuiLogOutcomeDegradation,
		)
		m.sessionLoadErr = result.catalogErr
	} else if m.sessionLoadErr != nil {
		logTUILoadTransition(m.operationContext(), tuiOperationLoadCatalog, m.sessionLoadErr, nil, tuiLogOutcomeFailure)
		m.sessionLoadErr = nil
	}

	m.applySessionRows(result)

	return nil
}

func loadSessionRows(ctx context.Context, cfg *config.Config, deps Services) sessionLoadResultMsg {
	var (
		catalogResult   session.CatalogResult
		catalogErr      error
		agentSummaries  []session.AgentSummary
		agentSummaryErr error
		remoteCatalog   RemoteCatalog
		remoteErr       error
		originRows      []session.Session
		originErr       error
		tasks           []task.Live
		tasksErr        error
	)

	tasksLoaded := workModeEnabled(cfg) && deps.Tasks != nil

	var (
		loaders sync.WaitGroup
		timing  sessionLoadTiming
	)

	loaders.Go(func() {
		started := time.Now()
		catalogResult, catalogErr = deps.Catalog.LoadCatalog(ctx)
		timing.catalog = time.Since(started)
	})

	loaders.Go(func() {
		started := time.Now()
		agentSummaries, agentSummaryErr = loadAgentSummaries(ctx, cfg, deps)
		timing.agents = time.Since(started)
	})

	if tasksLoaded {
		loaders.Go(func() {
			tasks, tasksErr = deps.Tasks.LoadTasks(ctx)
		})
	}

	if remoteSessionsEnabled(cfg, deps) {
		loaders.Go(func() {
			remoteCatalog, remoteErr = deps.Remote.LoadRemoteCatalog()
		})
	}

	if deps.OriginBridge != nil {
		loaders.Go(func() {
			bridgeCtx, cancel := context.WithTimeout(ctx, originDiscoveryTimeout)
			defer cancel()

			_, originRows, originErr = deps.OriginBridge.LoadOriginSessions(bridgeCtx)
		})
	}

	loaders.Wait()

	if catalogErr != nil && len(catalogResult.Sessions) == 0 {
		return sessionLoadResultMsg{
			sessions:          nil,
			originQueried:     deps.OriginBridge != nil,
			originErr:         originErr,
			status:            catalogResult.Status,
			catalogErr:        catalogErr,
			agentSummaryErr:   nil,
			agentSummaries:    nil,
			agentObservation:  0,
			tasks:             nil,
			tasksLoaded:       false,
			tasksErr:          nil,
			agentGit:          nil,
			customFieldCache:  nil,
			remoteCatalog:     RemoteCatalog{GeneratedAt: time.Time{}, Connections: nil},
			remoteErr:         nil,
			remoteObservation: 0,
			timing:            timing,
		}
	}

	if agentSummaryErr != nil {
		agentSummaries = nil
	}

	localRows := applyAgentSummaries(catalogResult.Sessions, agentSummaries)
	localRows = append(localRows, originRows...)

	return sessionLoadResultMsg{
		sessions:          mergeRemoteSessionRows(localRows, remoteCatalog, cfg, nil, time.Now()),
		originQueried:     deps.OriginBridge != nil,
		originErr:         originErr,
		status:            catalogResult.Status,
		catalogErr:        catalogErr,
		agentSummaryErr:   agentSummaryErr,
		agentSummaries:    agentSummaries,
		agentObservation:  0,
		tasks:             tasks,
		tasksLoaded:       tasksLoaded,
		tasksErr:          tasksErr,
		agentGit:          nil,
		customFieldCache:  nil,
		remoteCatalog:     remoteCatalog,
		remoteErr:         remoteErr,
		remoteObservation: 0,
		timing:            timing,
	}
}

func loadInitialSessionRows(ctx context.Context, cfg *config.Config, deps Services) sessionLoadResultMsg {
	deps.OriginBridge = nil

	return loadSessionRows(ctx, cfg, deps)
}

func applyAgentSummaries(rows []session.Session, summaries []session.AgentSummary) []session.Session {
	result := slices.Clone(rows)
	byID, byName := indexAgentSummaries(summaries)

	for index := range result {
		if result[index].Origin == session.OriginSSH || result[index].Origin == session.OriginBridge {
			continue
		}

		applyAgentSummaryToSession(&result[index], byID, byName)
	}

	return result
}

func loadAgentSummaries(
	ctx context.Context,
	cfg *config.Config,
	deps Services,
) ([]session.AgentSummary, error) {
	if !agentSummariesEnabled(cfg, deps) {
		return nil, nil
	}

	snapshot, err := deps.Agents.LoadAgentSummaries(ctx)
	if err != nil {
		return nil, oops.In("tui").Code("load_agents_failed").With("operation", "load_agent_summaries").Wrap(err)
	}

	return liveAgentSummaries(snapshot), nil
}

func liveAgentSummaries(snapshot AgentSnapshot) []session.AgentSummary {
	if len(snapshot.Summaries) == 0 || !snapshot.PanesObserved {
		return snapshot.Summaries
	}

	return pruneAgentsWithVanishedPanes(snapshot.Summaries, snapshot.Panes)
}

func agentSummariesEnabled(cfg *config.Config, deps Services) bool {
	return cfg != nil &&
		cfg.AgentSessions.Enabled &&
		(cfg.AgentSessions.PopupCounts ||
			agentDisplayModeEnabled(cfg.Settings.DisplayModes) ||
			seshFormatNeedsAgentSummaries(cfg.Display.SeshFormat)) &&
		deps.Agents != nil
}

func seshFormatNeedsAgentSummaries(format string) bool {
	template, err := display.Compile(display.FormatKindSession, format)
	if err != nil {
		return false
	}

	return template.UsesAny(
		display.FieldAgentTotal,
		display.FieldAgentWorking,
		display.FieldAgentWaiting,
		display.FieldAgentIdle,
		display.FieldAgentUnknown,
		display.FieldAgentStatus,
		display.FieldAgentAge,
		display.FieldAgentDuration,
		display.FieldAgentClock,
	)
}

func agentDisplayModeEnabled(modes []string) bool {
	if len(modes) == 0 {
		return true
	}

	for _, mode := range modes {
		if parseDisplayMode(mode).tree() {
			return true
		}
	}

	return false
}

func indexAgentSummaries(summaries []session.AgentSummary) (map[string]session.AgentSummary, map[string]session.AgentSummary) {
	byID := make(map[string]session.AgentSummary, len(summaries))
	byName := make(map[string]session.AgentSummary, len(summaries))
	idCounts := make(map[string]int, len(summaries))
	nameCounts := make(map[string]int, len(summaries))

	for _, summary := range summaries {
		if summary.TmuxSessionID != "" {
			idCounts[agentSummaryLookupKey(summary.TmuxServerSocket, summary.TmuxSessionID)]++
		}

		if summary.TmuxSessionName != "" {
			nameCounts[agentSummaryLookupKey(summary.TmuxServerSocket, summary.TmuxSessionName)]++
		}
	}

	for _, summary := range summaries {
		idKey := agentSummaryLookupKey(summary.TmuxServerSocket, summary.TmuxSessionID)
		if summary.TmuxSessionID != "" && idCounts[idKey] == 1 {
			byID[idKey] = summary
		}

		nameKey := agentSummaryLookupKey(summary.TmuxServerSocket, summary.TmuxSessionName)
		if summary.TmuxSessionName != "" && nameCounts[nameKey] == 1 {
			byName[nameKey] = summary
		}
	}

	return byID, byName
}

func applyAgentSummaryToSession(
	row *session.Session,
	byID map[string]session.AgentSummary,
	byName map[string]session.AgentSummary,
) {
	summary, ok := agentSummaryForSession(row, byID, byName)
	if !ok {
		row.AgentTotal = 0
		row.AgentActive = 0
		row.AgentWaiting = 0
		row.AgentFailed = 0
		row.AgentInterrupted = 0
		row.AgentUnknown = 0
		row.AgentIdleSince = time.Time{}
		row.AgentWaitingSince = time.Time{}
		row.AgentActivityChangedAt = time.Time{}
		row.Agents = nil

		return
	}

	row.AgentTotal = summary.Total
	row.AgentActive = summary.Active
	row.AgentWaiting = summary.Waiting
	row.AgentFailed = summary.Failed
	row.AgentInterrupted = summary.Interrupted
	row.AgentUnknown = summary.Unknown
	row.AgentIdleSince = summary.IdleSince
	row.AgentWaitingSince = summary.WaitingSince
	row.AgentActivityChangedAt = summary.ActivityChangedAt
	agentSessions := slices.Clone(summary.Agents)
	retainAgentPositions(agentSessions, row.Agents)
	row.Agents = agentSessions
	slices.SortStableFunc(row.Agents, compareAgentSessions)
}

func compareAgentSessions(left session.AgentSession, right session.AgentSession) int {
	for _, labels := range [][2]string{
		{left.HarnessSession, right.HarnessSession},
		{left.Harness, right.Harness},
		{left.RegistryID, right.RegistryID},
		{left.PaneID, right.PaneID},
	} {
		if byLabel := compareFoldedLabels(labels[0], labels[1]); byLabel != 0 {
			return byLabel
		}
	}

	return 0
}

func agentSummaryForSession(
	row *session.Session,
	byID map[string]session.AgentSummary,
	byName map[string]session.AgentSummary,
) (session.AgentSummary, bool) {
	if row.ID != "" {
		for _, server := range agentSummaryServerLookupKeys(row.TmuxServerSocket) {
			key := agentSummaryLookupKey(server, row.ID)
			if summary, ok := byID[key]; ok && agentSummaryMatchesSession(row, summary) {
				return summary, true
			}
		}
	}

	if row.Name == "" {
		return emptyAgentSummary(), false
	}

	for _, server := range agentSummaryServerLookupKeys(row.TmuxServerSocket) {
		key := agentSummaryLookupKey(server, row.Name)

		summary, ok := byName[key]
		if !ok {
			continue
		}

		if row.ID != "" && summary.TmuxSessionID != "" && summary.TmuxSessionID != row.ID {
			continue
		}

		if agentSummaryMatchesSession(row, summary) {
			return summary, true
		}
	}

	return emptyAgentSummary(), false
}

func emptyAgentSummary() session.AgentSummary {
	var summary session.AgentSummary

	return summary
}

func agentSummaryMatchesSession(row *session.Session, summary session.AgentSummary) bool {
	return row == nil ||
		(sameServerSocket(row.TmuxServerSocket, summary.TmuxServerSocket) &&
			(row.Name == "" ||
				summary.TmuxSessionName == "" ||
				row.Name == summary.TmuxSessionName))
}

func agentSummaryLookupKey(server string, identity string) string {
	server = normalizeServerSocket(server)
	if server == "" {
		return identity
	}

	return server + "\x00" + identity
}

func agentSummaryServerLookupKeys(server string) []string {
	keys := serverSocketLookupKeys(server)
	if len(keys) == 0 {
		return []string{""}
	}

	return append(keys, "")
}

func (m *Model) applySessionRows(result sessionLoadResultMsg) []agentViewStateUpdate {
	selected := m.selectionBeforeCatalogLoad()
	if result.originQueried {
		m.observeOriginLoad(result.originErr)
	}

	if result.originQueried && result.originErr == nil {
		m.originObservation++
		m.originRows = originSessionRows(result.sessions)
	} else if len(m.originRows) == 0 {
		m.originRows = originSessionRows(result.sessions)
	}

	rows := append(nonOriginSessionRows(result.sessions), m.originRows...)

	acceptLoadedRemote := m.acceptsLoadedRemote(result)
	if acceptLoadedRemote {
		m.remoteCatalogErr = result.remoteErr
		if result.remoteErr == nil {
			m.remoteCatalog = result.remoteCatalog
		}
	}

	m.sessions = rows
	if !acceptLoadedRemote || result.remoteErr != nil {
		m.sessions = mergeRemoteSessionRows(localSessionRows(rows), m.remoteCatalog, m.config, m.state, time.Now())
	}

	if m.acceptsLoadedAgents(result) {
		m.agentSummaryErr = result.agentSummaryErr
		if result.agentSummaryErr == nil {
			m.agentSummaries = cloneAgentSummaries(result.agentSummaries)
		}
	} else {
		m.sessions = applyAgentSummaries(m.sessions, m.agentSummaries)
	}

	m.applyLoadedTasks(result)

	session.SortSessions(m.sessions, m.state)

	for index := range m.sessions {
		m.applyCachedAgentGit(&m.sessions[index])
	}

	m.applyCatalogStatus(result.status)
	m.applyRemoteStatus(m.remoteCatalogErr)
	m.applyAgentSummaryStatus(m.agentSummaryErr)

	m.loadingSessions = false
	m.refreshDisplaySessions()
	m.filterSessions()
	m.restoreSelectedSession(selected)
	m.settleInitialAgentSelection()

	views := m.recordActiveAgentViews()

	return views
}

func (m *Model) applyLoadedTasks(result sessionLoadResultMsg) {
	if !result.tasksLoaded {
		return
	}

	m.tasksErr = result.tasksErr
	if result.tasksErr == nil {
		m.tasks = result.tasks
	}
}

func (m *Model) selectionBeforeCatalogLoad() sessionIdentity {
	if m.initialAgentSelectionPending && m.loadingSessions {
		return emptySessionIdentity()
	}

	return m.selectedSessionIdentity()
}

func (m *Model) acceptsLoadedRemote(result sessionLoadResultMsg) bool {
	return !m.remoteCatalogWatchDelivered && result.remoteObservation == m.remoteCatalogObservation
}

func (m *Model) acceptsLoadedAgents(result sessionLoadResultMsg) bool {
	return result.agentObservation == m.agentSummaryObservation
}

func originSessionRows(rows []session.Session) []session.Session {
	var origin []session.Session

	for _, row := range rows {
		if row.Origin == session.OriginBridge {
			origin = append(origin, row)
		}
	}

	return origin
}

func nonOriginSessionRows(rows []session.Session) []session.Session {
	result := make([]session.Session, 0, len(rows))
	for _, row := range rows {
		if row.Origin != session.OriginBridge {
			result = append(result, row)
		}
	}

	return result
}

func (m *Model) handleOriginSessions(msg originSessionsMsg) {
	if msg.observation != m.originObservation {
		return
	}

	m.observeOriginLoad(msg.err)

	if msg.err != nil {
		return
	}

	selected := m.selectedSessionIdentity()
	m.originRows = msg.rows
	m.sessions = append(nonOriginSessionRows(m.sessions), msg.rows...)
	session.SortSessions(m.sessions, m.state)
	m.refreshDisplaySessions()
	m.filterSessions()
	m.restoreSelectedSession(selected)
}

func (m *Model) observeOriginLoad(err error) {
	logTUILoadTransition(m.operationContext(), tuiOperationLoadOriginSessions, m.originLoadErr, err, tuiLogOutcomeDegradation)
	m.originLoadErr = err
}

func (m *Model) startRemoteCatalogWatchCommand() tea.Cmd {
	if !remoteSessionsEnabled(m.config, m.deps) || m.deps.RemoteWatcher == nil || m.remoteCatalogWatchStarted {
		return nil
	}

	m.remoteCatalogWatchStarted = true
	watcher := m.deps.RemoteWatcher
	updates := m.remoteCatalogUpdates
	ctx := m.operationContext()

	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		watchErr := watcher.WatchRemoteCatalog(ctx, func(catalog RemoteCatalog, err error) {
			select {
			case updates <- remoteCatalogMsg{catalog: catalog, err: err}:
			case <-ctx.Done():
			}
		})

		if watchErr != nil && ctx.Err() == nil {
			select {
			case updates <- remoteCatalogMsg{catalog: RemoteCatalog{GeneratedAt: time.Time{}, Connections: nil}, err: watchErr}:
			case <-ctx.Done():
			}
		}

		return nil
	}
}

func (m *Model) waitRemoteCatalogUpdateCommand() tea.Cmd {
	updates := m.remoteCatalogUpdates
	ctx := m.operationContext()

	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		select {
		case <-ctx.Done():
			return nil
		case msg := <-updates:
			return msg
		}
	}
}

func (m *Model) handleRemoteCatalog(msg remoteCatalogMsg) {
	m.remoteCatalogObservation++
	previousErr := m.remoteCatalogErr
	m.remoteCatalogErr = msg.err

	if msg.err != nil {
		m.applyRemoteStatus(msg.err)
		logTUILoadTransition(
			m.operationContext(),
			tuiOperationLoadRemoteCatalog,
			previousErr,
			msg.err,
			tuiLogOutcomeDegradation,
		)

		return
	}

	m.remoteCatalog = msg.catalog
	m.remoteCatalogWatchDelivered = true
	m.applyRemoteStatus(nil)

	if previousErr != nil {
		logTUILoadTransition(
			m.operationContext(),
			tuiOperationLoadRemoteCatalog,
			previousErr,
			nil,
			tuiLogOutcomeDegradation,
		)
	}

	selected := m.selectedSessionIdentity()
	m.sessions = mergeRemoteSessionRows(localSessionRows(m.sessions), m.remoteCatalog, m.config, m.state, time.Now())
	m.refreshDisplaySessions()
	m.filterSessions()
	m.restoreSelectedSession(selected)
}

func localSessionRows(rows []session.Session) []session.Session {
	result := make([]session.Session, 0, len(rows))

	for _, row := range rows {
		if row.Origin == session.OriginSSH {
			continue
		}

		result = append(result, row)
	}

	return result
}

func (m *Model) handleAgentSummaries(msg agentSummariesMsg) tea.Cmd {
	if msg.polled && msg.observation != m.agentSummaryObservation {
		return nil
	}

	if msg.revision != 0 && msg.revision <= m.agentSummaryRevision {
		return nil
	}

	m.agentSummaryObservation++
	if msg.revision != 0 {
		m.agentSummaryRevision = msg.revision
	}

	previousErr := m.agentSummaryErr
	m.agentSummaryErr = msg.err

	if msg.err != nil {
		m.applyAgentSummaryStatus(msg.err)
		logTUILoadTransition(
			m.operationContext(),
			tuiOperationLoadAgentSummaries,
			previousErr,
			msg.err,
			tuiLogOutcomeDegradation,
		)

		return nil
	}

	m.applyAgentSummaryStatus(nil)

	if previousErr != nil {
		logTUILoadTransition(
			m.operationContext(),
			tuiOperationLoadAgentSummaries,
			previousErr,
			nil,
			tuiLogOutcomeDegradation,
		)
	}

	m.agentSummaries = cloneAgentSummaries(msg.summaries)
	m.applyAgentSummaries(m.agentSummaries)
	views := m.recordActiveAgentViews()

	return m.persistAgentViewsCommand(views)
}

func cloneAgentSummaries(summaries []session.AgentSummary) []session.AgentSummary {
	result := slices.Clone(summaries)
	for index := range result {
		result[index].Agents = slices.Clone(result[index].Agents)
	}

	return result
}

func (m *Model) applyAgentSummaries(summaries []session.AgentSummary) {
	selected := m.selectedSessionIdentity()
	byID, byName := indexAgentSummaries(summaries)

	for index := range m.sessions {
		row := &m.sessions[index]
		if row.Origin == session.OriginSSH || row.Origin == session.OriginBridge {
			continue
		}

		applyAgentSummaryToSession(row, byID, byName)
		m.applyCachedAgentGit(row)
	}

	m.refreshDisplaySessions()
	m.filterSessions()
	m.restoreSelectedSession(selected)
	m.settleInitialAgentSelection()
}

func (m *Model) recordActiveAgentViews() []agentViewStateUpdate {
	var views []agentViewStateUpdate

	for index := range m.sessions {
		row := &m.sessions[index]
		if row.Origin == session.OriginSSH || row.Origin == session.OriginBridge || row.ID == "" {
			continue
		}

		for _, agent := range row.Agents {
			if !m.hasActiveAgentView(*row, agent) {
				continue
			}

			if m.state != nil {
				m.state.RecordAgentView(row.SemanticIdentity(), agent.PaneID)
			}

			views = append(views, agentViewStateUpdate{
				identity: row.SemanticIdentity(),
				paneID:   agent.PaneID,
			})
		}
	}

	return views
}

func (m *Model) persistAgentViewsCommand(views []agentViewStateUpdate) tea.Cmd {
	if len(views) == 0 {
		return nil
	}

	if deferrer, ok := m.deps.State.(AgentViewDeferrer); ok {
		deferred := make([]AgentView, 0, len(views))
		for _, view := range views {
			deferred = append(deferred, AgentView{Session: view.identity, PaneID: view.paneID})
		}

		if deferrer.DeferAgentViews(deferred) {
			return nil
		}
	}

	ctx := m.operationContext()

	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		if err := m.updateState(ctx, func(current *session.State) {
			for _, view := range views {
				current.RecordAgentView(view.identity, view.paneID)
			}
		}); err != nil {
			return stateUpdateResultMsg{err: err}
		}

		return stateUpdateResultMsg{err: nil}
	}
}

func (m *Model) loadInitialSessionsCommand() tea.Cmd {
	ctx := m.operationContext()
	cfg := m.config
	deps := m.deps
	gitEnabled := m.agentGitEnabled()
	cachePath := agentGitCachePath()
	customFieldNames := m.customFieldNames()
	remoteObservation := m.remoteCatalogObservation
	agentObservation := m.agentSummaryObservation

	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		result := loadInitialSessionRows(ctx, cfg, deps)
		if finisher, ok := deps.Catalog.(InitialLoadFinisher); ok {
			finisher.FinishInitialLoad()
		}

		result.remoteObservation = remoteObservation

		result.agentObservation = agentObservation
		if gitEnabled {
			result.agentGit = readAgentGitCache(cachePath)
		}

		result.customFieldCache = readCustomFieldCaches(customFieldNames)

		return result
	}
}

func (m *Model) loadOriginSessionsCommand() tea.Cmd {
	if m.deps.OriginBridge == nil {
		return nil
	}

	ctx := m.operationContext()
	bridge := m.deps.OriginBridge
	observation := m.originObservation

	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		queryCtx, cancel := context.WithTimeout(ctx, originInitialDiscoveryTimeout)
		defer cancel()

		_, rows, err := bridge.LoadOriginSessions(queryCtx)

		return originSessionsMsg{rows: rows, observation: observation, err: err}
	}
}

func (m *Model) loadAgentSummariesCommand() tea.Cmd {
	if !m.agentSummaryRefreshEnabled() {
		return nil
	}

	ctx := m.operationContext()
	cfg := m.config
	deps := m.deps
	observation := m.agentSummaryObservation

	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		summaries, err := loadAgentSummaries(ctx, cfg, deps)

		return agentSummariesMsg{summaries: summaries, revision: 0, polled: true, observation: observation, err: err}
	}
}

func (m *Model) startAgentSummaryWatchCommand() tea.Cmd {
	if m.deps.AgentWatcher == nil || m.agentSummaryWatchStarted || !m.agentSummaryWatchEnabled() {
		return nil
	}

	m.agentSummaryWatchStarted = true
	watcher := m.deps.AgentWatcher
	updates := m.agentSummaryUpdates
	ctx := m.operationContext()
	pollAvailable := m.agentSummaryRefreshEnabled()

	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		watchErr := watcher.WatchAgentSummaries(ctx, func(revision uint64, snapshot AgentSnapshot, err error) error {
			summaries := snapshot.Summaries
			if err == nil {
				summaries = liveAgentSummaries(snapshot)
			}

			select {
			case updates <- agentSummariesMsg{summaries: summaries, revision: revision, polled: false, observation: 0, err: err}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})

		if shouldReportAgentWatchError(ctx, watchErr, pollAvailable) {
			select {
			case updates <- agentSummariesMsg{summaries: nil, revision: 0, polled: false, observation: 0, err: watchErr}:
			case <-ctx.Done():
			}
		}

		return nil
	}
}

func shouldReportAgentWatchError(ctx context.Context, err error, pollAvailable bool) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}

	return !pollAvailable || !errors.Is(err, agents.ErrRegistryUnavailable)
}

func (m *Model) waitAgentSummaryUpdateCommand() tea.Cmd {
	updates := m.agentSummaryUpdates
	ctx := m.operationContext()

	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		select {
		case <-ctx.Done():
			return nil
		case msg := <-updates:
			return msg
		}
	}
}

func (m *Model) agentSummaryWatchEnabled() bool {
	return agentSummariesEnabled(m.config, m.deps)
}

func (m *Model) handleSessionLoadResult(msg sessionLoadResultMsg) tea.Cmd {
	if m.popupStartup == popupStartupWaiting {
		logTUIFirstFrame(m.operationContext(), firstFrameTimingAt(m.initializedAt, m.launchedAt, &msg.timing, time.Now()))
	}

	m.popupStartup = popupStartupContent

	if m.loadingSessions {
		m.agentGit = msg.agentGit
	}

	if msg.catalogErr != nil && len(msg.sessions) == 0 {
		logTUILoadTransition(
			m.operationContext(),
			tuiOperationLoadCatalog,
			m.sessionLoadErr,
			msg.catalogErr,
			tuiLogOutcomeFailure,
		)
		m.sessionLoadErr = msg.catalogErr
		m.setErrorStatusf("Failed to load sessions: %v", msg.catalogErr)
		m.loadingSessions = false

		return nil
	}

	if msg.catalogErr != nil {
		logTUILoadTransition(
			m.operationContext(),
			tuiOperationLoadCatalog,
			m.sessionLoadErr,
			msg.catalogErr,
			tuiLogOutcomeDegradation,
		)
		m.sessionLoadErr = msg.catalogErr
	} else if m.sessionLoadErr != nil {
		logTUILoadTransition(
			m.operationContext(),
			tuiOperationLoadCatalog,
			m.sessionLoadErr,
			nil,
			tuiLogOutcomeDegradation,
		)
		m.sessionLoadErr = nil
	}

	cachedGit := m.loadingSessions && msg.agentGit != nil

	m.seedCustomFieldCaches(msg.customFieldCache)

	views := m.applySessionRows(msg)
	if cachedGit {
		m.providerRunner.Publish(providers.Git, m.gitProviderRows(m.agentGit))
	}

	commands := []tea.Cmd{
		m.persistAgentViewsCommand(views),
		m.loadAgentGitCommand(),
		m.loadResourceCommand(),
		m.loadGHCommand(),
		m.syncTasksCommand(),
	}

	if msg.agentSummaryErr != nil && m.acceptsLoadedAgents(msg) {
		m.agentSummaryErr = msg.agentSummaryErr
		m.applyAgentSummaryStatus(msg.agentSummaryErr)
		logTUIFailure(m.operationContext(), tuiOperationLoadAgentSummaries, msg.agentSummaryErr)
	}

	return tea.Batch(commands...)
}

func (m *Model) refreshSessions() tea.Cmd {
	ctx := m.operationContext()
	cfg := m.config
	deps := m.deps
	remoteObservation := m.remoteCatalogObservation
	agentObservation := m.agentSummaryObservation

	m.loadingSessions = true
	m.invalidateAgentGit()
	m.providerRunner.Invalidate(providers.Resource)
	m.providerRunner.Invalidate(providers.GH)

	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		result := loadSessionRows(ctx, cfg, deps)
		result.remoteObservation = remoteObservation
		result.agentObservation = agentObservation

		return result
	}
}

func (m *Model) agentSummaryRefreshEnabled() bool {
	return agentSummariesEnabled(m.config, m.deps) && m.config.AgentSessions.RefreshInterval != ""
}

func (m *Model) agentSummaryTickCommand() tea.Cmd {
	if !m.agentSummaryRefreshEnabled() {
		return nil
	}

	interval, err := time.ParseDuration(m.config.AgentSessions.RefreshInterval)
	if err != nil || interval <= 0 {
		return nil
	}

	return tea.Tick(interval, func(time.Time) tea.Msg { return agentSummaryTickMsg{} })
}

func (m *Model) agentClockTickCommand() tea.Cmd {
	definition, _ := providers.DefinitionFor(providers.Clock)
	if !agentSummariesEnabled(m.config, m.deps) ||
		(!m.sessionFormats.agentRowsUseAny(definition.Fields...) &&
			!m.sessionFormats.agentRowsUseAny(display.FieldAgentPromptAge) &&
			!m.sessionFormats.agentRowsUseWidget("spinner")) {
		return nil
	}

	return tea.Tick(definition.Cadence.Interval, func(time.Time) tea.Msg { return agentClockTickMsg{} })
}

func (m *Model) handleRefresh() {
	if err := m.loadSessions(); err != nil {
		m.setErrorStatusf("Failed to refresh sessions: %v", err)
	}
}

func (m *Model) applyCatalogStatus(status session.CatalogStatus) {
	if status == session.StatusPartial {
		m.setDegradedSource(degradedSourceCatalog, "Session list is incomplete: some sources could not be read")

		return
	}

	m.clearDegradedSource(degradedSourceCatalog)
}

func (m *Model) applyRemoteStatus(err error) {
	if err == nil || !remoteSessionsEnabled(m.config, m.deps) {
		m.clearDegradedSource(degradedSourceRemote)

		return
	}

	m.setDegradedSource(degradedSourceRemote, "Remote sessions unavailable: cache could not be read")
}

func (m *Model) applyAgentSummaryStatus(err error) {
	if err == nil || !agentSummariesEnabled(m.config, m.deps) {
		m.clearDegradedSource(degradedSourceAgents)

		return
	}

	m.setDegradedSource(degradedSourceAgents, "Agent status unavailable: inventory could not be read")
}

func workModeEnabled(cfg *config.Config) bool {
	return slices.Contains(configuredDisplayModes(cfg), displayModeWork)
}
