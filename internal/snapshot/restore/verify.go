//go:build linux || darwin

package restore

import (
	"context"
	"maps"
	"sort"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/integration/tmuxserver"
	"github.com/zigai/sesh/internal/snapshot/model"
)

const maxVerificationMismatches = 16

const (
	MismatchSession     = "session"
	MismatchGroup       = "group"
	MismatchLink        = "link"
	MismatchNavigation  = "navigation"
	MismatchWindow      = "window"
	MismatchGeometry    = "geometry"
	MismatchPane        = "pane"
	MismatchOption      = "option"
	MismatchEnvironment = "environment"
)

type VerificationMismatch struct {
	Domain string `json:"domain"`
	Entity string `json:"entity"`
	Reason string `json:"reason"`
}

type VerificationReport struct {
	Verified      bool                   `json:"verified"`
	Sessions      int                    `json:"sessions"`
	Windows       int                    `json:"windows"`
	Panes         int                    `json:"panes"`
	Links         int                    `json:"links"`
	Mismatches    []VerificationMismatch `json:"mismatches"`
	Advisories    []VerificationMismatch `json:"advisories"`
	VerifiedSteps []string               `json:"verifiedSteps"`
	RuntimePanes  map[string]string      `json:"-"`
}

type slotKey struct {
	session string
	index   int
}

type verifiedState struct {
	session   map[string]bool
	group     map[string]bool
	window    map[string]bool
	layout    map[string]bool
	zoom      map[string]bool
	pane      map[string]bool
	title     map[string]bool
	active    map[string]bool
	slot      map[slotKey]bool
	nav       map[string]bool
	options   map[string]bool
	variables map[string]bool
}

type slotExpectation struct {
	window string
	active bool
	last   bool
}

type layoutLeaf struct {
	runtime string
	left    int
	top     int
	width   int
	height  int
}

func (r *VerificationReport) mismatch(domain, entity, reason string) {
	if len(r.Mismatches) >= maxVerificationMismatches {
		return
	}

	r.Mismatches = append(r.Mismatches, VerificationMismatch{Domain: domain, Entity: entity, Reason: reason})
}

func (r *VerificationReport) advisory(domain, entity, reason string) {
	if len(r.Advisories) >= maxVerificationMismatches {
		return
	}

	r.Advisories = append(r.Advisories, VerificationMismatch{Domain: domain, Entity: entity, Reason: reason})
}

func Verify(ctx context.Context, server *gotmux.Server, plan Plan, receipts map[string]string) (VerificationReport, error) {
	report := VerificationReport{
		Verified:      false,
		Sessions:      0,
		Windows:       0,
		Panes:         0,
		Links:         0,
		Mismatches:    nil,
		Advisories:    nil,
		VerifiedSteps: nil,
		RuntimePanes:  nil,
	}

	identities, err := ResolveRuntimeIdentities(ctx, server, plan, receipts)
	if err != nil {
		return report, err
	}

	report.RuntimePanes = make(map[string]string, len(identities.Panes))
	for sourceID, runtimeID := range identities.Panes {
		report.RuntimePanes[sourceID] = string(runtimeID)
	}

	state := newVerifiedState()

	verifySessions(ctx, server, plan, identities, state, &report)
	verifyWindows(ctx, server, plan, identities, state, &report)
	verifyScopedState(ctx, server, plan, identities, state, &report)

	report.VerifiedSteps = verifiedSteps(plan, state)
	report.Verified = len(report.Mismatches) == 0 && len(report.VerifiedSteps) == len(plan.Steps)

	return report, nil
}

func newVerifiedState() *verifiedState {
	return &verifiedState{
		session:   map[string]bool{},
		group:     map[string]bool{},
		window:    map[string]bool{},
		layout:    map[string]bool{},
		zoom:      map[string]bool{},
		pane:      map[string]bool{},
		title:     map[string]bool{},
		active:    map[string]bool{},
		slot:      map[slotKey]bool{},
		nav:       map[string]bool{},
		options:   map[string]bool{},
		variables: map[string]bool{},
	}
}

func expectedSlotsBySession(plan Plan) map[string]map[int]slotExpectation {
	slots := map[string]map[int]slotExpectation{}

	for _, link := range plan.SourceGraph.Links {
		if slots[link.Session] == nil {
			slots[link.Session] = map[int]slotExpectation{}
		}

		slots[link.Session][link.Index] = slotExpectation{window: link.Window, active: link.Active, last: link.Last}
	}

	return slots
}

func verifySessions(
	ctx context.Context,
	server *gotmux.Server,
	plan Plan,
	identities RuntimeIdentities,
	state *verifiedState,
	report *VerificationReport,
) {
	groupNames := map[string]string{}
	for _, group := range plan.SourceGraph.Groups {
		groupNames[group.ID] = string(group.Name)
	}

	expectedSlots := expectedSlotsBySession(plan)

	for _, session := range plan.SourceGraph.Sessions {
		runtimeID, ok := identities.Sessions[session.ID]
		if !ok {
			report.mismatch(MismatchSession, session.ID, "runtime_identity_missing")

			continue
		}

		handle, err := server.Session(ctx, runtimeID)
		if err != nil {
			report.mismatch(MismatchSession, session.ID, "session_unavailable")

			continue
		}

		report.Sessions++

		sessionOK := verifySessionIdentity(ctx, handle, session, groupNames, report)

		state.session[session.ID] = sessionOK
		state.group[session.ID] = sessionOK

		navOK := verifySessionLinks(ctx, handle, identities, expectedSlots[session.ID], session.ID, state, report)

		state.nav[session.ID] = navOK
	}
}

func verifySessionIdentity(
	ctx context.Context,
	handle gotmux.Session,
	session model.Session,
	groupNames map[string]string,
	report *VerificationReport,
) bool {
	info, err := handle.Info(ctx)
	if err != nil {
		report.mismatch(MismatchSession, session.ID, "session_unreadable")

		return false
	}

	ok := true

	if info.Name != string(session.Name) {
		report.mismatch(MismatchSession, session.ID, "session_name_mismatch")

		ok = false
	}

	expectedGroup := ""
	if session.Group != "" {
		expectedGroup = groupNames[session.Group]
	}

	observedGroup, hasGroup := info.Group.Get()
	if !hasGroup {
		observedGroup = ""
	}

	if observedGroup != expectedGroup {
		report.mismatch(MismatchGroup, session.ID, "session_group_mismatch")

		ok = false
	}

	return ok
}

func verifySessionLinks(
	ctx context.Context,
	handle gotmux.Session,
	identities RuntimeIdentities,
	expected map[int]slotExpectation,
	session string,
	state *verifiedState,
	report *VerificationReport,
) bool {
	_, links, err := handle.WindowInfos(ctx)
	if err != nil {
		report.mismatch(MismatchLink, session, "session_links_unreadable")

		return false
	}

	observed := make(map[int]gotmux.WindowLinkInfo, len(links))
	for _, link := range links {
		observed[link.Index] = link
	}

	navOK := len(observed) == len(expected)

	if len(observed) != len(expected) {
		report.mismatch(MismatchLink, session, "slot_count_mismatch")
	}

	slots := make([]int, 0, len(expected))
	for slot := range expected {
		slots = append(slots, slot)
	}

	sort.Ints(slots)

	for _, slot := range slots {
		want := expected[slot]

		link, ok := observed[slot]
		if !ok {
			report.mismatch(MismatchLink, session, "slot_missing")

			navOK = false

			continue
		}

		windowID, ok := identities.Windows[want.window]
		if !ok {
			report.mismatch(MismatchLink, session, "slot_receipt_missing")

			navOK = false

			continue
		}

		if link.WindowID != windowID {
			report.mismatch(MismatchLink, session, "window_identity_mismatch")

			navOK = false

			continue
		}

		state.slot[slotKey{session: session, index: slot}] = true
		report.Links++

		if link.Active != want.active {
			report.mismatch(MismatchNavigation, session, "active_slot_mismatch")

			navOK = false
		}

		if containsFlag(link.Flags, '-') != want.last {
			report.mismatch(MismatchNavigation, session, "last_slot_mismatch")

			navOK = false
		}
	}

	return navOK
}

func containsFlag(flags string, flag byte) bool {
	for i := range len(flags) {
		if flags[i] == flag {
			return true
		}
	}

	return false
}

func verifyWindows(
	ctx context.Context,
	server *gotmux.Server,
	plan Plan,
	identities RuntimeIdentities,
	state *verifiedState,
	report *VerificationReport,
) {
	panesByWindow := map[string][]model.Pane{}

	for _, pane := range plan.SourceGraph.Panes {
		panesByWindow[pane.Window] = append(panesByWindow[pane.Window], pane)
	}

	for _, window := range plan.SourceGraph.Windows {
		verifyWindow(ctx, server, window, panesByWindow[window.ID], identities, state, report)
	}
}

func verifyWindow(
	ctx context.Context,
	server *gotmux.Server,
	window model.Window,
	expectedPanes []model.Pane,
	identities RuntimeIdentities,
	state *verifiedState,
	report *VerificationReport,
) {
	runtimeID, ok := identities.Windows[window.ID]
	if !ok {
		report.mismatch(MismatchWindow, window.ID, "runtime_identity_missing")

		return
	}

	handle, err := server.Window(ctx, runtimeID)
	if err != nil {
		report.mismatch(MismatchWindow, window.ID, "window_unavailable")

		return
	}

	report.Windows++

	windowOK, zoomOK := verifyWindowIdentity(ctx, handle, window, report)

	state.window[window.ID] = windowOK
	state.zoom[window.ID] = zoomOK

	observedPanes, paneErr := handle.Panes(ctx)
	if paneErr != nil {
		report.mismatch(MismatchPane, window.ID, "window_panes_unreadable")

		return
	}

	if len(observedPanes) != len(expectedPanes) {
		report.mismatch(MismatchPane, window.ID, "pane_count_mismatch")
	}

	state.layout[window.ID] = verifyWindowLayout(ctx, handle, window, identities, observedPanes, report)
	state.active[window.ID] = verifyActivePane(ctx, handle, expectedPanes, identities, report)

	verifyWindowPanes(observedPanes, expectedPanes, identities, state, report)
}

func verifyWindowPanes(
	observedPanes []gotmux.PaneInfo,
	expectedPanes []model.Pane,
	identities RuntimeIdentities,
	state *verifiedState,
	report *VerificationReport,
) {
	observedByID := make(map[gotmux.PaneID]gotmux.PaneInfo, len(observedPanes))
	for _, pane := range observedPanes {
		observedByID[pane.ID] = pane
	}

	for _, pane := range expectedPanes {
		paneID, ok := identities.Panes[pane.ID]
		if !ok {
			report.mismatch(MismatchPane, pane.ID, "runtime_identity_missing")

			continue
		}

		observedPane, ok := observedByID[paneID]
		if !ok {
			report.mismatch(MismatchPane, pane.ID, "pane_missing")

			continue
		}

		report.Panes++

		paneOK := observedPane.Index == pane.Index
		if !paneOK {
			report.mismatch(MismatchPane, pane.ID, "pane_index_mismatch")
		}

		// Titles belong to the programs running in the pane: shells and agents rewrite them as soon
		// as they start, so a later difference is advisory and equality is never claimed.
		if observedPane.Title != string(pane.Title) {
			report.advisory(MismatchPane, pane.ID, "pane_title_changed")
		}

		state.pane[pane.ID] = paneOK
		state.title[pane.ID] = paneOK
	}
}

func windowNameIsAutomatic(window model.Window) bool {
	for _, option := range window.Options {
		if string(option.Name) == "automatic-rename" && option.Present {
			return string(option.Value) == "on"
		}
	}

	return false
}

func verifyWindowIdentity(
	ctx context.Context,
	handle gotmux.Window,
	window model.Window,
	report *VerificationReport,
) (bool, bool) {
	info, err := handle.Info(ctx)
	if err != nil {
		report.mismatch(MismatchWindow, window.ID, "window_unreadable")

		return false, false
	}

	ok := true

	if info.Name != string(window.Name) {
		if windowNameIsAutomatic(window) {
			// With automatic-rename on, tmux renames the window to its running program, so the
			// name is the program's to change after restore, not a restored fact.
			report.advisory(MismatchWindow, window.ID, "window_name_changed")
		} else {
			report.mismatch(MismatchWindow, window.ID, "window_name_mismatch")

			ok = false
		}
	}

	if info.Width != window.Width || info.Height != window.Height {
		report.mismatch(MismatchGeometry, window.ID, "window_size_mismatch")

		ok = false
	}

	zoomOK := info.Zoomed == window.Zoomed
	if !zoomOK {
		report.mismatch(MismatchWindow, window.ID, "zoom_mismatch")

		ok = false
	}

	return ok, zoomOK
}

func verifyWindowLayout(
	ctx context.Context,
	handle gotmux.Window,
	window model.Window,
	identities RuntimeIdentities,
	observedPanes []gotmux.PaneInfo,
	report *VerificationReport,
) bool {
	info, err := handle.Info(ctx)
	if err != nil {
		report.mismatch(MismatchGeometry, window.ID, "window_layout_unreadable")

		return false
	}

	paneNames := make(map[string]string, len(observedPanes))
	for _, pane := range observedPanes {
		paneNames[string(pane.ID)] = string(pane.ID)
	}

	observed, err := tmuxserver.DecodeLayout(info.Layout, paneNames)
	if err != nil {
		report.mismatch(MismatchGeometry, window.ID, "window_layout_undecodable")

		return false
	}

	expected := make([]layoutLeaf, 0, len(observedPanes))
	collectLayoutLeaves(window.Layout, &expected)

	for i := range expected {
		runtimeID, ok := identities.Panes[expected[i].runtime]
		if !ok {
			report.mismatch(MismatchGeometry, window.ID, "pane_receipt_missing")

			return false
		}

		expected[i].runtime = string(runtimeID)
	}

	actual := make([]layoutLeaf, 0, len(expected))
	collectLayoutLeaves(observed.Tiled, &actual)

	sortLayoutLeaves(expected)
	sortLayoutLeaves(actual)

	if len(expected) != len(actual) {
		report.mismatch(MismatchGeometry, window.ID, "pane_layout_count_mismatch")

		return false
	}

	for i := range expected {
		if expected[i] != actual[i] {
			report.mismatch(MismatchGeometry, window.ID, "pane_layout_mismatch")

			return false
		}
	}

	return true
}

func collectLayoutLeaves(node model.Layout, out *[]layoutLeaf) {
	if node.Pane != "" && len(node.Children) == 0 {
		*out = append(*out, layoutLeaf{
			runtime: node.Pane,
			left:    node.Left,
			top:     node.Top,
			width:   node.Width,
			height:  node.Height,
		})

		return
	}

	for _, child := range node.Children {
		collectLayoutLeaves(child, out)
	}
}

func sortLayoutLeaves(leaves []layoutLeaf) {
	sort.Slice(leaves, func(i, j int) bool {
		if leaves[i].top != leaves[j].top {
			return leaves[i].top < leaves[j].top
		}

		if leaves[i].left != leaves[j].left {
			return leaves[i].left < leaves[j].left
		}

		return leaves[i].runtime < leaves[j].runtime
	})
}

func verifyActivePane(
	ctx context.Context,
	handle gotmux.Window,
	expectedPanes []model.Pane,
	identities RuntimeIdentities,
	report *VerificationReport,
) bool {
	expectedActive := ""

	for _, pane := range expectedPanes {
		if pane.Active {
			expectedActive = pane.ID

			break
		}
	}

	if expectedActive == "" {
		return true
	}

	runtimeActive, ok := identities.Panes[expectedActive]
	if !ok {
		report.mismatch(MismatchPane, expectedActive, "runtime_identity_missing")

		return false
	}

	activePane, err := handle.ActivePane(ctx)
	if err != nil {
		report.mismatch(MismatchPane, expectedActive, "active_pane_unreadable")

		return false
	}

	if activePane.ID() != runtimeActive {
		report.mismatch(MismatchPane, expectedActive, "active_pane_mismatch")

		return false
	}

	return true
}

func verifyScopedState(
	ctx context.Context,
	server *gotmux.Server,
	plan Plan,
	identities RuntimeIdentities,
	state *verifiedState,
	report *VerificationReport,
) {
	targetGlobal, globalErr := environmentTable(ctx, server.Environment())

	for _, session := range plan.SourceGraph.Sessions {
		runtimeID, ok := identities.Sessions[session.ID]
		if !ok {
			continue
		}

		handle, err := server.Session(ctx, runtimeID)
		if err != nil {
			continue
		}

		if globalErr != nil {
			report.mismatch(MismatchEnvironment, session.ID, "scope_unreadable")
			state.options[session.ID] = verifyOptions(ctx, handle.Options(), session.Options, session.ID, report)
			state.variables[session.ID] = false

			continue
		}

		optionsOK, variablesOK := verifySessionScope(
			ctx,
			targetGlobal,
			handle,
			plan.SourceGraph.Source.Environment,
			session,
			report,
		)

		state.options[session.ID] = optionsOK
		state.variables[session.ID] = variablesOK
	}

	for _, window := range plan.SourceGraph.Windows {
		runtimeID, ok := identities.Windows[window.ID]
		if !ok {
			continue
		}

		handle, err := server.Window(ctx, runtimeID)
		if err != nil {
			continue
		}

		state.options[window.ID] = verifyOptions(ctx, handle.Options(), window.Options, window.ID, report)
	}

	for _, pane := range plan.SourceGraph.Panes {
		runtimeID, ok := identities.Panes[pane.ID]
		if !ok {
			continue
		}

		handle, err := server.Pane(ctx, runtimeID)
		if err != nil {
			continue
		}

		state.options[pane.ID] = verifyOptions(ctx, handle.Options(), pane.Options, pane.ID, report)
	}
}

func verifySessionScope(
	ctx context.Context,
	targetGlobal map[environmentKey]environmentShape,
	handle gotmux.Session,
	expectedGlobal []model.Variable,
	session model.Session,
	report *VerificationReport,
) (bool, bool) {
	optionsOK := verifyOptions(ctx, handle.Options(), session.Options, session.ID, report)

	matches, err := effectiveEnvironmentMatch(
		ctx,
		targetGlobal,
		handle.Environment(),
		expectedGlobal,
		session.Environment,
	)
	if err != nil {
		report.mismatch(MismatchEnvironment, session.ID, "scope_unreadable")

		return optionsOK, false
	}

	if !matches {
		report.mismatch(MismatchEnvironment, session.ID, "environment_mismatch")

		return optionsOK, false
	}

	return optionsOK, true
}

func verifyOptions(
	ctx context.Context,
	scope optionScope,
	expected []model.Option,
	entity string,
	report *VerificationReport,
) bool {
	live, err := scope.ListWith(ctx, gotmux.ListOptionOptions{Inherited: true, Hooks: true})
	if err != nil {
		report.mismatch(MismatchOption, entity, "scope_unreadable")

		return false
	}

	liveValues := optionEntries(live)

	wanted, ignored := effectiveOptions(expected)
	for name := range ignored {
		delete(liveValues, name)
	}

	if !maps.Equal(liveValues, wanted) {
		report.mismatch(MismatchOption, entity, "local_option_mismatch")

		return false
	}

	return true
}

func verifiedSteps(plan Plan, state *verifiedState) []string {
	verified := make([]string, 0, len(plan.Steps))

	for _, step := range plan.Steps {
		if stepObserved(step, state) {
			verified = append(verified, step.ID())
		}
	}

	return verified
}

func stepObserved(step Step, state *verifiedState) bool {
	switch step := step.(type) {
	case *CreateSessionStep:
		return createdSessionObserved(step, state)
	case *PlaceInitialWindowStep:
		return placedInitialWindowObserved(step, state)
	case *CreateWindowStep:
		return createdWindowObserved(step, state)
	case *SplitPaneStep:
		return state.pane[step.PaneID]
	case *LinkWindowStep:
		return state.slot[slotKey{session: step.SessionID, index: step.Index}]
	case *CloseBootstrapWindowStep:
		return state.nav[step.SessionID]
	case *ApplyLayoutStep:
		return state.layout[step.WindowID]
	case *SelectPaneStep:
		return state.active[step.WindowID]
	case *ApplyZoomStep:
		return state.zoom[step.WindowID]
	case *SelectWindowStep:
		return state.nav[step.SessionID]
	case *SetPaneTitleStep:
		return state.title[step.PaneID]
	}

	return false
}

func createdSessionObserved(step *CreateSessionStep, state *verifiedState) bool {
	session := step.SessionID

	return state.session[session] && state.group[session] && state.options[session] && state.variables[session]
}

func placedInitialWindowObserved(step *PlaceInitialWindowStep, state *verifiedState) bool {
	if step.WindowID == "" {
		return state.nav[step.SessionID]
	}

	return state.slot[slotKey{session: step.SessionID, index: step.Index}]
}

func createdWindowObserved(step *CreateWindowStep, state *verifiedState) bool {
	window := step.WindowID

	return state.window[window] && state.layout[window] && state.zoom[window]
}
