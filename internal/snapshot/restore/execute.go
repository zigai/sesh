//go:build linux || darwin

package restore

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/integration/tmuxserver"
	"github.com/zigai/sesh/internal/snapshot/model"
)

const (
	MissingWorkingDirFail   MissingWorkingDir = "fail"
	MissingWorkingDirLaunch MissingWorkingDir = "launch_in_fallback"
	MissingWorkingDirShell  MissingWorkingDir = "shell_in_fallback"
)

const (
	defaultWindowWidth  = 80
	defaultWindowHeight = 24
	environmentProgram  = "/usr/bin/env"
	resumeShellProgram  = "/bin/sh"
)

var (
	errGroupLeaderNotFound      = errors.New("group leader session not found")
	errInitialPlacementNotFound = errors.New("session not found for initial window placement")
	errBootstrapNotFound        = errors.New("bootstrap link not found")
	errOwnerSessionNotFound     = errors.New("owner session not found")
	errTargetPaneNotFound       = errors.New("target pane not found")
	errPaneNotFound             = errors.New("pane not found")
	errWindowNotFound           = errors.New("window not found")
	errSessionNotFound          = errors.New("session not found")
	errRuntimeHandleUnusable    = errors.New("confirmed runtime identity is not usable")
	errAdoptedLayoutUnresolved  = errors.New("adopted window has no identifiable initial pane")
)

var tmuxPaneVariables = []string{"TMUX", "TMUX_PANE", "TERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION", "COLORTERM"}

type MissingWorkingDir string

type ExecuteConfig struct {
	LaunchProcesses   bool
	MissingWorkingDir MissingWorkingDir
}

type stepEffect struct {
	run func(ctx context.Context) (string, error)
}

type paneLaunch struct {
	program     gotmux.Program
	directory   string
	environment map[string]string
	enabled     bool
	cwdFallback bool
	reason      string
}

type planIndex struct {
	stepsByID            map[string]Step
	splitStepsByWindow   map[string][]*SplitPaneStep
	placeStepBySession   map[string]*PlaceInitialWindowStep
	initialPaneByWindow  map[string]string
	ownerSessionByWindow map[string]string
	panesByID            map[string]model.Pane
	sessionsByID         map[string]model.Session
	followersByLeader    map[string][]string
}

type Executor struct {
	server   *gotmux.Server
	manifest model.Manifest
	cfg      ExecuteConfig
	index    planIndex

	sessions   map[string]gotmux.Session
	bootstraps map[string]gotmux.WindowLink
	windows    map[string]gotmux.Window
	panes      map[string]gotmux.Pane

	receipts    map[string]string
	sessionLog  []string
	missing     []MissingRequirement
	paneResults map[string]PaneResult
}

type ExecutionReport struct {
	SessionsCreated []string             `json:"sessionsCreated"`
	WindowsCreated  []string             `json:"windowsCreated"`
	PanesCreated    []string             `json:"panesCreated"`
	Dispatched      []string             `json:"dispatched"`
	Adopted         []string             `json:"adopted"`
	Receipts        map[string]string    `json:"receipts"`
	Missing         []MissingRequirement `json:"missingRequirements"`
	Panes           []PaneResult         `json:"panes,omitempty"`
}

func (m MissingWorkingDir) fallsBack() bool {
	return m == MissingWorkingDirLaunch || m == MissingWorkingDirShell
}

func DefaultExecuteConfig() ExecuteConfig {
	return ExecuteConfig{
		LaunchProcesses:   false,
		MissingWorkingDir: MissingWorkingDirFail,
	}
}

func NewExecutor(server *gotmux.Server, manifest model.Manifest, cfg ExecuteConfig) *Executor {
	panesByID := make(map[string]model.Pane, len(manifest.Panes))
	for _, p := range manifest.Panes {
		panesByID[p.ID] = p
	}

	return &Executor{
		server:   server,
		manifest: manifest,
		cfg:      cfg,
		index: planIndex{
			stepsByID:            map[string]Step{},
			splitStepsByWindow:   map[string][]*SplitPaneStep{},
			placeStepBySession:   map[string]*PlaceInitialWindowStep{},
			initialPaneByWindow:  map[string]string{},
			ownerSessionByWindow: map[string]string{},
			panesByID:            panesByID,
			sessionsByID:         map[string]model.Session{},
			followersByLeader:    map[string][]string{},
		},
		sessions:    map[string]gotmux.Session{},
		bootstraps:  map[string]gotmux.WindowLink{},
		windows:     map[string]gotmux.Window{},
		panes:       map[string]gotmux.Pane{},
		receipts:    map[string]string{},
		sessionLog:  nil,
		missing:     nil,
		paneResults: map[string]PaneResult{},
	}
}

func PreflightCheckCollisions(ctx context.Context, server *gotmux.Server, plan Plan) error {
	return PreflightCheckCollisionsExcluding(ctx, server, plan, nil)
}

func PreflightCheckCollisionsExcluding(ctx context.Context, server *gotmux.Server, plan Plan, adoptedSessions map[string]bool) error {
	if server == nil {
		return oops.In("snapshot.restore").Code("endpoint_required").Wrap(tmuxserver.ErrEndpoint)
	}

	liveSessions, err := server.Sessions(ctx)
	if err != nil {
		return oops.In("snapshot.restore").Code("endpoint_required").With("operation", "preflight_collisions").Wrapf(err, "query live sessions")
	}

	if err := checkSessionCollisions(liveSessions, plan, adoptedSessions); err != nil {
		return err
	}

	return checkGroupCollisions(ctx, server, plan, adoptedSessions)
}

func PreflightCheckGroupCollision(ctx context.Context, server *gotmux.Server, groupName string) error {
	sessions, err := server.Sessions(ctx)
	if err != nil {
		return oops.In("snapshot.restore").
			Code("restore_conflict").
			With("operation", "preflight_collisions", "phase", "group", "group_name", groupName).
			Wrap(errors.Join(ErrGroupCollision, err))
	}

	for _, sess := range sessions {
		if sess.Name == groupName {
			return oops.In("snapshot.restore").
				Code("restore_conflict").
				With("operation", "preflight_collisions", "phase", "group", "group_name", groupName).
				Wrap(ErrGroupCollision)
		}

		if grp, ok := sess.Group.Get(); ok && grp == groupName {
			return oops.In("snapshot.restore").
				Code("restore_conflict").
				With("operation", "preflight_collisions", "phase", "group", "group_name", groupName).
				Wrap(ErrGroupCollision)
		}
	}

	if _, err := server.FindSession(ctx, groupName); err != nil {
		if !errors.Is(err, gotmux.ErrNotFound) {
			return oops.In("snapshot.restore").
				Code("restore_conflict").
				With("operation", "preflight_collisions", "phase", "group", "group_name", groupName).
				Wrap(errors.Join(ErrGroupCollision, err))
		}
	} else {
		return oops.In("snapshot.restore").
			Code("restore_conflict").
			With("operation", "preflight_collisions", "phase", "group", "group_name", groupName).
			Wrap(ErrGroupCollision)
	}

	return nil
}

func checkSessionCollisions(liveSessions []gotmux.SessionInfo, plan Plan, adoptedSessions map[string]bool) error {
	sessionNames := make(map[string]string, len(plan.SourceGraph.Sessions))
	for _, s := range plan.SourceGraph.Sessions {
		sessionNames[s.ID] = string(s.Name)
	}

	for _, sessID := range plan.ClosureSessions {
		if adoptedSessions[sessID] {
			continue
		}

		name := sessionNames[sessID]
		if name == "" {
			continue
		}

		for _, live := range liveSessions {
			if live.Name == name {
				return oops.In("snapshot.restore").
					Code("restore_conflict").
					With("operation", "preflight_collisions", "phase", "session").
					Wrapf(ErrSessionCollision, "session collision on target server")
			}
		}
	}

	return nil
}

func checkGroupCollisions(ctx context.Context, server *gotmux.Server, plan Plan, adoptedSessions map[string]bool) error {
	for _, step := range plan.Steps {
		create, ok := step.(*CreateSessionStep)
		if !ok || len(create.GroupName) == 0 {
			continue
		}

		if adoptedSessions[create.SessionID] {
			continue
		}

		grp := string(create.GroupName)
		if err := PreflightCheckGroupCollision(ctx, server, grp); err != nil {
			return err
		}
	}

	return nil
}

func (e *Executor) Execute(ctx context.Context, plan Plan, journal Journal) (ExecutionReport, error) {
	report := ExecutionReport{
		SessionsCreated: nil,
		WindowsCreated:  nil,
		PanesCreated:    nil,
		Dispatched:      nil,
		Adopted:         nil,
		Receipts:        nil,
		Missing:         nil,
		Panes:           nil,
	}

	if journal == nil {
		return report, oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "execute", "reason", "journal_required").
			Wrap(ErrJournalRequired)
	}

	e.index = indexPlan(plan)

	states, err := journal.Steps(ctx, plan.Steps)
	if err != nil {
		return report, oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "execute", "reason", "read_journal_failed").
			Wrapf(err, "read step journal")
	}

	if err := validateJournalStates(plan, states); err != nil {
		return report, err
	}

	e.seedReceipts(plan, states)
	e.noteUnsupportedLaunches(plan)

	adoptedSessions := adoptedSessionIDs(plan, states)

	if err := PreflightCheckCollisionsExcluding(ctx, e.server, plan, adoptedSessions); err != nil {
		return report, err
	}

	for _, step := range plan.Steps {
		if err := e.executeStep(ctx, step, states[step.ID()], journal, &report); err != nil {
			return e.abort(ctx, plan, &report, err)
		}
	}

	e.ensureAllPaneResults(plan)
	report.Receipts = e.cloneReceipts()
	report.SessionsCreated = e.restoredSessionIDs(plan)
	report.WindowsCreated, report.PanesCreated = e.restoredWindowAndPaneIDs(plan)
	report.Missing = slices.Clone(e.missing)
	report.Panes = e.collectedPaneResults(plan)

	return report, nil
}

func (e *Executor) executeStep(ctx context.Context, step Step, state StepState, journal Journal, report *ExecutionReport) error {
	if err := ctx.Err(); err != nil {
		return oops.In("snapshot.restore").
			Code("operation_canceled").
			With("operation", "execute_step", "step_id", step.ID()).
			Wrap(err)
	}

	switch state.State {
	case model.ActionStateConfirmed, model.ActionStateVerified:
		if err := e.installStep(ctx, step, state.RuntimeID); err != nil {
			return err
		}

		report.Adopted = append(report.Adopted, step.ID())

		return nil
	case model.ActionStatePlanned, model.ActionStateNotSent:
	case model.ActionStateIssued, model.ActionStateUnknown:
		return nil
	}

	effect, err := e.prepareStep(step)
	if err != nil {
		return err
	}

	runtimeID, err := e.dispatch(ctx, step, effect, journal)
	if err != nil {
		return err
	}

	if err := e.installStep(ctx, step, runtimeID); err != nil {
		return err
	}

	report.Dispatched = append(report.Dispatched, step.ID())

	return nil
}

func indexPlan(plan Plan) planIndex {
	index := planIndex{
		stepsByID:            make(map[string]Step, len(plan.Steps)),
		splitStepsByWindow:   map[string][]*SplitPaneStep{},
		placeStepBySession:   map[string]*PlaceInitialWindowStep{},
		initialPaneByWindow:  map[string]string{},
		ownerSessionByWindow: map[string]string{},
		panesByID:            map[string]model.Pane{},
		sessionsByID:         map[string]model.Session{},
		followersByLeader:    map[string][]string{},
	}

	for _, step := range plan.Steps {
		index.stepsByID[step.ID()] = step

		switch s := step.(type) {
		case *SplitPaneStep:
			index.splitStepsByWindow[s.WindowID] = append(index.splitStepsByWindow[s.WindowID], s)
		case *PlaceInitialWindowStep:
			index.placeStepBySession[s.SessionID] = s
		case *CreateWindowStep:
			index.initialPaneByWindow[s.WindowID] = s.InitialPaneID
			index.ownerSessionByWindow[s.WindowID] = s.OwnerSessionID
		case *CreateSessionStep:
			if s.GroupLeaderSessionID != "" {
				index.followersByLeader[s.GroupLeaderSessionID] = append(index.followersByLeader[s.GroupLeaderSessionID], s.SessionID)
			}

			if s.InitialWindowID != "" {
				index.initialPaneByWindow[s.InitialWindowID] = s.InitialPaneID
				index.ownerSessionByWindow[s.InitialWindowID] = s.SessionID
			}
		case *LinkWindowStep, *CloseBootstrapWindowStep, *ApplyLayoutStep,
			*SelectPaneStep, *ApplyZoomStep, *SelectWindowStep, *SetPaneTitleStep:
		}
	}

	for _, pane := range plan.SourceGraph.Panes {
		index.panesByID[pane.ID] = pane
	}

	for _, session := range plan.SourceGraph.Sessions {
		index.sessionsByID[session.ID] = session
	}

	return index
}

func validateJournalStates(plan Plan, states map[string]StepState) error {
	for _, step := range plan.Steps {
		state, ok := states[step.ID()]
		if !ok || state.State == "" {
			return oops.In("snapshot.restore").
				Code("restore_failed").
				With("operation", "execute", "reason", "journal_incomplete").
				Wrap(ErrJournalIncomplete)
		}

		switch state.State {
		case model.ActionStateIssued, model.ActionStateUnknown:
			return oops.In("snapshot.restore").
				Code("operation_uncertain").
				With("operation", "execute", "reason", "prior_effect_uncertain").
				Wrap(ErrPriorUncertainty)
		case model.ActionStatePlanned, model.ActionStateNotSent, model.ActionStateConfirmed, model.ActionStateVerified:
		default:
			return oops.In("snapshot.restore").
				Code("restore_failed").
				With("operation", "execute", "reason", "journal_state_unsupported").
				Wrap(ErrJournalIncomplete)
		}
	}

	return nil
}

func adoptedSessionIDs(plan Plan, states map[string]StepState) map[string]bool {
	adopted := map[string]bool{}

	for _, step := range plan.Steps {
		create, ok := step.(*CreateSessionStep)
		if !ok {
			continue
		}

		switch states[step.ID()].State {
		case model.ActionStateConfirmed, model.ActionStateVerified:
			adopted[create.SessionID] = true
		case model.ActionStatePlanned, model.ActionStateIssued, model.ActionStateNotSent, model.ActionStateUnknown:
		}
	}

	return adopted
}

func (e *Executor) seedReceipts(plan Plan, states map[string]StepState) {
	for _, step := range plan.Steps {
		if id := states[step.ID()].RuntimeID; id != "" {
			e.receipts[step.ID()] = id
		}
	}
}

func (e *Executor) noteUnsupportedLaunches(plan Plan) {
	if !e.cfg.LaunchProcesses {
		return
	}

	for _, pane := range plan.SourceGraph.Panes {
		switch pane.Launch.Recovery {
		case model.RecoveryNonrestartable:
			if pane.Launch.Reason != "" {
				e.note("launch", model.Reason(pane.Launch.Reason))
			} else {
				e.note("launch", model.ReasonNonrestartablePane)
			}
		case model.RecoveryUnverified:
			if len(pane.Launch.Executable) > 0 && len(pane.Launch.Arguments) > 0 {
				e.note("launch", model.ReasonApplicationRestartUnsupported)
			}
		case model.RecoveryRestart, model.RecoveryResume:
		}
	}
}

func (e *Executor) note(domain string, reason model.Reason) {
	entry := MissingRequirement{Domain: domain, Status: "partial", Reason: reason}
	if slices.Contains(e.missing, entry) {
		return
	}

	e.missing = append(e.missing, entry)
	sort.Slice(e.missing, func(i, j int) bool { return e.missing[i].Domain < e.missing[j].Domain })
}

func (e *Executor) cloneReceipts() map[string]string {
	if len(e.receipts) == 0 {
		return map[string]string{}
	}

	out := make(map[string]string, len(e.receipts))
	maps.Copy(out, e.receipts)

	return out
}

func (e *Executor) restoredSessionIDs(plan Plan) []string {
	ids := make([]string, 0, len(plan.ClosureSessions))

	for _, sessionID := range plan.SourceGraph.Sessions {
		for _, step := range plan.Steps {
			if create, ok := step.(*CreateSessionStep); ok && create.SessionID == sessionID.ID {
				if id := e.receipts[step.ID()]; id != "" {
					ids = append(ids, id)
				}

				break
			}
		}
	}

	return ids
}

func (e *Executor) restoredWindowAndPaneIDs(plan Plan) ([]string, []string) {
	windows := make([]string, 0, len(plan.SourceGraph.Windows))
	panes := make([]string, 0, len(plan.SourceGraph.Panes))

	for _, step := range plan.Steps {
		id := e.receipts[step.ID()]
		if id == "" {
			continue
		}

		switch step.(type) {
		case *CreateWindowStep:
			windows = append(windows, id)
		case *SplitPaneStep:
			panes = append(panes, id)
		case *CreateSessionStep, *PlaceInitialWindowStep, *LinkWindowStep,
			*CloseBootstrapWindowStep, *ApplyLayoutStep, *SelectPaneStep,
			*ApplyZoomStep, *SelectWindowStep, *SetPaneTitleStep:
		}
	}

	return windows, panes
}

func (e *Executor) abort(_ context.Context, plan Plan, report *ExecutionReport, err error) (ExecutionReport, error) {
	report.Receipts = e.cloneReceipts()
	report.SessionsCreated = e.restoredSessionIDs(plan)
	report.WindowsCreated, report.PanesCreated = e.restoredWindowAndPaneIDs(plan)
	report.Missing = slices.Clone(e.missing)

	return *report, err
}

func (e *Executor) dispatch(ctx context.Context, step Step, effect stepEffect, journal Journal) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", oops.In("snapshot.restore").
			Code("operation_canceled").
			With("operation", "dispatch_step", "step_id", step.ID()).
			Wrap(err)
	}

	if err := journal.Intent(ctx, step); err != nil {
		return "", oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "journal_intent", "reason", "intent_not_durable").
			Wrapf(err, "record step intent")
	}

	runtimeID, runErr := effect.run(ctx)
	if runErr == nil {
		if runtimeID == "" {
			return "", e.uncertain(ctx, step, journal, ErrRuntimeIdentityUnavailable)
		}

		if err := e.confirm(ctx, journal, step, runtimeID); err != nil {
			return "", err
		}

		return runtimeID, nil
	}

	return e.handleFailedEffect(ctx, step, journal, runErr)
}

func (e *Executor) handleFailedEffect(ctx context.Context, step Step, journal Journal, runErr error) (string, error) {
	outcome := classifyMutation(runErr)

	switch outcome.Effect {
	case gotmux.EffectConfirmed:
		return "", e.uncertain(ctx, step, journal, runErr)
	case gotmux.EffectNotSent:
		recordCtx, cancel := detach(ctx)
		defer cancel()

		if err := journal.NotSent(recordCtx, step); err != nil {
			return "", e.uncertain(ctx, step, journal, errors.Join(runErr, err))
		}

		return "", &mutationError{step: step.ID(), err: runErr}
	case gotmux.EffectUnspecified, gotmux.EffectUnknown, gotmux.EffectRejected:
		return "", e.uncertain(ctx, step, journal, runErr)
	}

	return "", e.uncertain(ctx, step, journal, runErr)
}

func (e *Executor) confirm(ctx context.Context, journal Journal, step Step, runtimeID string) error {
	recordCtx, cancel := detach(ctx)
	defer cancel()

	if err := journal.Confirmed(recordCtx, step, runtimeID); err != nil {
		return e.uncertain(ctx, step, journal, err)
	}

	return nil
}

func (e *Executor) uncertain(ctx context.Context, step Step, journal Journal, cause error) error {
	recordCtx, cancel := detach(ctx)
	defer cancel()

	if err := journal.Unknown(recordCtx, step); err != nil {
		cause = errors.Join(cause, err)
	}

	return &uncertainError{step: step.ID(), err: cause}
}

func (e *Executor) installStep(ctx context.Context, step Step, runtimeID string) error {
	if runtimeID == "" {
		return oops.In("snapshot.restore").
			Code("operation_uncertain").
			With("operation", "install_step", "reason", "runtime_identity_missing").
			Wrapf(errRuntimeHandleUnusable, "step %s", step.ID())
	}

	e.receipts[step.ID()] = runtimeID

	switch s := step.(type) {
	case *CreateSessionStep:
		return e.installSession(ctx, *s, runtimeID)
	case *PlaceInitialWindowStep:
		return e.installInitialPlacement(ctx, *s, runtimeID)
	case *CreateWindowStep:
		return e.installWindow(ctx, *s, runtimeID)
	case *SplitPaneStep:
		return e.installPane(ctx, *s, runtimeID)
	case *CloseBootstrapWindowStep:
		delete(e.bootstraps, s.SessionID)

		return nil
	case *LinkWindowStep, *ApplyLayoutStep, *SelectPaneStep, *ApplyZoomStep, *SelectWindowStep, *SetPaneTitleStep:
		return nil
	}

	return planFailure("execution", "unknown_step_kind", ErrInvalid)
}

func (e *Executor) installSession(ctx context.Context, step CreateSessionStep, runtimeID string) error {
	session, err := e.server.Session(ctx, gotmux.SessionID(runtimeID))
	if err != nil {
		return oops.In("snapshot.restore").
			Code("operation_uncertain").
			With("operation", "resolve_session", "reason", "recorded_session_unavailable").
			Wrap(err)
	}

	e.sessions[step.SessionID] = session
	e.sessionLog = append(e.sessionLog, step.SessionID)

	if step.GroupLeaderSessionID != "" {
		return nil
	}

	if step.InitialWindowID == "" && !step.BootstrapWindow {
		return nil
	}

	link, found, err := e.sessionInitialLink(ctx, step)
	if err != nil {
		return err
	}

	if !found {
		return nil
	}

	e.bootstraps[step.SessionID] = link

	if step.InitialWindowID == "" {
		return nil
	}

	window := link.Window()
	e.windows[step.InitialWindowID] = window

	pane, err := e.initialPaneFromConfirmedSplits(ctx, step.InitialWindowID, window)
	if err != nil {
		return err
	}

	e.panes[step.InitialPaneID] = pane

	return nil
}

func (e *Executor) sessionInitialLink(ctx context.Context, step CreateSessionStep) (gotmux.WindowLink, bool, error) {
	session := e.sessions[step.SessionID]

	placement, ok := e.index.placeStepBySession[step.SessionID]
	if !ok {
		return gotmux.WindowLink{}, false, oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "install_session", "reason", "initial_placement_missing").
			Wrapf(errInitialPlacementNotFound, "%s", step.SessionID)
	}

	if windowID := e.receipts[placement.ID()]; windowID != "" {
		link, err := e.linkAt(ctx, session, placement.Index, gotmux.WindowID(windowID))
		if err != nil && step.InitialWindowID == "" && errors.Is(err, errRuntimeHandleUnusable) {
			return gotmux.WindowLink{}, false, nil
		}

		return link, err == nil, err
	}

	return e.unplacedInitialLink(ctx, session)
}

func (e *Executor) unplacedInitialLink(ctx context.Context, session gotmux.Session) (gotmux.WindowLink, bool, error) {
	_, links, err := session.WindowInfos(ctx)
	if err != nil {
		return gotmux.WindowLink{}, false, oops.In("snapshot.restore").
			Code("operation_uncertain").
			With("operation", "resolve_link").
			Wrap(err)
	}

	if len(links) != 1 {
		return gotmux.WindowLink{}, false, oops.In("snapshot.restore").
			Code("operation_uncertain").
			With("operation", "install_session", "reason", "initial_window_ambiguous").
			Wrapf(errBootstrapNotFound, "session %s", session.ID())
	}

	return links[0].Handle(), true, nil
}

func (e *Executor) installInitialPlacement(ctx context.Context, step PlaceInitialWindowStep, runtimeID string) error {
	session, ok := e.sessions[step.SessionID]
	if !ok {
		return oops.In("snapshot.restore").
			Code("operation_uncertain").
			With("operation", "install_placement", "reason", "session_not_adopted").
			Wrapf(errInitialPlacementNotFound, "%s", step.SessionID)
	}

	link, err := e.linkAt(ctx, session, step.Index, gotmux.WindowID(runtimeID))
	if err != nil {
		if step.WindowID == "" && errors.Is(err, errRuntimeHandleUnusable) {
			return nil
		}

		return err
	}

	e.bootstraps[step.SessionID] = link

	return nil
}

func (e *Executor) linkAt(ctx context.Context, session gotmux.Session, index int, windowID gotmux.WindowID) (gotmux.WindowLink, error) {
	_, links, err := session.WindowInfos(ctx)
	if err != nil {
		return gotmux.WindowLink{}, oops.In("snapshot.restore").
			Code("operation_uncertain").
			With("operation", "resolve_link").
			Wrap(err)
	}

	for _, link := range links {
		if link.Index == index && link.WindowID == windowID {
			return link.Handle(), nil
		}
	}

	return gotmux.WindowLink{}, oops.In("snapshot.restore").
		Code("operation_uncertain").
		With("operation", "resolve_link", "reason", "recorded_link_unavailable").
		Wrapf(errRuntimeHandleUnusable, "session %s index %d", session.ID(), index)
}

func (e *Executor) installWindow(ctx context.Context, step CreateWindowStep, runtimeID string) error {
	window, err := e.server.Window(ctx, gotmux.WindowID(runtimeID))
	if err != nil {
		return oops.In("snapshot.restore").
			Code("operation_uncertain").
			With("operation", "resolve_window", "reason", "recorded_window_unavailable").
			Wrap(err)
	}

	e.windows[step.WindowID] = window

	pane, err := e.initialPaneFromConfirmedSplits(ctx, step.WindowID, window)
	if err != nil {
		return err
	}

	e.panes[step.InitialPaneID] = pane

	return nil
}

func (e *Executor) installPane(ctx context.Context, step SplitPaneStep, runtimeID string) error {
	pane, err := e.server.Pane(ctx, gotmux.PaneID(runtimeID))
	if err != nil {
		return oops.In("snapshot.restore").
			Code("operation_uncertain").
			With("operation", "resolve_pane", "reason", "recorded_pane_unavailable").
			Wrap(err)
	}

	e.panes[step.PaneID] = pane

	return nil
}

func (e *Executor) initialPaneFromConfirmedSplits(ctx context.Context, windowID string, window gotmux.Window) (gotmux.Pane, error) {
	produced := e.confirmedSplitPaneIDs(windowID)

	paneID, err := initialPaneIDOf(ctx, window, windowID, produced)
	if err != nil {
		return gotmux.Pane{}, err
	}

	return e.paneHandle(ctx, paneID)
}

func (e *Executor) confirmedSplitPaneIDs(windowID string) map[gotmux.PaneID]bool {
	produced := map[gotmux.PaneID]bool{}

	for _, step := range e.index.splitStepsByWindow[windowID] {
		if id := e.receipts[step.ID()]; id != "" {
			produced[gotmux.PaneID(id)] = true
		}
	}

	return produced
}

func (e *Executor) paneHandle(ctx context.Context, id gotmux.PaneID) (gotmux.Pane, error) {
	pane, err := e.server.Pane(ctx, id)
	if err != nil {
		return gotmux.Pane{}, oops.In("snapshot.restore").
			Code("operation_uncertain").
			With("operation", "resolve_pane", "reason", "recorded_pane_unavailable").
			Wrap(err)
	}

	return pane, nil
}

func (e *Executor) prepareStep(step Step) (stepEffect, error) {
	switch s := step.(type) {
	case *CreateSessionStep:
		return e.prepareCreateSession(*s)
	case *PlaceInitialWindowStep:
		return e.preparePlaceInitialWindow(*s)
	case *CreateWindowStep:
		return e.prepareCreateWindow(*s)
	case *SplitPaneStep:
		return e.prepareSplitPane(*s)
	case *LinkWindowStep:
		return e.prepareLinkWindow(*s)
	case *CloseBootstrapWindowStep:
		return e.prepareCloseBootstrap(*s)
	case *ApplyLayoutStep:
		return e.prepareApplyLayout(*s)
	case *SelectPaneStep:
		return e.prepareSelectPane(*s)
	case *ApplyZoomStep:
		return e.prepareApplyZoom(*s)
	case *SelectWindowStep:
		return e.prepareSelectWindow(*s)
	case *SetPaneTitleStep:
		return e.prepareSetPaneTitle(*s)
	}

	return stepEffect{}, planFailure("execution", "unknown_step_kind", ErrInvalid)
}

func (e *Executor) prepareCreateSession(step CreateSessionStep) (stepEffect, error) {
	if step.GroupLeaderSessionID != "" {
		return e.prepareFollowerSession(step)
	}

	return e.prepareStandaloneSession(step)
}

func (e *Executor) prepareFollowerSession(step CreateSessionStep) (stepEffect, error) {
	leader, ok := e.sessions[step.GroupLeaderSessionID]
	if !ok {
		return stepEffect{}, oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "prepare_step", "step_id", step.SessionID).
			Wrapf(errGroupLeaderNotFound, "%s", step.GroupLeaderSessionID)
	}

	groupTarget := string(step.GroupName)
	leaderInfo := e.index.sessionsByID[step.GroupLeaderSessionID]
	targetLeaderName := string(leaderInfo.Name)

	if groupTarget == "" {
		groupTarget = targetLeaderName
		if groupTarget == "" {
			groupTarget = string(step.Name)
		}
	}

	options := gotmux.NewSessionOptions{
		Name:    string(step.Name),
		Dir:     "",
		Window:  "",
		Program: gotmux.Program{},
		Env:     nil,
		TmuxEnv: nil,
		Size:    gotmux.Size{Width: 0, Height: 0},
		Start:   gotmux.StartPolicyExistingOnly,
		Group:   groupTarget,
	}

	return stepEffect{
		run: func(ctx context.Context) (string, error) {
			session, err := e.server.NewSession(ctx, options)
			if err != nil {
				return "", fmt.Errorf("new group follower session %s: %w", step.SessionID, err)
			}

			if targetLeaderName != "" && targetLeaderName != groupTarget {
				if err := leader.Rename(ctx, targetLeaderName); err != nil {
					return string(session.ID()), &appliedError{runtimeID: string(session.ID()), err: err}
				}
			}

			return string(session.ID()), nil
		},
	}, nil
}

func (e *Executor) prepareStandaloneSession(step CreateSessionStep) (stepEffect, error) {
	options, launch, hasPane, err := e.standaloneSessionOptions(step)
	if err != nil {
		return stepEffect{}, err
	}

	paneInfo := e.index.panesByID[step.InitialPaneID]
	windowName := string(step.InitialWindowName)

	return stepEffect{
		run: func(ctx context.Context) (string, error) {
			session, err := e.server.NewSession(ctx, options)
			if err != nil {
				return "", fmt.Errorf("new session %s: %w", step.SessionID, err)
			}

			if err := e.alignInitialWindow(ctx, session, step, windowName); err != nil {
				return string(session.ID()), &appliedError{runtimeID: string(session.ID()), err: err}
			}

			runtimePaneID, err := e.launchSessionInitialPane(ctx, session, step.SessionID, launch)
			if err != nil {
				return string(session.ID()), &appliedError{runtimeID: string(session.ID()), err: err}
			}

			if hasPane && runtimePaneID != "" {
				e.recordPaneResult(step.InitialPaneID, runtimePaneID, paneInfo, launch)
			}

			return string(session.ID()), nil
		},
	}, nil
}

func (e *Executor) standaloneSessionOptions(step CreateSessionStep) (gotmux.NewSessionOptions, paneLaunch, bool, error) {
	size := gotmux.Size{Width: defaultWindowWidth, Height: defaultWindowHeight}
	if step.Width > 0 && step.Height > 0 {
		size = gotmux.Size{Width: step.Width, Height: step.Height}
	}

	hasFollowers := len(e.index.followersByLeader[step.SessionID]) > 0
	sessionName := string(step.Name)

	if hasFollowers && len(step.GroupName) > 0 && string(step.GroupName) != string(step.Name) {
		sessionName = string(step.GroupName)
	}

	options := gotmux.NewSessionOptions{
		Name:    sessionName,
		Dir:     "",
		Window:  "",
		Program: gotmux.Program{},
		Env:     nil,
		TmuxEnv: nil,
		Size:    size,
		Start:   gotmux.StartPolicyExistingOnly,
		Group:   "",
	}

	if len(step.GroupName) == 0 && len(step.InitialWindowName) > 0 {
		options.Window = string(step.InitialWindowName)
	}

	paneInfo, hasPane := e.index.panesByID[step.InitialPaneID]
	launch := disabledPaneLaunch()

	if hasPane {
		dir, cwdFallback, err := e.resolveWorkingDir(paneInfo)
		if err != nil {
			return gotmux.NewSessionOptions{}, disabledPaneLaunch(), false, err
		}

		options.Dir = dir

		launch, err = e.preparePaneLaunch(paneInfo, step.SessionID)
		if err != nil {
			return gotmux.NewSessionOptions{}, disabledPaneLaunch(), false, err
		}

		launch.cwdFallback = launch.cwdFallback || cwdFallback
	}

	return options, launch, hasPane, nil
}

func (e *Executor) alignInitialWindow(ctx context.Context, session gotmux.Session, step CreateSessionStep, windowName string) error {
	if step.InitialWindowID == "" {
		return nil
	}

	_, links, err := session.WindowInfos(ctx)
	if err != nil {
		return fmt.Errorf("bootstrap window links for session %s: %w", step.SessionID, err)
	}

	if len(links) != 1 {
		return fmt.Errorf("bootstrap window links for session %s: %w", step.SessionID, errBootstrapNotFound)
	}

	window := links[0].Handle().Window()

	if info, infoErr := window.Info(ctx); infoErr == nil && info.Name != windowName {
		if err := window.Rename(ctx, windowName); err != nil {
			return fmt.Errorf("rename initial window %s: %w", step.InitialWindowID, err)
		}
	}

	return nil
}

func (e *Executor) preparePlaceInitialWindow(step PlaceInitialWindowStep) (stepEffect, error) {
	session, ok := e.sessions[step.SessionID]
	if !ok {
		return stepEffect{}, oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "prepare_step", "step_id", step.SessionID).
			Wrapf(errInitialPlacementNotFound, "%s", step.SessionID)
	}

	bootLink, ok := e.bootstraps[step.SessionID]
	if !ok {
		return stepEffect{}, oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "prepare_step", "step_id", step.SessionID).
			Wrapf(errBootstrapNotFound, "%s", step.SessionID)
	}

	return stepEffect{
		run: func(ctx context.Context) (string, error) {
			info, err := bootLink.Info(ctx)
			if err != nil {
				return "", fmt.Errorf("bootstrap window info: %w", err)
			}

			if info.Index == step.Index {
				return string(bootLink.Window().ID()), nil
			}

			idx := step.Index

			moved, err := bootLink.Move(ctx, session, gotmux.LinkOptions{
				Index:   &idx,
				Select:  false,
				Replace: false,
			})
			if err != nil {
				return "", fmt.Errorf("move initial window to %d: %w", step.Index, err)
			}

			return string(moved.Window().ID()), nil
		},
	}, nil
}

func (e *Executor) prepareCreateWindow(step CreateWindowStep) (stepEffect, error) {
	session, ok := e.sessions[step.OwnerSessionID]
	if !ok {
		return stepEffect{}, oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "prepare_step", "step_id", step.WindowID).
			Wrapf(errOwnerSessionNotFound, "%s", step.OwnerSessionID)
	}

	options, size, launch, err := e.newWindowOptions(step)
	if err != nil {
		return stepEffect{}, err
	}

	windowName := string(step.Name)

	return stepEffect{
		run: func(ctx context.Context) (string, error) {
			return e.createWindow(ctx, session, step, options, size, windowName, launch)
		},
	}, nil
}

func (e *Executor) newWindowOptions(step CreateWindowStep) (gotmux.NewWindowOptions, gotmux.Size, paneLaunch, error) {
	idx := step.Index

	options := gotmux.NewWindowOptions{
		Name:    string(step.Name),
		Dir:     "",
		Program: gotmux.Program{},
		Env:     nil,
		TmuxEnv: nil,
		Index:   &idx,
		Before:  false,
		After:   false,
		Select:  false,
	}

	paneInfo, hasPane := e.index.panesByID[step.InitialPaneID]
	launch := disabledPaneLaunch()

	if hasPane {
		dir, cwdFallback, err := e.resolveWorkingDir(paneInfo)
		if err != nil {
			return gotmux.NewWindowOptions{}, gotmux.Size{}, paneLaunch{}, err
		}

		options.Dir = dir

		launch, err = e.preparePaneLaunch(paneInfo, step.OwnerSessionID)
		if err != nil {
			return gotmux.NewWindowOptions{}, gotmux.Size{}, paneLaunch{}, err
		}

		launch.cwdFallback = launch.cwdFallback || cwdFallback
	}

	size := gotmux.Size{Width: defaultWindowWidth, Height: defaultWindowHeight}
	if step.Width > 0 && step.Height > 0 {
		size = gotmux.Size{Width: step.Width, Height: step.Height}
	}

	return options, size, launch, nil
}

func (e *Executor) createWindow(
	ctx context.Context,
	session gotmux.Session,
	step CreateWindowStep,
	opts gotmux.NewWindowOptions,
	size gotmux.Size,
	windowName string,
	launch paneLaunch,
) (string, error) {
	link, err := session.NewWindow(ctx, opts)
	if err != nil {
		return "", fmt.Errorf("new window %s in session %s: %w", step.WindowID, step.OwnerSessionID, err)
	}

	window := link.Window()

	info, infoErr := window.Info(ctx)
	if infoErr != nil {
		return string(window.ID()), &appliedError{runtimeID: string(window.ID()), err: fmt.Errorf("window info %s: %w", step.WindowID, infoErr)}
	}

	if info.Width != size.Width || info.Height != size.Height {
		if err := window.Resize(ctx, size); err != nil {
			return string(window.ID()), &appliedError{runtimeID: string(window.ID()), err: fmt.Errorf("resize window %s: %w", step.WindowID, err)}
		}
	}

	if info.Name != windowName {
		if err := window.Rename(ctx, windowName); err != nil {
			return string(window.ID()), &appliedError{runtimeID: string(window.ID()), err: fmt.Errorf("rename window %s: %w", step.WindowID, err)}
		}
	}

	pane, err := window.ActivePane(ctx)
	if err != nil {
		return string(window.ID()), &appliedError{runtimeID: string(window.ID()), err: fmt.Errorf("resolve initial pane for window %s: %w", step.WindowID, err)}
	}

	if err := respawnPane(ctx, pane, launch); err != nil {
		return string(window.ID()), &appliedError{runtimeID: string(window.ID()), err: fmt.Errorf("launch initial pane for window %s: %w", step.WindowID, err)}
	}

	if step.InitialPaneID != "" {
		if paneInfo, ok := e.index.panesByID[step.InitialPaneID]; ok {
			e.recordPaneResult(step.InitialPaneID, string(pane.ID()), paneInfo, launch)
		}
	}

	return string(window.ID()), nil
}

func (e *Executor) prepareSplitPane(step SplitPaneStep) (stepEffect, error) {
	targetPane, ok := e.panes[step.TargetPaneID]
	if !ok {
		return stepEffect{}, oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "prepare_step", "step_id", step.PaneID).
			Wrapf(errTargetPaneNotFound, "%s", step.TargetPaneID)
	}

	direction := gotmux.DirectionHorizontal
	cells := step.SubtreeWidth

	if step.Axis == "y" {
		direction = gotmux.DirectionVertical
		cells = step.SubtreeHeight
	}

	options := gotmux.SplitOptions{
		Direction:           direction,
		Size:                gotmux.SplitSize{Cells: cells, Percent: 0},
		Dir:                 "",
		Program:             gotmux.Program{},
		Env:                 nil,
		TmuxEnv:             nil,
		Select:              false,
		Before:              false,
		FullSize:            false,
		KillTarget:          false,
		Zoom:                false,
		Title:               "",
		BorderLines:         "",
		Style:               "",
		ActiveBorderStyle:   "",
		InactiveBorderStyle: "",
		Message:             "",
	}

	paneInfo, hasPane := e.index.panesByID[step.PaneID]
	launch := disabledPaneLaunch()

	if hasPane {
		dir, cwdFallback, err := e.resolveWorkingDir(paneInfo)
		if err != nil {
			return stepEffect{}, err
		}

		options.Dir = dir

		launch, err = e.preparePaneLaunch(paneInfo, e.index.ownerSessionByWindow[step.WindowID])
		if err != nil {
			return stepEffect{}, err
		}

		launch.cwdFallback = launch.cwdFallback || cwdFallback
	}

	title := string(step.Title)

	return stepEffect{
		run: func(ctx context.Context) (string, error) {
			newPane, err := targetPane.Split(ctx, options)
			if err != nil {
				return "", fmt.Errorf("split pane %s: %w", step.PaneID, err)
			}

			if err := newPane.SetTitle(ctx, title); err != nil {
				return string(newPane.ID()), &appliedError{runtimeID: string(newPane.ID()), err: fmt.Errorf("set title for pane %s: %w", step.PaneID, err)}
			}

			if err := respawnPane(ctx, newPane, launch); err != nil {
				return string(newPane.ID()), &appliedError{runtimeID: string(newPane.ID()), err: fmt.Errorf("launch pane %s: %w", step.PaneID, err)}
			}

			if hasPane {
				e.recordPaneResult(step.PaneID, string(newPane.ID()), paneInfo, launch)
			}

			return string(newPane.ID()), nil
		},
	}, nil
}

func (e *Executor) prepareSetPaneTitle(step SetPaneTitleStep) (stepEffect, error) {
	pane, ok := e.panes[step.PaneID]
	if !ok {
		return stepEffect{}, oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "prepare_step", "step_id", step.PaneID).
			Wrapf(errPaneNotFound, "%s", step.PaneID)
	}

	title := string(step.Title)

	return stepEffect{
		run: func(ctx context.Context) (string, error) {
			if err := pane.SetTitle(ctx, title); err != nil {
				return "", fmt.Errorf("set title for pane %s: %w", step.PaneID, err)
			}

			return string(pane.ID()), nil
		},
	}, nil
}

func (e *Executor) prepareCloseBootstrap(step CloseBootstrapWindowStep) (stepEffect, error) {
	bootLink, ok := e.bootstraps[step.SessionID]
	if !ok {
		return stepEffect{}, oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "prepare_step", "step_id", step.SessionID).
			Wrapf(errBootstrapNotFound, "%s", step.SessionID)
	}

	return stepEffect{
		run: func(ctx context.Context) (string, error) {
			if err := bootLink.UnlinkWith(ctx, gotmux.UnlinkOptions{Force: true}); err != nil {
				return "", fmt.Errorf("close bootstrap window for %s: %w", step.SessionID, err)
			}

			return string(bootLink.Window().ID()), nil
		},
	}, nil
}

func (e *Executor) prepareLinkWindow(step LinkWindowStep) (stepEffect, error) {
	session, ok := e.sessions[step.SessionID]
	if !ok {
		return stepEffect{}, oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "prepare_step", "step_id", step.SessionID).
			Wrapf(errSessionNotFound, "%s", step.SessionID)
	}

	window, ok := e.windows[step.WindowID]
	if !ok {
		return stepEffect{}, oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "prepare_step", "step_id", step.WindowID).
			Wrapf(errWindowNotFound, "%s", step.WindowID)
	}

	idx := step.Index

	return stepEffect{
		run: func(ctx context.Context) (string, error) {
			if _, err := window.Link(ctx, session, gotmux.LinkOptions{
				Index:   &idx,
				Select:  false,
				Replace: false,
			}); err != nil {
				return "", fmt.Errorf("link window %s to %s:%d: %w", step.WindowID, step.SessionID, idx, err)
			}

			return string(window.ID()), nil
		},
	}, nil
}

func (e *Executor) prepareApplyLayout(step ApplyLayoutStep) (stepEffect, error) {
	window, ok := e.windows[step.WindowID]
	if !ok {
		return stepEffect{}, oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "prepare_step", "step_id", step.WindowID).
			Wrapf(errWindowNotFound, "%s", step.WindowID)
	}

	paneMap := make(map[string]gotmux.PaneID, len(e.panes))
	for id, pane := range e.panes {
		paneMap[id] = pane.ID()
	}

	encoded, err := tmuxserver.EncodeLayout(step.Layout, paneMap)
	if err != nil {
		return stepEffect{}, oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "prepare_step", "step_id", step.WindowID).
			Wrapf(err, "encode layout for window %s", step.WindowID)
	}

	return stepEffect{
		run: func(ctx context.Context) (string, error) {
			if err := window.SelectLayout(ctx, encoded); err != nil {
				return "", fmt.Errorf("select layout for window %s: %w", step.WindowID, err)
			}

			return string(window.ID()), nil
		},
	}, nil
}

func (e *Executor) prepareSelectPane(step SelectPaneStep) (stepEffect, error) {
	pane, ok := e.panes[step.PaneID]
	if !ok {
		return stepEffect{}, oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "prepare_step", "step_id", step.PaneID).
			Wrapf(errPaneNotFound, "%s", step.PaneID)
	}

	var last gotmux.Pane

	if step.LastPaneID != "" {
		last, ok = e.panes[step.LastPaneID]
		if !ok {
			return stepEffect{}, oops.In("snapshot.restore").
				Code("restore_failed").
				With("operation", "prepare_step", "step_id", step.LastPaneID).
				Wrapf(errPaneNotFound, "%s", step.LastPaneID)
		}
	}

	return stepEffect{
		run: func(ctx context.Context) (string, error) {
			// Selecting the last pane first leaves it on top of tmux's last-pane stack.
			if step.LastPaneID != "" {
				if err := last.Select(ctx); err != nil {
					return "", fmt.Errorf("select last pane %s: %w", step.LastPaneID, err)
				}
			}

			if err := pane.Select(ctx); err != nil {
				return "", fmt.Errorf("select pane %s: %w", step.PaneID, err)
			}

			return string(pane.ID()), nil
		},
	}, nil
}

func (e *Executor) prepareApplyZoom(step ApplyZoomStep) (stepEffect, error) {
	pane, ok := e.panes[step.PaneID]
	if !ok {
		return stepEffect{}, oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "prepare_step", "step_id", step.PaneID).
			Wrapf(errPaneNotFound, "%s", step.PaneID)
	}

	return stepEffect{
		run: func(ctx context.Context) (string, error) {
			if err := pane.ToggleZoom(ctx); err != nil {
				return "", fmt.Errorf("toggle zoom for pane %s: %w", step.PaneID, err)
			}

			return string(pane.ID()), nil
		},
	}, nil
}

func (e *Executor) prepareSelectWindow(step SelectWindowStep) (stepEffect, error) {
	session, ok := e.sessions[step.SessionID]
	if !ok {
		return stepEffect{}, oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "prepare_step", "step_id", step.SessionID).
			Wrapf(errSessionNotFound, "%s", step.SessionID)
	}

	return stepEffect{
		run: func(ctx context.Context) (string, error) {
			if err := e.selectWindows(ctx, session, step); err != nil {
				return "", err
			}

			return string(session.ID()), nil
		},
	}, nil
}

func (e *Executor) selectWindows(ctx context.Context, session gotmux.Session, step SelectWindowStep) error {
	_, links, err := session.WindowInfos(ctx)
	if err != nil {
		return fmt.Errorf("window infos for session %s: %w", step.SessionID, err)
	}

	if step.LastWindowID != "" && step.LastIndex >= 0 {
		if err := selectWindowAt(ctx, links, step.LastIndex, "last"); err != nil {
			return err
		}
	}

	return selectWindowAt(ctx, links, step.ActiveIndex, "active")
}

func selectWindowAt(ctx context.Context, links []gotmux.WindowLinkInfo, index int, kind string) error {
	for _, link := range links {
		if link.Index != index {
			continue
		}

		if err := link.Handle().Select(ctx); err != nil {
			return fmt.Errorf("select %s window %d: %w", kind, index, err)
		}

		return nil
	}

	return nil
}

func (e *Executor) resolveNonrestartableWorkingDir(pane model.Pane) string {
	dir := string(pane.Directory)
	if dir == "" && len(pane.Launch.Directory) > 0 {
		dir = string(pane.Launch.Directory)
	}

	if dir == "" {
		return ""
	}

	if statErr := statSavedWorkingDirectory(dir); statErr != nil {
		switch pane.Launch.Reason {
		case string(model.ReasonWorkingDirectoryDeleted):
			e.note("launch", model.ReasonWorkingDirectoryDeleted)
		case string(model.ReasonPaneDead):
			e.note("launch", model.ReasonPaneDead)
		case string(model.ReasonExecutableDeleted):
			e.note("launch", model.ReasonExecutableDeleted)
		default:
			e.note("directory", model.ReasonSavedWorkingDirectoryMissing)
		}

		return ""
	}

	return dir
}

func (e *Executor) resolveWorkingDir(pane model.Pane) (string, bool, error) {
	if pane.Dead || pane.Launch.Recovery == model.RecoveryNonrestartable {
		directory := e.resolveNonrestartableWorkingDir(pane)
		return directory, directory == "" && savedWorkingDir(pane) != "", nil
	}

	dir := savedWorkingDir(pane)

	if dir == "" {
		return "", false, nil
	}

	if statErr := statSavedWorkingDirectory(dir); statErr != nil {
		if !e.cfg.MissingWorkingDir.fallsBack() {
			return "", false, oops.In("snapshot.restore").
				Code("working_directory_missing").
				With("operation", "resolve_working_dir", "pane_id", pane.ID, "dir", dir).
				Wrap(ErrWorkingDirNotFound)
		}

		e.note("directory", model.ReasonSavedWorkingDirectoryMissing)

		return "", true, nil
	}

	return dir, false, nil
}

func statSavedWorkingDirectory(dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open saved working directory: %w", err)
	}

	if err := root.Close(); err != nil {
		return fmt.Errorf("close saved working directory: %w", err)
	}

	return nil
}

func savedWorkingDir(pane model.Pane) string {
	if len(pane.Directory) > 0 {
		return string(pane.Directory)
	}

	return string(pane.Launch.Directory)
}

func (e *Executor) unlaunchedPane(pane model.Pane) (paneLaunch, error) {
	result := disabledPaneLaunch()

	_, cwdFallback, err := e.resolveWorkingDir(pane)
	if err != nil {
		return result, err
	}

	result.cwdFallback = cwdFallback
	if !e.cfg.LaunchProcesses && (pane.Launch.Recovery == model.RecoveryRestart || pane.Launch.Recovery == model.RecoveryResume) {
		result.reason = PaneReasonProcessLaunchDisabled
	}

	return result, nil
}

func (e *Executor) preparePaneLaunch(pane model.Pane, ownerSessionID string) (paneLaunch, error) {
	launch := pane.Launch
	if !e.cfg.LaunchProcesses || (launch.Recovery != model.RecoveryRestart && launch.Recovery != model.RecoveryResume) {
		return e.unlaunchedPane(pane)
	}

	if !executableAvailable(string(launch.Executable)) {
		e.note("launch", model.ReasonApprovedLaunchUnavailable)

		result := disabledPaneLaunch()
		result.reason = PaneReasonExecutableUnavailable

		return result, nil
	}

	directory, cwdFallback, err := e.resolveLaunchWorkingDir(pane)
	if err != nil {
		return disabledPaneLaunch(), err
	}

	if cwdFallback && e.cfg.MissingWorkingDir == MissingWorkingDirShell {
		result := disabledPaneLaunch()
		result.cwdFallback = true
		result.reason = PaneReasonWorkingDirFallback

		return result, nil
	}

	environment, removed := launchEnvironment(e.effectivePaneLaunchEnvironment(pane, ownerSessionID))

	program, ok := e.approvedLaunchProgram(launch, ownerSessionID, environment, removed)
	if !ok {
		e.note("launch", model.ReasonApprovedLaunchUnavailable)

		result := disabledPaneLaunch()
		result.reason = PaneReasonExecutableUnavailable
		result.cwdFallback = cwdFallback

		return result, nil
	}

	return paneLaunch{program: program, directory: directory, environment: environment, enabled: true, cwdFallback: cwdFallback, reason: ""}, nil
}

func (e *Executor) approvedLaunchProgram(launch model.Launch, ownerSessionID string, environment map[string]string, removed []string) (gotmux.Program, bool) {
	executable := string(launch.Executable)

	arguments := make([]string, 0, len(launch.Arguments)-1)
	for _, argument := range launch.Arguments[1:] {
		arguments = append(arguments, string(argument))
	}

	if launch.Recovery == model.RecoveryResume {
		if !executableAvailable(resumeShellProgram) {
			return gotmux.Program{}, false
		}

		arguments = append([]string{
			"-m",
			"-c",
			`trap ':' INT QUIT; "$@"; trap - INT QUIT; exec "$0" -i`,
			e.resumeShell(ownerSessionID, environment),
			executable,
		}, arguments...)
		executable = resumeShellProgram
	}

	return literalLaunchProgram(executable, arguments, removed)
}

func (e *Executor) resumeShell(ownerSessionID string, environment map[string]string) string {
	session := e.index.sessionsByID[ownerSessionID]
	for _, option := range e.manifest.EffectiveSessionOptions(session) {
		if string(option.Name) == "default-shell" && option.Present && executableAvailable(string(option.Value)) {
			return string(option.Value)
		}
	}

	if shell := environment["SHELL"]; executableAvailable(shell) {
		return shell
	}

	return resumeShellProgram
}

func (e *Executor) effectivePaneLaunchEnvironment(pane model.Pane, ownerSessionID string) []model.Variable {
	session := e.index.sessionsByID[ownerSessionID]
	effective := effectiveEnvironmentVariables(e.manifest.Source.Environment, session.Environment)
	values := make(map[string]model.Variable, len(effective)+len(pane.Launch.Environment))

	for _, variable := range effective {
		values[string(variable.Name)] = variable
	}

	for _, variable := range pane.Launch.Environment {
		values[string(variable.Name)] = variable
	}

	result := make([]model.Variable, 0, len(values))
	for _, name := range slices.Sorted(maps.Keys(values)) {
		result = append(result, values[name])
	}

	return result
}

func disabledPaneLaunch() paneLaunch {
	return paneLaunch{program: gotmux.Program{}, directory: "", environment: nil, enabled: false, cwdFallback: false, reason: ""}
}

func executableAvailable(path string) bool {
	info, err := os.Stat(path)

	return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
}

func launchEnvironment(variables []model.Variable) (map[string]string, []string) {
	environment := make(map[string]string)
	removed := make([]string, 0)

	for _, variable := range variables {
		name := string(variable.Name)
		if slices.Contains(tmuxPaneVariables, name) {
			continue
		}

		if variable.State == "set" {
			environment[name] = string(variable.Value)
		} else {
			removed = append(removed, name)
		}
	}

	return environment, removed
}

func literalLaunchProgram(executable string, arguments, removed []string) (gotmux.Program, bool) {
	if len(removed) == 0 {
		return gotmux.Exec(executable, arguments...), true
	}

	if !executableAvailable(environmentProgram) {
		return gotmux.Program{}, false
	}

	slices.Sort(removed)

	envArguments := make([]string, 0, len(removed)*2+2+len(arguments))
	for _, name := range removed {
		envArguments = append(envArguments, "-u", name)
	}

	envArguments = append(envArguments, "--", executable)
	envArguments = append(envArguments, arguments...)

	return gotmux.Exec(environmentProgram, envArguments...), true
}

func (e *Executor) resolveLaunchWorkingDir(pane model.Pane) (string, bool, error) {
	launchPane := pane
	if len(pane.Launch.Directory) > 0 {
		launchPane.Directory = pane.Launch.Directory
	}

	return e.resolveWorkingDir(launchPane)
}

func (e *Executor) launchSessionInitialPane(ctx context.Context, session gotmux.Session, sessionID string, launch paneLaunch) (string, error) {
	_, links, err := session.WindowInfos(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve initial window for session %s: %w", sessionID, err)
	}

	if len(links) != 1 {
		return "", fmt.Errorf("resolve initial window for session %s: %w", sessionID, errBootstrapNotFound)
	}

	pane, err := links[0].Handle().Window().ActivePane(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve initial pane for session %s: %w", sessionID, err)
	}

	if launch.enabled {
		if err := respawnPane(ctx, pane, launch); err != nil {
			return string(pane.ID()), fmt.Errorf("launch initial pane for session %s: %w", sessionID, err)
		}
	}

	return string(pane.ID()), nil
}

func respawnPane(ctx context.Context, pane gotmux.Pane, launch paneLaunch) error {
	if !launch.enabled {
		return nil
	}

	err := pane.Respawn(ctx, gotmux.RespawnOptions{
		Program:             launch.program,
		Dir:                 launch.directory,
		Env:                 launch.environment,
		TmuxEnv:             nil,
		KillRunning:         true,
		PreserveEnvironment: false,
	})
	if err != nil {
		return fmt.Errorf("respawn pane: %w", err)
	}

	return nil
}

func (e *Executor) runnableLaunchOutcome(recovery model.Recovery, launch paneLaunch) string {
	if !launch.enabled && launch.cwdFallback {
		return PaneOutcomeAllowedCWDFallback
	}

	if !launch.enabled {
		return PaneOutcomeMissingExecutable
	}

	if recovery == model.RecoveryRestart {
		return PaneOutcomeRestartDispatch
	}

	return PaneOutcomeNativeResumeDispatch
}

func (e *Executor) paneLaunchOutcome(pane model.Pane, launch paneLaunch) string {
	if !e.cfg.LaunchProcesses {
		if launch.cwdFallback {
			return PaneOutcomeAllowedCWDFallback
		}

		return PaneOutcomeDefaultShell
	}

	switch pane.Launch.Recovery {
	case model.RecoveryNonrestartable:
		return PaneOutcomeUnsupported
	case model.RecoveryUnverified:
		if len(pane.Launch.Executable) == 0 && len(pane.Launch.Arguments) == 0 {
			if launch.cwdFallback {
				return PaneOutcomeAllowedCWDFallback
			}

			return PaneOutcomeDefaultShell
		}

		return PaneOutcomeUnsupported
	case model.RecoveryRestart, model.RecoveryResume:
		return e.runnableLaunchOutcome(pane.Launch.Recovery, launch)
	default:
		if launch.cwdFallback {
			return PaneOutcomeAllowedCWDFallback
		}

		return PaneOutcomeDefaultShell
	}
}

func (e *Executor) recordPaneResult(paneID, runtimeID string, pane model.Pane, launch paneLaunch) {
	e.paneResults[paneID] = PaneResult{
		PaneID:             paneID,
		RuntimeID:          runtimeID,
		Outcome:            e.paneLaunchOutcome(pane, launch),
		Reason:             e.paneLaunchReason(pane, launch),
		AppVerified:        false,
		CWDFallback:        launch.cwdFallback,
		VerificationReason: "",
	}
}

func (e *Executor) paneLaunchReason(pane model.Pane, launch paneLaunch) string {
	if pane.Launch.Recovery == model.RecoveryNonrestartable {
		return pane.Launch.Reason
	}

	if launch.reason != "" {
		return launch.reason
	}

	if pane.Launch.Recovery == model.RecoveryUnverified && (len(pane.Launch.Executable) > 0 || len(pane.Launch.Arguments) > 0) {
		return PaneReasonUnsupportedLaunch
	}

	if launch.cwdFallback {
		return PaneReasonWorkingDirFallback
	}

	return ""
}

func (e *Executor) ensureAllPaneResults(plan Plan) {
	for _, pane := range plan.SourceGraph.Panes {
		if _, ok := e.paneResults[pane.ID]; ok {
			continue
		}

		runtimeID := ""
		if handle, ok := e.panes[pane.ID]; ok {
			runtimeID = string(handle.ID())
		} else {
			for _, step := range plan.Steps {
				if split, ok := step.(*SplitPaneStep); ok && split.PaneID == pane.ID {
					runtimeID = e.receipts[step.ID()]
					break
				}
			}
		}

		outcome, reason := paneOutcome(pane.Launch, e.cfg.LaunchProcesses)
		e.paneResults[pane.ID] = PaneResult{
			PaneID:             pane.ID,
			RuntimeID:          runtimeID,
			Outcome:            outcome,
			Reason:             reason,
			AppVerified:        false,
			CWDFallback:        false,
			VerificationReason: "",
		}
	}
}

func (e *Executor) collectedPaneResults(plan Plan) []PaneResult {
	results := make([]PaneResult, 0, len(plan.SourceGraph.Panes))
	for _, pane := range plan.SourceGraph.Panes {
		if r, ok := e.paneResults[pane.ID]; ok {
			results = append(results, r)
		}
	}

	return results
}
