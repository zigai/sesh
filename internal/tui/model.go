package tui

import (
	"context"
	"errors"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/rs/zerolog"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

const (
	modeStandalone uiMode = iota
	modeInline
	modePopup
)

const (
	dialogNone dialogState = iota
	dialogConfirmKill
	dialogConfirmDelete
	dialogInputRename
	dialogInputCreateDir
)

const (
	statusNone statusLevel = iota
	statusError
	statusDegraded
)

const (
	degradedSourceAgents  = "agents"
	degradedSourceCatalog = "catalog"
	degradedSourceRemote  = "remote"
)

const agentGitTickInterval = 2 * time.Second

const remoteStatusTickInterval = 2 * time.Second

var (
	errNilConfig   = errors.New("config must not be nil")
	errNilState    = errors.New("state must not be nil")
	errModelClosed = errors.New("model is closed: state update rejected")
)

type (
	uiMode      int
	dialogState int
	statusLevel int
)

type sessionActionResultMsg struct {
	operation      tuiLogOperation
	deferred       bool
	attach         bool
	err            error
	sessionName    string
	target         session.Session
	switchSession  string
	switchPane     string
	quit           bool
	refresh        bool
	statePersisted bool
	agentView      *agentViewStateUpdate
}

type agentViewStateUpdate struct {
	identity session.Identity
	paneID   string
}

type stateUpdateResultMsg struct {
	err error
}

type agentSummariesMsg struct {
	summaries   []session.AgentSummary
	revision    uint64
	polled      bool
	observation uint64
	err         error
}

type remoteCatalogMsg struct {
	catalog RemoteCatalog
	err     error
}

type (
	agentSummaryTickMsg struct{}
	agentClockTickMsg   struct{}
	remoteStatusTickMsg struct {
		now time.Time
	}
)

type Model struct {
	sessions                     []session.Session
	displaySessions              []session.Session
	filtered                     []session.Session
	listRows                     []popupListRow
	selectableRows               []popupListRow
	shortcutRows                 []popupListRow
	state                        *session.State
	config                       *config.Config
	cursor                       int
	agentCursor                  int
	listTop                      int
	showHelp                     bool
	width                        int
	height                       int
	mode                         uiMode
	displayMode                  displayMode
	initialAgentSelectionPending bool
	displayModes                 []displayMode
	agentSort                    agentSort
	sessionFormats               sessionRowFormats
	textInput                    textinput.Model
	renameInput                  textinput.Model
	createDirInput               textinput.Model
	dialog                       dialogState
	confirmAction                func() error
	confirmCommand               tea.Cmd
	confirmYes                   bool
	createSession                string
	createDirSuggestions         []string
	createDirSuggestionIndex     int
	createDirSuggestionTop       int
	statusMessage                string
	statusLevel                  statusLevel
	degradedSources              map[string]string
	footerHoverDesc              string
	sessionLoadErr               error
	agentSummaryErr              error
	remoteCatalogErr             error
	remoteCatalog                RemoteCatalog
	originRows                   []session.Session
	originObservation            uint64
	remoteCatalogWatchDelivered  bool
	remoteCatalogObservation     uint64
	switchAfterQuit              string
	paneAfterQuit                string
	loadingSessions              bool
	suppressInitialFrame         bool
	agentGit                     map[string]agentGitCacheEntry
	agentGitRequest              uint64
	agentGitCancel               context.CancelFunc
	gitInFlight                  bool
	gitScan                      gitScanner
	remoteCatalogUpdates         chan remoteCatalogMsg
	remoteCatalogWatchStarted    bool
	agentSummaryUpdates          chan agentSummariesMsg
	agentSummaryWatchStarted     bool
	agentSummaryRevision         uint64
	agentSummaryObservation      uint64
	agentSummaries               []session.AgentSummary
	deferredSwitch               session.Session
	hasDeferredSwitch            bool
	deferredAttach               session.Session
	hasDeferredAttach            bool
	deferredRemoteAttach         session.Session
	hasDeferredRemoteAttach      bool
	remoteAttachPane             string
	originDetachAfterQuit        bool
	renameTarget                 session.Session
	killTarget                   session.Session
	deleteTarget                 session.Session
	lifecycle                    *lifecycle
	logger                       zerolog.Logger
	deps                         Services
}

func NewPopupModel(ctx context.Context, cfg *config.Config, st *session.State, services Services) (*Model, error) {
	model, err := buildModel(ctx, cfg, st, services)
	if err != nil {
		return nil, err
	}

	model.mode = modePopup

	return model, nil
}

func NewModel(ctx context.Context, cfg *config.Config, st *session.State, services Services) (*Model, error) {
	return buildModel(ctx, cfg, st, services)
}

func NewInlineModel(ctx context.Context, cfg *config.Config, st *session.State, services Services) (*Model, error) {
	model, err := buildModel(ctx, cfg, st, services)
	if err != nil {
		return nil, err
	}

	model.mode = modeInline

	return model, nil
}

func NewStandaloneModel(ctx context.Context, cfg *config.Config, st *session.State, services Services) (*Model, error) {
	model, err := buildModel(ctx, cfg, st, services)
	if err != nil {
		return nil, err
	}

	model.mode = modeStandalone

	return model, nil
}

func detectUIMode(deps Services) uiMode {
	if deps.Detector == nil || !deps.Detector.TmuxIsInsideTmux() {
		return modeStandalone
	}

	if os.Getenv("TMUX_POPUP") != "" {
		return modePopup
	}

	return modeInline
}

func buildModel(ctx context.Context, cfg *config.Config, st *session.State, deps Services) (*Model, error) {
	if cfg == nil {
		return nil, errNilConfig
	}

	if st == nil {
		return nil, errNilState
	}

	if err := validateServices(deps); err != nil {
		return nil, err
	}

	textInputStyles := newTextInputStyles(cfg)
	textInput := newTextInput(textInputStyles)
	textInput.Focus()

	createDirInput := newTextInput(textInputStyles)
	renameInput := newTextInput(textInputStyles)

	displayModes := configuredDisplayModes(cfg)
	displayMode := initialDisplayMode(st.PopupDisplayMode, displayModes)

	formats, err := compileSessionRowFormats(cfg.Display, cfg.Settings)
	if err != nil {
		return nil, err
	}

	return &Model{
		sessions:                     []session.Session{},
		displaySessions:              []session.Session{},
		filtered:                     []session.Session{},
		listRows:                     []popupListRow{},
		selectableRows:               []popupListRow{},
		shortcutRows:                 []popupListRow{},
		state:                        st,
		config:                       cfg,
		cursor:                       0,
		agentCursor:                  parentAgentIndex,
		listTop:                      0,
		showHelp:                     false,
		width:                        0,
		height:                       0,
		mode:                         detectUIMode(deps),
		displayMode:                  displayMode,
		initialAgentSelectionPending: displayMode == displayModeAgents,
		displayModes:                 displayModes,
		agentSort:                    parseAgentSort(cfg.AgentSessions.PopupSort),
		sessionFormats:               formats,
		textInput:                    textInput,
		renameInput:                  renameInput,
		createDirInput:               createDirInput,
		dialog:                       dialogNone,
		confirmAction:                nil,
		confirmCommand:               nil,
		confirmYes:                   false,
		createSession:                "",
		createDirSuggestions:         nil,
		createDirSuggestionIndex:     0,
		createDirSuggestionTop:       0,
		statusMessage:                "",
		statusLevel:                  statusNone,
		degradedSources:              nil,
		footerHoverDesc:              "",
		sessionLoadErr:               nil,
		agentSummaryErr:              nil,
		remoteCatalogErr:             nil,
		remoteCatalog:                RemoteCatalog{GeneratedAt: time.Time{}, Connections: nil},
		originRows:                   nil,
		originObservation:            0,
		remoteCatalogWatchDelivered:  false,
		remoteCatalogObservation:     0,
		switchAfterQuit:              "",
		paneAfterQuit:                "",
		loadingSessions:              false,
		suppressInitialFrame:         false,
		agentGit:                     map[string]agentGitCacheEntry{},
		agentGitRequest:              0,
		agentGitCancel:               nil,
		gitInFlight:                  false,
		gitScan:                      scanGitMetadata,
		remoteCatalogUpdates:         make(chan remoteCatalogMsg, 1),
		remoteCatalogWatchStarted:    false,
		agentSummaryUpdates:          make(chan agentSummariesMsg, 1),
		agentSummaryWatchStarted:     false,
		agentSummaryRevision:         0,
		agentSummaryObservation:      0,
		agentSummaries:               nil,
		deferredSwitch:               emptySession(),
		hasDeferredSwitch:            false,
		deferredAttach:               emptySession(),
		hasDeferredAttach:            false,
		deferredRemoteAttach:         emptySession(),
		hasDeferredRemoteAttach:      false,
		remoteAttachPane:             "",
		originDetachAfterQuit:        false,
		renameTarget:                 emptySession(),
		killTarget:                   emptySession(),
		deleteTarget:                 emptySession(),
		lifecycle:                    newLifecycle(ctx),
		logger:                       *zerolog.Ctx(ctx),
		deps:                         deps,
	}, nil
}

func newTextInput(styles textinput.Styles) textinput.Model {
	input := textinput.New()
	input.Placeholder = ""
	input.Prompt = ""
	input.CharLimit = 256
	input.SetStyles(styles)

	return input
}

func newTextInputStyles(cfg *config.Config) textinput.Styles {
	styles := textinput.DefaultDarkStyles()
	styles.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	styles.Blurred.Text = styles.Focused.Text
	styles.Focused.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color(cfg.Theme.Muted))
	styles.Blurred.Placeholder = styles.Focused.Placeholder
	styles.Cursor.Color = lipgloss.Color("15")
	styles.Cursor.Shape = tea.CursorBlock
	styles.Cursor.Blink = true

	return styles
}

func (m *Model) Close() {
	if m == nil {
		return
	}

	m.agentGitRequest++
	m.stopAgentGit()
	m.lifecycle.Close()
}

func (m *Model) SetSize(width, height int) {
	if m == nil || width <= 0 || height <= 0 {
		return
	}

	m.width = width
	m.height = height
}

func (m *Model) SetInitialSessions(sessions []session.Session) {
	if m == nil || len(sessions) == 0 {
		return
	}

	m.applySessionRows(sessionLoadResultMsg{
		sessions:          sessions,
		originQueried:     false,
		originErr:         nil,
		status:            session.StatusComplete,
		catalogErr:        nil,
		agentSummaryErr:   nil,
		agentSummaries:    nil,
		agentObservation:  0,
		agentGit:          m.agentGit,
		remoteCatalog:     RemoteCatalog{GeneratedAt: time.Time{}, Connections: nil},
		remoteErr:         nil,
		remoteObservation: 0,
	})
	m.loadingSessions = false
	m.suppressInitialFrame = false
}

func (m *Model) DeferredSwitchTarget() (session.Session, bool) {
	return m.deferredSwitch, m.hasDeferredSwitch
}

func (m *Model) DeferredSwitchSession() string {
	if m.hasDeferredSwitch {
		return m.deferredSwitch.Name
	}

	return m.switchAfterQuit
}

func (m *Model) DeferredAttachTarget() (session.Session, bool) {
	return m.deferredAttach, m.hasDeferredAttach
}

func (m *Model) DeferredAttachSession() string {
	if m.hasDeferredAttach {
		return m.deferredAttach.Name
	}

	return ""
}

func (m *Model) DeferredSwitchPane() string {
	return m.paneAfterQuit
}

func (m *Model) DeferredRemoteAttach() (session.Session, string, bool) {
	return m.deferredRemoteAttach, m.remoteAttachPane, m.hasDeferredRemoteAttach
}

func (m *Model) DeferredOriginDetach() bool {
	return m.originDetachAfterQuit
}

func (m *Model) Init() tea.Cmd {
	if len(m.sessions) == 0 {
		m.loadingSessions = true
		m.suppressInitialFrame = m.mode == modePopup
	}

	commands := []tea.Cmd{
		m.textInput.Focus(),
		textinput.Blink,
		m.loadInitialSessionsCommand(),
		m.loadOriginSessionsCommand(),
		m.agentPositionTickCommand(),
	}

	if remoteSessionsEnabled(m.config, m.deps) {
		commands = append(commands, m.remoteStatusTickCommand())
	}

	if command := m.startRemoteCatalogWatchCommand(); command != nil {
		commands = append(commands, command)
	}

	if command := m.startAgentSummaryWatchCommand(); command != nil {
		commands = append(commands, command)
	}

	if m.agentSummaryWatchStarted {
		commands = append(commands, m.waitAgentSummaryUpdateCommand())
	}

	if m.config.AgentSessions.Enabled && m.config.AgentSessions.PopupCounts {
		commands = append(commands, m.loadAgentSummariesCommand(), m.agentSummaryTickCommand())
	}

	commands = append(commands, m.agentClockTickCommand(), m.agentGitTickCommand())

	return tea.Batch(commands...)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.lifecycle.IsClosed() {
		return m, nil
	}

	if handler := m.handleModelMessage(msg); handler != nil {
		return handler()
	}

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleKeyPressMsg(msg)
	case tea.MouseClickMsg:
		return m.handleMouseClickMsg(msg)
	case tea.MouseWheelMsg:
		return m.handleMouseWheelMsg(msg)
	case tea.MouseMotionMsg:
		return m.handleMouseMotionMsg(msg)
	default:
		return m.updateTextInput(msg)
	}
}

func (m *Model) View() tea.View {
	content := ""
	if !m.suppressInitialFrame {
		content = m.viewContent()
	}

	view := tea.NewView(content)

	view.AltScreen = m.mode != modeInline
	if m.mode != modeInline {
		view.MouseMode = tea.MouseModeAllMotion
	}

	return view
}

func (m *Model) operationContext() context.Context {
	return m.logger.WithContext(m.lifecycle)
}

func (m *Model) setErrorStatus(message string) {
	m.statusMessage = message
	m.statusLevel = statusError
}

func (m *Model) updateState(ctx context.Context, update session.StateUpdate) error {
	if m.lifecycle.IsClosed() || ctx.Err() != nil {
		return errModelClosed
	}

	if err := m.deps.State.UpdateModelState(ctx, update); err != nil {
		return oops.In("tui").With("operation", "save_state").Wrap(err)
	}

	if m.state != nil {
		update(m.state)
	}

	return nil
}

func (m *Model) handleKeyPressMsg(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if cmd := m.handleDialogKeys(msg); cmd != nil {
		return cmd()
	}

	if m.showHelp {
		m.showHelp = false

		return m, nil
	}

	if cmd := m.handleNavigationKeys(msg); cmd != nil {
		return cmd()
	}

	if cmd := m.handleActionKeys(msg); cmd != nil {
		return cmd()
	}

	if cmd := m.handleSearchKeys(msg); cmd != nil {
		return cmd()
	}

	return m, nil
}

func (m *Model) handleMouseClickMsg(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if cmd := m.handleMouseClick(msg.Mouse()); cmd != nil {
		return cmd()
	}

	return m, nil
}

func (m *Model) handleMouseWheelMsg(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	if cmd := m.handleMouseWheel(msg.Mouse()); cmd != nil {
		return cmd()
	}

	return m, nil
}

func (m *Model) handleMouseMotionMsg(msg tea.MouseMotionMsg) (tea.Model, tea.Cmd) {
	if cmd := m.handleMouseMotion(msg.Mouse()); cmd != nil {
		return cmd()
	}

	return m, nil
}

func (m *Model) updateTextInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	m.textInput, cmd = m.textInput.Update(msg)

	return m, cmd
}

func (m *Model) handleTickModelMessage(msg tea.Msg) func() (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case agentSummaryTickMsg:
		return func() (tea.Model, tea.Cmd) {
			return m, tea.Batch(m.loadAgentSummariesCommand(), m.agentSummaryTickCommand())
		}
	case agentGitTickMsg:
		return func() (tea.Model, tea.Cmd) { return m, m.loadAgentGitCommand() }
	case agentGitResultMsg:
		return func() (tea.Model, tea.Cmd) { return m, m.handleAgentGit(msg) }
	case agentPositionTickMsg:
		return func() (tea.Model, tea.Cmd) { return m, m.loadAgentPositionsCommand() }
	case agentPositionsMsg:
		return func() (tea.Model, tea.Cmd) {
			m.applyAgentPositions(msg)
			return m, m.agentPositionTickCommand()
		}
	case agentClockTickMsg:
		return func() (tea.Model, tea.Cmd) {
			return m, m.agentClockTickCommand()
		}
	case remoteStatusTickMsg:
		return func() (tea.Model, tea.Cmd) {
			m.handleRemoteStatusTick(msg.now)
			return m, m.remoteStatusTickCommand()
		}
	default:
		return nil
	}
}

func (m *Model) handleModelMessage(msg tea.Msg) func() (tea.Model, tea.Cmd) {
	if tickHandler := m.handleTickModelMessage(msg); tickHandler != nil {
		return tickHandler
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return func() (tea.Model, tea.Cmd) {
			m.width = msg.Width
			m.height = msg.Height

			return m, nil
		}

	case refreshMsg:
		return func() (tea.Model, tea.Cmd) {
			m.handleRefresh()

			return m, nil
		}

	case sessionLoadResultMsg:
		return func() (tea.Model, tea.Cmd) {
			return m, m.handleSessionLoadResult(msg)
		}
	case originSessionsMsg:
		return func() (tea.Model, tea.Cmd) {
			m.handleOriginSessions(msg)

			return m, nil
		}

	case agentSummariesMsg:
		return func() (tea.Model, tea.Cmd) {
			cmd := m.handleAgentSummaries(msg)

			if !m.agentSummaryWatchStarted {
				return m, cmd
			}

			return m, tea.Batch(cmd, m.waitAgentSummaryUpdateCommand())
		}

	case remoteCatalogMsg:
		return func() (tea.Model, tea.Cmd) {
			m.handleRemoteCatalog(msg)

			return m, m.waitRemoteCatalogUpdateCommand()
		}

	case sessionActionResultMsg:
		return func() (tea.Model, tea.Cmd) {
			return m.handleSessionActionResult(msg)
		}
	case stateUpdateResultMsg:
		return func() (tea.Model, tea.Cmd) {
			if msg.err != nil {
				logTUIFailure(m.operationContext(), tuiOperationSaveState, msg.err)
				m.setErrorStatusf("Failed to save state: %v", msg.err)
			}

			return m, nil
		}
	}

	return nil
}

func (m *Model) setDegradedSource(source string, message string) {
	if m.degradedSources == nil {
		m.degradedSources = map[string]string{}
	}

	m.degradedSources[source] = message
	m.applyDegradedStatus()
}

func (m *Model) clearDegradedSource(source string) {
	if m.degradedSources == nil {
		return
	}

	delete(m.degradedSources, source)
	m.applyDegradedStatus()
}

func (m *Model) applyDegradedStatus() {
	if m.statusLevel == statusError {
		return
	}

	if len(m.degradedSources) == 0 {
		if m.statusLevel == statusDegraded {
			m.statusMessage = ""
			m.statusLevel = statusNone
		}

		return
	}

	sources := slices.Sorted(maps.Keys(m.degradedSources))
	parts := make([]string, 0, len(sources))

	for _, source := range sources {
		parts = append(parts, m.degradedSources[source])
	}

	m.statusMessage = strings.Join(parts, "; ")
	m.statusLevel = statusDegraded
}

func emptySession() session.Session {
	var s session.Session

	return s
}

func (m *Model) sessionIdentityForName(sessionName string) session.Identity {
	var fallback session.Identity

	for _, row := range m.sessions {
		if row.Name != sessionName {
			continue
		}

		if row.Kind == session.KindLive {
			return row.SemanticIdentity()
		}

		if fallback.Name == "" {
			fallback = row.SemanticIdentity()
		}
	}

	if fallback.Name != "" {
		return fallback
	}

	return session.Identity{
		Origin:           "",
		ConnectionID:     "",
		Socket:           "",
		RepositorySource: "",
		Name:             sessionName,
	}
}
