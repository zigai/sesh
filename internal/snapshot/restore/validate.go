package restore

import (
	"bytes"
	"reflect"
	"slices"

	"github.com/zigai/sesh/internal/snapshot/model"
)

const (
	maxPlanSteps                = model.MaxEntities * 5
	maxDependencies             = model.MaxEntities
	maxTotalDependencies        = model.MaxEntities * 16
	entityDependencyAllowance   = 12
	paneLinkDependencyAllowance = 2
)

type planTracker struct {
	createdWindows         map[string]string
	createdPanes           map[string]string
	createdSessions        map[string]string
	bootstrapSessions      map[string]bool
	closedBootstraps       map[string]string
	layoutWindows          map[string]string
	selectedPaneWindows    map[string]string
	zoomedWindows          map[string]string
	selectedWindowSessions map[string]string
	handledLinks           map[linkSlotKey]bool
	sessionLinkStepIDs     map[string][]string
	windowPaneIDs          map[string][]string
	windowFinalStepIDs     map[string]string
	titledPanes            map[string]string
}

type validationContext struct {
	graph      model.Manifest
	tracker    *planTracker
	seenSteps  map[string]int
	groupNames map[string]model.Bytes
}

func ValidatePlan(p Plan) error {
	if err := validatePlanBounds(p); err != nil {
		return err
	}

	if err := validatePlanPrerequisites(p); err != nil {
		return err
	}

	expected, _ := generateSteps(p.SourceGraph)
	if !reflect.DeepEqual(p.Steps, expected) {
		return planFailure("validation", "step_payload_mismatch", ErrInvalid)
	}

	seenSteps := make(map[string]int, len(p.Steps))
	tracker := newPlanTracker(len(p.Steps), len(p.SourceGraph.Sessions), len(p.SourceGraph.Windows), len(p.SourceGraph.Panes))

	groupNames := make(map[string]model.Bytes, len(p.SourceGraph.Groups))
	for _, grp := range p.SourceGraph.Groups {
		groupNames[grp.ID] = cloneBytes(grp.Name)
	}

	ctx := validationContext{
		graph:      p.SourceGraph,
		tracker:    &tracker,
		seenSteps:  seenSteps,
		groupNames: groupNames,
	}

	if err := validatePlanSteps(p.Steps, ctx); err != nil {
		return err
	}

	if err := tracker.checkEntityCoverage(p.SourceGraph); err != nil {
		return err
	}

	if err := tracker.checkLinkCoverage(p.SourceGraph); err != nil {
		return err
	}

	return tracker.checkFinalizationCoverage(p.SourceGraph)
}

func validatePlanBounds(p Plan) error {
	if len(p.Steps) == 0 || len(p.Steps) > maxPlanSteps {
		return planFailure("validation", "bounds", ErrInvalid)
	}

	if len(p.RequestedSessions) == 0 || len(p.RequestedSessions) > model.MaxEntities {
		return planFailure("validation", "bounds", ErrInvalid)
	}

	if len(p.AddedSessions) > model.MaxEntities || len(p.ClosureSessions) > model.MaxEntities {
		return planFailure("validation", "bounds", ErrInvalid)
	}

	if len(p.MissingRequirements) > model.MaxEntities {
		return planFailure("validation", "bounds", ErrInvalid)
	}

	total := 0

	for _, step := range p.Steps {
		if len(step.DependsOn()) > maxDependencies {
			return planFailure("validation", "bounds", ErrInvalid)
		}

		total += len(step.DependsOn())
		if total > maxTotalDependencies {
			return planFailure("validation", "bounds", ErrInvalid)
		}
	}

	return nil
}

func validatePlanPrerequisites(p Plan) error {
	if err := p.SourceGraph.Validate(); err != nil {
		return planFailure("validation", "invalid_source_graph", err)
	}

	if err := validatePlanningBudget(p.SourceGraph); err != nil {
		return err
	}

	if err := validateSelectionAndClosure(p); err != nil {
		return err
	}

	if err := validateSummary(p); err != nil {
		return err
	}

	if err := validateMissingRequirements(p); err != nil {
		return err
	}

	return validateNames(p.SourceGraph)
}

func validatePlanningBudget(g model.Manifest) error {
	panes := make(map[string]int, len(g.Windows))
	for _, pane := range g.Panes {
		panes[pane.Window]++
	}

	budget := entityDependencyAllowance * (len(g.Sessions) + len(g.Windows) + len(g.Panes) + len(g.Links))
	for _, link := range g.Links {
		budget += paneLinkDependencyAllowance * panes[link.Window]
		if budget > maxTotalDependencies {
			return planFailure("validation", "plan_dependency_budget", ErrInvalid)
		}
	}

	return nil
}

func validatePlanSteps(steps []Step, ctx validationContext) error {
	for i, step := range steps {
		if step.ID() == "" {
			return planFailure("validation", "empty_step_id", ErrInvalid)
		}

		if _, exists := ctx.seenSteps[step.ID()]; exists {
			return planFailure("validation", "duplicate_step_id", ErrInvalid)
		}

		ctx.seenSteps[step.ID()] = i

		if err := validateDAGDependencies(step, ctx.seenSteps, i); err != nil {
			return err
		}

		if err := ctx.tracker.recordAndValidateStep(step, ctx); err != nil {
			return err
		}
	}

	return nil
}

func newPlanTracker(stepCount, sessionCount, windowCount, paneCount int) planTracker {
	return planTracker{
		createdWindows:         make(map[string]string, windowCount),
		createdPanes:           make(map[string]string, paneCount),
		createdSessions:        make(map[string]string, sessionCount),
		bootstrapSessions:      make(map[string]bool, sessionCount),
		closedBootstraps:       make(map[string]string, sessionCount),
		layoutWindows:          make(map[string]string, windowCount),
		selectedPaneWindows:    make(map[string]string, windowCount),
		zoomedWindows:          make(map[string]string, windowCount),
		selectedWindowSessions: make(map[string]string, sessionCount),
		handledLinks:           make(map[linkSlotKey]bool, stepCount),
		sessionLinkStepIDs:     make(map[string][]string, sessionCount),
		windowPaneIDs:          make(map[string][]string, windowCount),
		windowFinalStepIDs:     make(map[string]string, windowCount),
		titledPanes:            make(map[string]string, windowCount),
	}
}

func validateMissingRequirements(p Plan) error {
	expected := collectMissingRequirements(p.SourceGraph)
	if len(p.MissingRequirements) != len(expected) {
		return planFailure("validation", "missing_requirements_mismatch", ErrInvalid)
	}

	for i := range expected {
		if p.MissingRequirements[i] != expected[i] {
			return planFailure("validation", "missing_requirements_mismatch", ErrInvalid)
		}
	}

	return nil
}

func validateSelectionAndClosure(p Plan) error {
	if err := validateSortedSelectionIDs(p); err != nil {
		return err
	}

	return validateClosureMatch(p)
}

func validateSortedSelectionIDs(p Plan) error {
	if err := validateSortedIDs(p.RequestedSessions, "requested"); err != nil {
		return err
	}

	if err := validateSortedIDs(p.AddedSessions, "added"); err != nil {
		return err
	}

	return validateSortedIDs(p.ClosureSessions, "closure")
}

func validateClosureMatch(p Plan) error {
	reqMap := make(map[string]bool, len(p.RequestedSessions))
	for _, id := range p.RequestedSessions {
		reqMap[id] = true
	}

	for _, id := range p.AddedSessions {
		if reqMap[id] {
			return planFailure("validation", "overlapping_requested_and_added", ErrInvalid)
		}
	}

	expectedClosure := make([]string, 0, len(p.RequestedSessions)+len(p.AddedSessions))
	expectedClosure = append(expectedClosure, p.RequestedSessions...)
	expectedClosure = append(expectedClosure, p.AddedSessions...)
	slices.Sort(expectedClosure)

	if !slices.Equal(expectedClosure, p.ClosureSessions) {
		return planFailure("validation", "closure_mismatch", ErrInvalid)
	}

	if len(p.ClosureSessions) != len(p.SourceGraph.Sessions) {
		return planFailure("validation", "closure_source_graph_mismatch", ErrInvalid)
	}

	sourceIDs := make([]string, len(p.SourceGraph.Sessions))
	for i, s := range p.SourceGraph.Sessions {
		sourceIDs[i] = s.ID
	}

	slices.Sort(sourceIDs)

	if !slices.Equal(sourceIDs, p.ClosureSessions) {
		return planFailure("validation", "closure_source_graph_ids_mismatch", ErrInvalid)
	}

	closure, err := p.SourceGraph.Closure(p.RequestedSessions)
	if err != nil {
		return planFailure("validation", "invalid_closure", err)
	}

	slices.Sort(closure)

	if !slices.Equal(closure, p.ClosureSessions) {
		return planFailure("validation", "extraneous_closure", ErrInvalid)
	}

	return nil
}

func validateSortedIDs(ids []string, phase string) error {
	for i, id := range ids {
		if !model.ValidID(id) {
			return planFailure("validation", "malformed_"+phase+"_id", ErrInvalid)
		}

		if i > 0 && ids[i-1] >= id {
			return planFailure("validation", "unsorted_or_duplicate_"+phase+"_ids", ErrInvalid)
		}
	}

	return nil
}

func validateSummary(p Plan) error {
	if err := validateBasicEntityCounts(p.Summary, p.SourceGraph, len(p.Steps)); err != nil {
		return err
	}

	return validateSharedCounts(p.Summary, p.SourceGraph)
}

func validateBasicEntityCounts(s PlanSummary, g model.Manifest, stepCount int) error {
	if s.SessionCount != len(g.Sessions) {
		return planFailure("validation", "summary_session_count_mismatch", ErrInvalid)
	}

	if s.GroupCount != len(g.Groups) {
		return planFailure("validation", "summary_group_count_mismatch", ErrInvalid)
	}

	if s.WindowCount != len(g.Windows) {
		return planFailure("validation", "summary_window_count_mismatch", ErrInvalid)
	}

	if s.PaneCount != len(g.Panes) {
		return planFailure("validation", "summary_pane_count_mismatch", ErrInvalid)
	}

	if s.LinkCount != len(g.Links) {
		return planFailure("validation", "summary_link_count_mismatch", ErrInvalid)
	}

	if s.StepCount != stepCount {
		return planFailure("validation", "summary_step_count_mismatch", ErrInvalid)
	}

	return nil
}

func validateSharedCounts(s PlanSummary, g model.Manifest) error {
	windowLinkCounts := make(map[string]int, len(g.Windows))
	for _, l := range g.Links {
		windowLinkCounts[l.Window]++
	}

	panesByWindow := make(map[string]int, len(g.Windows))
	for _, p := range g.Panes {
		panesByWindow[p.Window]++
	}

	expectedSharedWindows := 0
	expectedSharedPanes := 0

	for _, w := range g.Windows {
		if windowLinkCounts[w.ID] > 1 {
			expectedSharedWindows++
			expectedSharedPanes += panesByWindow[w.ID]
		}
	}

	if s.SharedWindowCount != expectedSharedWindows {
		return planFailure("validation", "summary_shared_window_count_mismatch", ErrInvalid)
	}

	if s.SharedPaneCount != expectedSharedPanes {
		return planFailure("validation", "summary_shared_pane_count_mismatch", ErrInvalid)
	}

	return nil
}

func validateDAGDependencies(step Step, seenSteps map[string]int, currentIndex int) error {
	for _, dep := range step.DependsOn() {
		depIdx, exists := seenSteps[dep]
		if !exists {
			return planFailure("validation", "dangling_dependency", ErrInvalid)
		}

		if depIdx >= currentIndex {
			return planFailure("validation", "cyclic_dependency", ErrInvalid)
		}
	}

	return nil
}

func (t *planTracker) recordAndValidateStep(step Step, ctx validationContext) error {
	switch step := step.(type) {
	case *CreateSessionStep:
		return t.recordCreateSession(step, ctx)
	case *PlaceInitialWindowStep:
		return t.validateInitialPlacement(step)
	case *CreateWindowStep:
		return t.recordCreateWindow(step, ctx)
	case *SplitPaneStep:
		return t.recordSplitPane(step, ctx)
	case *LinkWindowStep:
		return t.recordLinkWindow(step, ctx)
	case *CloseBootstrapWindowStep:
		return t.recordCloseBootstrap(step, ctx)
	case *ApplyLayoutStep:
		return t.recordApplyLayout(step, ctx)
	case *SetPaneTitleStep:
		return t.recordSetPaneTitle(step, ctx)
	case *SelectPaneStep:
		return t.recordSelectPane(step, ctx)
	case *ApplyZoomStep:
		return t.recordApplyZoom(step, ctx)
	case *SelectWindowStep:
		return t.recordSelectWindow(step, ctx)
	}

	return planFailure("validation", "unknown_step_kind", ErrInvalid)
}

func (t *planTracker) validateInitialPlacement(step *PlaceInitialWindowStep) error {
	if !slices.Contains(step.DependsOn(), t.createdSessions[step.SessionID]) {
		return planFailure("validation", "initial_window_missing_session", ErrInvalid)
	}

	return nil
}

func (t *planTracker) recordCreateSession(cs *CreateSessionStep, ctx validationContext) error {
	if !model.ValidID(cs.SessionID) {
		return planFailure("validation", "invalid_create_session", ErrInvalid)
	}

	if _, exists := t.createdSessions[cs.SessionID]; exists {
		return planFailure("validation", "duplicate_session_creation", ErrInvalid)
	}

	if err := validateCreateSessionPayload(cs, ctx.graph.Sessions, ctx.groupNames); err != nil {
		return err
	}

	if cs.BootstrapWindow {
		t.bootstrapSessions[cs.SessionID] = true
	}

	t.createdSessions[cs.SessionID] = cs.ID()

	return t.recordCreateSessionInitialWindow(cs, ctx)
}

func validateCreateSessionPayload(cs *CreateSessionStep, sessions []model.Session, groupNames map[string]model.Bytes) error {
	var sess *model.Session

	for i := range sessions {
		if sessions[i].ID == cs.SessionID {
			sess = &sessions[i]
			break
		}
	}

	if sess == nil {
		return planFailure("validation", "unknown_session_in_create_step", ErrInvalid)
	}

	if !bytes.Equal(cs.Name, sess.Name) {
		return planFailure("validation", "create_session_name_mismatch", ErrInvalid)
	}

	if !bytes.Equal(cs.Directory, sess.Directory) {
		return planFailure("validation", "create_session_directory_mismatch", ErrInvalid)
	}

	if cs.Group != sess.Group {
		return planFailure("validation", "create_session_group_mismatch", ErrInvalid)
	}

	if cs.Group != "" {
		grpName := groupNames[cs.Group]
		if !bytes.Equal(cs.GroupName, grpName) {
			return planFailure("validation", "create_session_group_name_mismatch", ErrInvalid)
		}
	} else if len(cs.GroupName) > 0 {
		return planFailure("validation", "unexpected_group_name_without_group", ErrInvalid)
	}

	if cs.InitialWindowID == "" && len(cs.InitialWindowName) > 0 {
		return planFailure("validation", "unexpected_initial_window_name_without_initial_window", ErrInvalid)
	}

	return nil
}

func validateInitialWindowMetadata(cs *CreateSessionStep, windows []model.Window) error {
	if cs.InitialWindowID == "" {
		if cs.Width != 0 || cs.Height != 0 {
			return planFailure("validation", "unexpected_window_size_without_initial_window", ErrInvalid)
		}

		if len(cs.InitialWindowName) != 0 {
			return planFailure("validation", "unexpected_initial_window_name_without_initial_window", ErrInvalid)
		}

		return nil
	}

	for i := range windows {
		if windows[i].ID == cs.InitialWindowID {
			if !bytes.Equal(cs.InitialWindowName, windows[i].Name) {
				return planFailure("validation", "create_session_initial_window_name_mismatch", ErrInvalid)
			}

			if cs.Width != windows[i].Width || cs.Height != windows[i].Height {
				return planFailure("validation", "create_session_window_size_mismatch", ErrInvalid)
			}

			return nil
		}
	}

	return planFailure("validation", "unknown_initial_window", ErrInvalid)
}

func (t *planTracker) recordCreateSessionInitialWindow(cs *CreateSessionStep, ctx validationContext) error {
	switch {
	case cs.InitialWindowID != "":
		if cs.BootstrapWindow {
			return planFailure("validation", "bootstrap_with_initial_window", ErrInvalid)
		}

		if _, exists := t.createdWindows[cs.InitialWindowID]; exists {
			return planFailure("validation", "duplicate_window_creation", ErrInvalid)
		}

		if err := validateInitialWindowMetadata(cs, ctx.graph.Windows); err != nil {
			return err
		}

		t.createdWindows[cs.InitialWindowID] = "create-session:" + cs.SessionID
		t.handledLinks[linkSlotKey{session: cs.SessionID, window: cs.InitialWindowID, index: cs.InitialWindowIndex}] = true
	case cs.Width != 0 || cs.Height != 0:
		return planFailure("validation", "unexpected_window_size_without_initial_window", ErrInvalid)
	case len(cs.InitialWindowName) != 0:
		return planFailure("validation", "unexpected_initial_window_name_without_initial_window", ErrInvalid)
	}

	if cs.InitialPaneID != "" {
		if _, exists := t.createdPanes[cs.InitialPaneID]; exists {
			return planFailure("validation", "duplicate_pane_creation", ErrInvalid)
		}

		t.createdPanes[cs.InitialPaneID] = "create-session:" + cs.SessionID
		if cs.InitialWindowID != "" {
			t.windowPaneIDs[cs.InitialWindowID] = append(t.windowPaneIDs[cs.InitialWindowID], cs.InitialPaneID)
		}
	}

	return nil
}

func validateCreateWindowPayload(cw *CreateWindowStep, windows []model.Window) error {
	for i := range windows {
		if windows[i].ID == cw.WindowID {
			if !bytes.Equal(cw.Name, windows[i].Name) {
				return planFailure("validation", "create_window_name_mismatch", ErrInvalid)
			}

			if cw.Width != windows[i].Width || cw.Height != windows[i].Height {
				return planFailure("validation", "create_window_size_mismatch", ErrInvalid)
			}

			return nil
		}
	}

	return planFailure("validation", "unknown_window_in_create_step", ErrInvalid)
}

func (t *planTracker) recordCreateWindow(cw *CreateWindowStep, ctx validationContext) error {
	if !model.ValidID(cw.WindowID) || !model.ValidID(cw.OwnerSessionID) {
		return planFailure("validation", "invalid_create_window", ErrInvalid)
	}

	if _, exists := t.createdWindows[cw.WindowID]; exists {
		return planFailure("validation", "duplicate_window_creation", ErrInvalid)
	}

	sessStep, sessExists := t.createdSessions[cw.OwnerSessionID]
	if !sessExists {
		return planFailure("validation", "create_window_owner_session_not_created", ErrInvalid)
	}

	if !slices.Contains(cw.DependsOn(), sessStep) {
		return planFailure("validation", "create_window_missing_owner_dep", ErrInvalid)
	}

	if err := validateCreateWindowPayload(cw, ctx.graph.Windows); err != nil {
		return err
	}

	if _, exists := t.createdPanes[cw.InitialPaneID]; exists {
		return planFailure("validation", "duplicate_pane_creation", ErrInvalid)
	}

	t.createdWindows[cw.WindowID] = cw.ID()
	t.createdPanes[cw.InitialPaneID] = cw.ID()
	t.windowPaneIDs[cw.WindowID] = append(t.windowPaneIDs[cw.WindowID], cw.InitialPaneID)
	t.handledLinks[linkSlotKey{session: cw.OwnerSessionID, window: cw.WindowID, index: cw.Index}] = true

	return nil
}

func (t *planTracker) recordSplitPane(sp *SplitPaneStep, ctx validationContext) error {
	if !model.ValidID(sp.PaneID) || !model.ValidID(sp.TargetPaneID) || !model.ValidID(sp.WindowID) {
		return planFailure("validation", "invalid_split_pane", ErrInvalid)
	}

	if _, exists := t.createdPanes[sp.PaneID]; exists {
		return planFailure("validation", "duplicate_pane_creation", ErrInvalid)
	}

	targetStep, targetExists := t.createdPanes[sp.TargetPaneID]
	if !targetExists {
		return planFailure("validation", "split_target_pane_not_created", ErrInvalid)
	}

	if !slices.Contains(sp.DependsOn(), targetStep) {
		return planFailure("validation", "split_missing_target_dep", ErrInvalid)
	}

	if err := validateSplitDimensionsAndTarget(sp, ctx.graph); err != nil {
		return err
	}

	t.createdPanes[sp.PaneID] = sp.ID()
	t.windowPaneIDs[sp.WindowID] = append(t.windowPaneIDs[sp.WindowID], sp.PaneID)

	return nil
}

func validateSplitDimensionsAndTarget(sp *SplitPaneStep, g model.Manifest) error {
	if sp.Axis != "x" && sp.Axis != "y" {
		return planFailure("validation", "invalid_split_axis", ErrInvalid)
	}

	if sp.Width < 1 || sp.Height < 1 {
		return planFailure("validation", "invalid_split_dimensions", ErrInvalid)
	}

	var pane *model.Pane

	for i := range g.Panes {
		if g.Panes[i].ID == sp.PaneID {
			pane = &g.Panes[i]
			break
		}
	}

	if pane == nil {
		return planFailure("validation", "unknown_pane_in_split_step", ErrInvalid)
	}

	if pane.Window != sp.WindowID {
		return planFailure("validation", "split_window_mismatch", ErrInvalid)
	}

	if !bytes.Equal(sp.Directory, pane.Directory) || !bytes.Equal(sp.Title, pane.Title) {
		return planFailure("validation", "split_pane_metadata_mismatch", ErrInvalid)
	}

	return nil
}

func (t *planTracker) recordLinkWindow(lw *LinkWindowStep, ctx validationContext) error {
	if !model.ValidID(lw.SessionID) || !model.ValidID(lw.WindowID) {
		return planFailure("validation", "invalid_link_window", ErrInvalid)
	}

	key := linkSlotKey{session: lw.SessionID, window: lw.WindowID, index: lw.Index}
	if t.handledLinks[key] {
		return planFailure("validation", "duplicate_link_slot", ErrInvalid)
	}

	sessStep, sessExists := t.createdSessions[lw.SessionID]
	if !sessExists {
		return planFailure("validation", "link_session_not_created", ErrInvalid)
	}

	winStep, winExists := t.createdWindows[lw.WindowID]
	if !winExists {
		return planFailure("validation", "link_window_not_created", ErrInvalid)
	}

	if !slices.Contains(lw.DependsOn(), sessStep) || !slices.Contains(lw.DependsOn(), winStep) {
		return planFailure("validation", "link_missing_prereq_deps", ErrInvalid)
	}

	if err := validateLinkStepMatch(lw, ctx.graph.Links); err != nil {
		return err
	}

	t.handledLinks[key] = true
	t.sessionLinkStepIDs[lw.SessionID] = append(t.sessionLinkStepIDs[lw.SessionID], lw.ID())

	return nil
}

func validateLinkStepMatch(lw *LinkWindowStep, links []model.Link) error {
	for _, l := range links {
		if l.Session == lw.SessionID && l.Window == lw.WindowID && l.Index == lw.Index {
			if l.Active != lw.Active || l.Last != lw.Last || !bytes.Equal(l.Flags, lw.Flags) {
				return planFailure("validation", "link_metadata_mismatch", ErrInvalid)
			}

			return nil
		}
	}

	return planFailure("validation", "unmatched_link_step", ErrInvalid)
}

func (t *planTracker) recordCloseBootstrap(cb *CloseBootstrapWindowStep, _ validationContext) error {
	if !model.ValidID(cb.SessionID) {
		return planFailure("validation", "invalid_close_bootstrap", ErrInvalid)
	}

	if !t.bootstrapSessions[cb.SessionID] {
		return planFailure("validation", "close_bootstrap_for_non_bootstrap_session", ErrInvalid)
	}

	if _, exists := t.closedBootstraps[cb.SessionID]; exists {
		return planFailure("validation", "duplicate_close_bootstrap", ErrInvalid)
	}

	t.closedBootstraps[cb.SessionID] = cb.ID()

	return nil
}

func (t *planTracker) recordApplyLayout(al *ApplyLayoutStep, ctx validationContext) error {
	if !model.ValidID(al.WindowID) {
		return planFailure("validation", "invalid_apply_layout", ErrInvalid)
	}

	if _, exists := t.layoutWindows[al.WindowID]; exists {
		return planFailure("validation", "duplicate_layout_step", ErrInvalid)
	}

	winStep, winExists := t.createdWindows[al.WindowID]
	if !winExists {
		return planFailure("validation", "layout_window_not_created", ErrInvalid)
	}

	if !slices.Contains(al.DependsOn(), winStep) {
		return planFailure("validation", "layout_missing_window_dep", ErrInvalid)
	}

	var win *model.Window

	for i := range ctx.graph.Windows {
		if ctx.graph.Windows[i].ID == al.WindowID {
			win = &ctx.graph.Windows[i]
			break
		}
	}

	if win == nil {
		return planFailure("validation", "unknown_window_in_layout_step", ErrInvalid)
	}

	if al.Width != win.Width || al.Height != win.Height {
		return planFailure("validation", "layout_dimensions_mismatch", ErrInvalid)
	}

	t.layoutWindows[al.WindowID] = al.ID()
	t.windowFinalStepIDs[al.WindowID] = al.ID()

	return nil
}

func (t *planTracker) validateSelectPaneDeps(step Step, windowID string, windows []model.Window) error {
	var expectedPrevStep string

	for i := range windows {
		if windows[i].ID == windowID {
			initPane := firstLeaf(windows[i].Layout)
			if initPane != "" {
				titleStep, exists := t.titledPanes[initPane]
				if !exists || !slices.Contains(step.DependsOn(), titleStep) {
					return planFailure("validation", "select_pane_missing_title_dep", ErrInvalid)
				}

				expectedPrevStep = titleStep
			}

			break
		}
	}

	if expectedPrevStep == "" {
		expectedPrevStep = t.layoutWindows[windowID]
	}

	if expectedPrevStep == "" || !slices.Contains(step.DependsOn(), expectedPrevStep) {
		return planFailure("validation", "select_pane_missing_layout_dep", ErrInvalid)
	}

	return nil
}

func validateSelectPaneLast(panes []model.Pane, windowID, lastPaneID string) error {
	want := ""

	for i := range panes {
		if panes[i].Window == windowID && panes[i].Last {
			want = panes[i].ID
		}
	}

	if lastPaneID != want {
		return planFailure("validation", "select_pane_last_mismatch", ErrInvalid)
	}

	return nil
}

func validateSelectPaneActive(panes []model.Pane, windowID, paneID string) error {
	for i := range panes {
		if panes[i].ID == paneID && panes[i].Window == windowID {
			if !panes[i].Active {
				return planFailure("validation", "select_pane_not_active_in_source", ErrInvalid)
			}

			return nil
		}
	}

	return planFailure("validation", "select_pane_not_active_in_source", ErrInvalid)
}

func (t *planTracker) recordSelectPane(sp *SelectPaneStep, ctx validationContext) error {
	if !model.ValidID(sp.WindowID) || !model.ValidID(sp.PaneID) {
		return planFailure("validation", "invalid_select_pane", ErrInvalid)
	}

	if _, exists := t.selectedPaneWindows[sp.WindowID]; exists {
		return planFailure("validation", "duplicate_select_pane", ErrInvalid)
	}

	if err := t.validateSelectPaneDeps(sp, sp.WindowID, ctx.graph.Windows); err != nil {
		return err
	}

	if err := validateSelectPaneActive(ctx.graph.Panes, sp.WindowID, sp.PaneID); err != nil {
		return err
	}

	if err := validateSelectPaneLast(ctx.graph.Panes, sp.WindowID, sp.LastPaneID); err != nil {
		return err
	}

	t.selectedPaneWindows[sp.WindowID] = sp.ID()
	t.windowFinalStepIDs[sp.WindowID] = sp.ID()

	return nil
}

func validateSetPaneTitleMatch(spt *SetPaneTitleStep, panes []model.Pane) error {
	for i := range panes {
		if panes[i].ID == spt.PaneID && panes[i].Window == spt.WindowID {
			if !bytes.Equal(spt.Title, panes[i].Title) {
				return planFailure("step", "set_pane_title_mismatch", ErrInvalid)
			}

			return nil
		}
	}

	return planFailure("validation", "unknown_pane_in_set_pane_title_step", ErrInvalid)
}

func (t *planTracker) recordSetPaneTitle(spt *SetPaneTitleStep, ctx validationContext) error {
	if !model.ValidID(spt.WindowID) || !model.ValidID(spt.PaneID) {
		return planFailure("validation", "invalid_set_pane_title", ErrInvalid)
	}

	if _, exists := t.titledPanes[spt.PaneID]; exists {
		return planFailure("validation", "duplicate_set_pane_title", ErrInvalid)
	}

	if _, exists := t.selectedPaneWindows[spt.WindowID]; exists {
		return planFailure("validation", "set_pane_title_after_select_pane", ErrInvalid)
	}

	paneStep, paneExists := t.createdPanes[spt.PaneID]
	if !paneExists || !slices.Contains(spt.DependsOn(), paneStep) {
		return planFailure("validation", "set_pane_title_missing_pane_dep", ErrInvalid)
	}

	layoutStep, layoutExists := t.layoutWindows[spt.WindowID]
	if !layoutExists || !slices.Contains(spt.DependsOn(), layoutStep) {
		return planFailure("validation", "set_pane_title_missing_layout_dep", ErrInvalid)
	}

	if err := validateSetPaneTitleMatch(spt, ctx.graph.Panes); err != nil {
		return err
	}

	t.titledPanes[spt.PaneID] = spt.ID()
	t.windowFinalStepIDs[spt.WindowID] = spt.ID()

	return nil
}

func (t *planTracker) recordApplyZoom(az *ApplyZoomStep, ctx validationContext) error {
	if !model.ValidID(az.WindowID) || !model.ValidID(az.PaneID) {
		return planFailure("validation", "invalid_apply_zoom", ErrInvalid)
	}

	if _, exists := t.zoomedWindows[az.WindowID]; exists {
		return planFailure("validation", "duplicate_apply_zoom", ErrInvalid)
	}

	spStep, spExists := t.selectedPaneWindows[az.WindowID]
	if !spExists || !slices.Contains(az.DependsOn(), spStep) {
		return planFailure("validation", "zoom_missing_select_pane_dep", ErrInvalid)
	}

	var win *model.Window

	for i := range ctx.graph.Windows {
		if ctx.graph.Windows[i].ID == az.WindowID {
			win = &ctx.graph.Windows[i]
			break
		}
	}

	if win == nil || !win.Zoomed {
		return planFailure("validation", "zoom_applied_to_unzoomed_window", ErrInvalid)
	}

	t.zoomedWindows[az.WindowID] = az.ID()
	t.windowFinalStepIDs[az.WindowID] = az.ID()

	return nil
}

func (t *planTracker) recordSelectWindow(sw *SelectWindowStep, ctx validationContext) error {
	if !model.ValidID(sw.SessionID) || !model.ValidID(sw.ActiveWindowID) {
		return planFailure("validation", "invalid_select_window", ErrInvalid)
	}

	if _, exists := t.selectedWindowSessions[sw.SessionID]; exists {
		return planFailure("validation", "duplicate_select_window", ErrInvalid)
	}

	if err := t.validateSelectWindowPrereqs(sw, sw.SessionID, ctx.graph.Links); err != nil {
		return err
	}

	if err := validateSelectWindowLinks(sw, ctx.graph.Links); err != nil {
		return err
	}

	t.selectedWindowSessions[sw.SessionID] = sw.ID()

	return nil
}

func (t *planTracker) validateSelectWindowPrereqs(step Step, sessionID string, links []model.Link) error {
	sessStep, sessExists := t.createdSessions[sessionID]
	if !sessExists {
		return planFailure("validation", "select_window_session_not_created", ErrInvalid)
	}

	if !slices.Contains(step.DependsOn(), sessStep) {
		return planFailure("validation", "select_window_missing_session_dep", ErrInvalid)
	}

	for _, linkStep := range t.sessionLinkStepIDs[sessionID] {
		if !slices.Contains(step.DependsOn(), linkStep) {
			return planFailure("validation", "select_window_missing_link_dep", ErrInvalid)
		}
	}

	if cbStep, hasCB := t.closedBootstraps[sessionID]; hasCB {
		if !slices.Contains(step.DependsOn(), cbStep) {
			return planFailure("validation", "select_window_missing_close_bootstrap_dep", ErrInvalid)
		}
	}

	for _, l := range links {
		if l.Session != sessionID {
			continue
		}

		finalStep, finalExists := t.windowFinalStepIDs[l.Window]
		if !finalExists || !slices.Contains(step.DependsOn(), finalStep) {
			return planFailure("validation", "select_window_missing_window_final_dep", ErrInvalid)
		}
	}

	return nil
}

func validateSelectWindowLinks(sw *SelectWindowStep, links []model.Link) error {
	activeLink, lastLink := findSessionActiveAndLastLinks(sw.SessionID, links)
	if activeLink == nil || activeLink.Window != sw.ActiveWindowID || activeLink.Index != sw.ActiveIndex {
		return planFailure("validation", "select_window_active_mismatch", ErrInvalid)
	}

	if lastLink != nil {
		if sw.LastWindowID != lastLink.Window || sw.LastIndex != lastLink.Index {
			return planFailure("validation", "select_window_last_mismatch", ErrInvalid)
		}
	} else if sw.LastWindowID != "" || sw.LastIndex != -1 {
		return planFailure("validation", "select_window_unexpected_last", ErrInvalid)
	}

	return nil
}

func findSessionActiveAndLastLinks(sessionID string, links []model.Link) (*model.Link, *model.Link) {
	var active, last *model.Link

	for i := range links {
		l := &links[i]
		if l.Session == sessionID {
			if l.Active {
				active = l
			}

			if l.Last {
				last = l
			}
		}
	}

	return active, last
}

func (t *planTracker) checkEntityCoverage(g model.Manifest) error {
	if len(t.createdSessions) != len(g.Sessions) {
		return planFailure("validation", "session_creation_mismatch", ErrInvalid)
	}

	for _, s := range g.Sessions {
		if _, exists := t.createdSessions[s.ID]; !exists {
			return planFailure("validation", "missing_session_creation", ErrInvalid)
		}
	}

	if len(t.createdWindows) != len(g.Windows) {
		return planFailure("validation", "window_creation_mismatch", ErrInvalid)
	}

	for _, w := range g.Windows {
		if _, exists := t.createdWindows[w.ID]; !exists {
			return planFailure("validation", "missing_window_creation", ErrInvalid)
		}
	}

	if len(t.createdPanes) != len(g.Panes) {
		return planFailure("validation", "pane_creation_mismatch", ErrInvalid)
	}

	for _, pane := range g.Panes {
		if _, exists := t.createdPanes[pane.ID]; !exists {
			return planFailure("validation", "missing_pane_creation", ErrInvalid)
		}
	}

	return nil
}

func (t *planTracker) checkLinkCoverage(g model.Manifest) error {
	groupOf := make(map[string]string, len(g.Sessions))
	leaderOf := make(map[string]string, len(g.Groups))

	for _, grp := range g.Groups {
		if len(grp.Members) > 0 {
			leaderOf[grp.ID] = grp.Members[0]
			for _, m := range grp.Members {
				groupOf[m] = grp.ID
			}
		}
	}

	for _, l := range g.Links {
		key := linkSlotKey{session: l.Session, window: l.Window, index: l.Index}
		if t.handledLinks[key] {
			continue
		}

		if grpID := groupOf[l.Session]; grpID != "" {
			leader := leaderOf[grpID]
			leaderKey := linkSlotKey{session: leader, window: l.Window, index: l.Index}

			if t.handledLinks[leaderKey] {
				continue
			}
		}

		return planFailure("validation", "missing_link_coverage", ErrInvalid)
	}

	return nil
}

func (t *planTracker) checkFinalizationCoverage(g model.Manifest) error {
	if len(t.layoutWindows) != len(g.Windows) {
		return planFailure("validation", "missing_layout_step", ErrInvalid)
	}

	for _, w := range g.Windows {
		pInit := firstLeaf(w.Layout)
		if pInit != "" {
			if _, exists := t.titledPanes[pInit]; !exists {
				return planFailure("validation", "missing_set_pane_title_step", ErrInvalid)
			}
		}
	}

	if len(t.selectedPaneWindows) != len(g.Windows) {
		return planFailure("validation", "missing_select_pane_step", ErrInvalid)
	}

	if len(t.selectedWindowSessions) != len(g.Sessions) {
		return planFailure("validation", "missing_select_window_step", ErrInvalid)
	}

	for _, w := range g.Windows {
		if w.Zoomed {
			if _, exists := t.zoomedWindows[w.ID]; !exists {
				return planFailure("validation", "missing_zoom_step", ErrInvalid)
			}
		} else {
			if _, exists := t.zoomedWindows[w.ID]; exists {
				return planFailure("validation", "unexpected_zoom_step", ErrInvalid)
			}
		}
	}

	return nil
}
