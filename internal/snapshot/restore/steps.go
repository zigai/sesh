package restore

import (
	"fmt"
	"slices"

	"github.com/zigai/sesh/internal/snapshot/model"
)

const (
	StepCreateSession        StepKind = "create_session"
	StepPlaceInitialWindow   StepKind = "place_initial_window"
	StepCreateWindow         StepKind = "create_window"
	StepSplitPane            StepKind = "split_pane"
	StepLinkWindow           StepKind = "link_window"
	StepCloseBootstrapWindow StepKind = "close_bootstrap_window"
	StepApplyLayout          StepKind = "apply_layout"
	StepSelectPane           StepKind = "select_pane"
	StepApplyZoom            StepKind = "apply_zoom"
	StepSelectWindow         StepKind = "select_window"
	StepSetPaneTitle         StepKind = "set_pane_title"
)

type StepKind string

type Step struct {
	ID                   string                    `json:"id"`
	Kind                 StepKind                  `json:"kind"`
	DependsOn            []string                  `json:"dependsOn,omitempty"`
	CreateSession        *CreateSessionStep        `json:"createSession,omitempty"`
	PlaceInitialWindow   *PlaceInitialWindowStep   `json:"placeInitialWindow,omitempty"`
	CreateWindow         *CreateWindowStep         `json:"createWindow,omitempty"`
	SplitPane            *SplitPaneStep            `json:"splitPane,omitempty"`
	LinkWindow           *LinkWindowStep           `json:"linkWindow,omitempty"`
	CloseBootstrapWindow *CloseBootstrapWindowStep `json:"closeBootstrapWindow,omitempty"`
	ApplyLayout          *ApplyLayoutStep          `json:"applyLayout,omitempty"`
	SelectPane           *SelectPaneStep           `json:"selectPane,omitempty"`
	ApplyZoom            *ApplyZoomStep            `json:"applyZoom,omitempty"`
	SelectWindow         *SelectWindowStep         `json:"selectWindow,omitempty"`
	SetPaneTitle         *SetPaneTitleStep         `json:"setPaneTitle,omitempty"`
}

type CreateSessionStep struct {
	SessionID            string           `json:"sessionId"`
	Name                 model.Bytes      `json:"name"`
	Directory            model.Bytes      `json:"directory"`
	Group                string           `json:"group,omitempty"`
	GroupName            model.Bytes      `json:"groupName,omitempty"`
	GroupLeaderSessionID string           `json:"groupLeaderSessionId,omitempty"`
	InitialWindowID      string           `json:"initialWindowId,omitempty"`
	InitialWindowName    model.Bytes      `json:"initialWindowName,omitempty"`
	InitialWindowIndex   int              `json:"initialWindowIndex,omitempty"`
	InitialPaneID        string           `json:"initialPaneId,omitempty"`
	Width                int              `json:"width,omitempty"`
	Height               int              `json:"height,omitempty"`
	BootstrapWindow      bool             `json:"bootstrapWindow,omitempty"`
	Options              []model.Option   `json:"options,omitempty"`
	Environment          []model.Variable `json:"environment,omitempty"`
}

type PlaceInitialWindowStep struct {
	SessionID string `json:"sessionId"`
	WindowID  string `json:"windowId,omitempty"`
	Index     int    `json:"index"`
}

type CreateWindowStep struct {
	WindowID       string         `json:"windowId"`
	OwnerSessionID string         `json:"ownerSessionId"`
	OwnerGroupID   string         `json:"ownerGroupId,omitempty"`
	Index          int            `json:"index"`
	Name           model.Bytes    `json:"name"`
	InitialPaneID  string         `json:"initialPaneId"`
	Width          int            `json:"width,omitempty"`
	Height         int            `json:"height,omitempty"`
	Options        []model.Option `json:"options,omitempty"`
}

type SplitPaneStep struct {
	PaneID        string         `json:"paneId"`
	WindowID      string         `json:"windowId"`
	TargetPaneID  string         `json:"targetPaneId"`
	Axis          string         `json:"axis"`
	Width         int            `json:"width"`
	Height        int            `json:"height"`
	SubtreeWidth  int            `json:"subtreeWidth"`
	SubtreeHeight int            `json:"subtreeHeight"`
	Directory     model.Bytes    `json:"directory"`
	Title         model.Bytes    `json:"title"`
	Options       []model.Option `json:"options,omitempty"`
	Launch        model.Launch   `json:"launch"`
	Terminal      model.Terminal `json:"terminal"`
}

type LinkWindowStep struct {
	SessionID string      `json:"sessionId"`
	WindowID  string      `json:"windowId"`
	Index     int         `json:"index"`
	Flags     model.Bytes `json:"flags,omitempty"`
	Active    bool        `json:"active"`
	Last      bool        `json:"last"`
}

type CloseBootstrapWindowStep struct {
	SessionID string `json:"sessionId"`
}

type ApplyLayoutStep struct {
	WindowID string       `json:"windowId"`
	Layout   model.Layout `json:"layout"`
	Width    int          `json:"width"`
	Height   int          `json:"height"`
}

type SelectPaneStep struct {
	WindowID string `json:"windowId"`
	PaneID   string `json:"paneId"`
}

type ApplyZoomStep struct {
	WindowID string `json:"windowId"`
	PaneID   string `json:"paneId"`
}

type SelectWindowStep struct {
	SessionID      string `json:"sessionId"`
	ActiveWindowID string `json:"activeWindowId"`
	ActiveIndex    int    `json:"activeIndex"`
	LastWindowID   string `json:"lastWindowId,omitempty"`
	LastIndex      int    `json:"lastIndex"`
}

type SetPaneTitleStep struct {
	WindowID string      `json:"windowId"`
	PaneID   string      `json:"paneId"`
	Title    model.Bytes `json:"title"`
}

type splitPlan struct {
	newPaneID     string
	targetPaneID  string
	axis          string
	subtreeWidth  int
	subtreeHeight int
}

type entityIndexes struct {
	sessionsByID   map[string]model.Session
	windowsByID    map[string]model.Window
	panesByID      map[string]model.Pane
	panesByWindow  map[string][]model.Pane
	linksBySession map[string][]model.Link
	windowLinks    map[string][]model.Link
}

type groupRoles struct {
	leaderOf   map[string]string
	isFollower map[string]bool
	groupOf    map[string]string
	nameOf     map[string]model.Bytes
}

type windowOwnership struct {
	ownerSession       map[string]string
	ownerGroup         map[string]string
	initialWindowOf    map[string]string
	initialWindowIndex map[string]int
}

type stepRegistry struct {
	sessionStepID        map[string]string
	sessionReadyStepID   map[string]string
	windowStepID         map[string]string
	paneStepID           map[string]string
	handledLinks         map[linkSlotKey]bool
	linkStepIDs          map[string][]string
	closeBootstrapStepID map[string]string
	windowFinalStepID    map[string]string
}

type linkSlotKey struct {
	session string
	window  string
	index   int
}

func cloneBytes(b model.Bytes) model.Bytes {
	if b == nil {
		return nil
	}

	return slices.Clone(b)
}

func cloneOptions(opts []model.Option) []model.Option {
	if opts == nil {
		return nil
	}

	c := make([]model.Option, len(opts))
	for i, opt := range opts {
		c[i] = model.Option{
			Scope:     opt.Scope,
			Name:      cloneBytes(opt.Name),
			Value:     cloneBytes(opt.Value),
			Present:   opt.Present,
			Inherited: opt.Inherited,
		}
	}

	return c
}

func cloneVariables(vars []model.Variable) []model.Variable {
	if vars == nil {
		return nil
	}

	c := make([]model.Variable, len(vars))
	for i, v := range vars {
		c[i] = model.Variable{
			Name:   cloneBytes(v.Name),
			Value:  cloneBytes(v.Value),
			State:  v.State,
			Hidden: v.Hidden,
		}
	}

	return c
}

func cloneLayout(l model.Layout) model.Layout {
	c := model.Layout{
		Width:    l.Width,
		Height:   l.Height,
		Left:     l.Left,
		Top:      l.Top,
		Pane:     l.Pane,
		Axis:     l.Axis,
		Children: nil,
	}

	if len(l.Children) > 0 {
		c.Children = make([]model.Layout, len(l.Children))
		for i, child := range l.Children {
			c.Children[i] = cloneLayout(child)
		}
	}

	return c
}

func cloneLaunch(l model.Launch) model.Launch {
	var args []model.Bytes
	if l.Arguments != nil {
		args = make([]model.Bytes, len(l.Arguments))
		for i, a := range l.Arguments {
			args[i] = cloneBytes(a)
		}
	}

	return model.Launch{
		Executable:  cloneBytes(l.Executable),
		Arguments:   args,
		Directory:   cloneBytes(l.Directory),
		Environment: cloneVariables(l.Environment),
		Birth:       cloneBytes(l.Birth),
		Recovery:    l.Recovery,
		Reason:      l.Reason,
	}
}

func cloneTerminal(t model.Terminal) model.Terminal {
	return model.Terminal{
		Alternate: t.Alternate,
		Mode:      t.Mode,
		History:   t.History,
		CursorX:   t.CursorX,
		CursorY:   t.CursorY,
		Artifact:  t.Artifact,
	}
}

func newStep(id string, kind StepKind, deps []string) Step {
	deps = slices.Clone(deps)
	slices.Sort(deps)
	deps = slices.Compact(deps)

	return Step{
		ID:                   id,
		Kind:                 kind,
		DependsOn:            deps,
		CreateSession:        nil,
		PlaceInitialWindow:   nil,
		CreateWindow:         nil,
		SplitPane:            nil,
		LinkWindow:           nil,
		CloseBootstrapWindow: nil,
		ApplyLayout:          nil,
		SelectPane:           nil,
		ApplyZoom:            nil,
		SelectWindow:         nil,
		SetPaneTitle:         nil,
	}
}

func firstLeaf(node model.Layout) string {
	if node.Pane != "" {
		return node.Pane
	}

	if len(node.Children) > 0 {
		return firstLeaf(node.Children[0])
	}

	return ""
}

func remainingSpan(n model.Layout, startIndex int) (int, int) {
	if n.Axis == "x" {
		width := 0
		for j := startIndex; j < len(n.Children); j++ {
			width += n.Children[j].Width
			if j > startIndex {
				width++
			}
		}

		return width, n.Height
	}

	height := 0
	for j := startIndex; j < len(n.Children); j++ {
		height += n.Children[j].Height
		if j > startIndex {
			height++
		}
	}

	return n.Width, height
}

func decomposeLayout(node model.Layout) (string, []splitPlan) {
	initial := firstLeaf(node)

	var splits []splitPlan

	var walk func(n model.Layout)

	walk = func(n model.Layout) {
		if n.Pane != "" || len(n.Children) < 2 {
			return
		}

		for i := 1; i < len(n.Children); i++ {
			subW, subH := remainingSpan(n, i)
			splits = append(splits, splitPlan{
				newPaneID:     firstLeaf(n.Children[i]),
				targetPaneID:  firstLeaf(n.Children[i-1]),
				axis:          n.Axis,
				subtreeWidth:  subW,
				subtreeHeight: subH,
			})
		}

		for _, child := range n.Children {
			walk(child)
		}
	}

	walk(node)

	return initial, splits
}

func buildEntityIndexes(g model.Manifest) entityIndexes {
	sessionsByID := make(map[string]model.Session, len(g.Sessions))
	for _, s := range g.Sessions {
		sessionsByID[s.ID] = s
	}

	windowsByID := make(map[string]model.Window, len(g.Windows))
	for _, w := range g.Windows {
		windowsByID[w.ID] = w
	}

	panesByID := make(map[string]model.Pane, len(g.Panes))

	panesByWindow := make(map[string][]model.Pane, len(g.Windows))
	for _, p := range g.Panes {
		panesByID[p.ID] = p
		panesByWindow[p.Window] = append(panesByWindow[p.Window], p)
	}

	linksBySession := make(map[string][]model.Link, len(g.Sessions))

	windowLinks := make(map[string][]model.Link, len(g.Windows))
	for _, l := range g.Links {
		linksBySession[l.Session] = append(linksBySession[l.Session], l)
		windowLinks[l.Window] = append(windowLinks[l.Window], l)
	}

	for sID := range linksBySession {
		slices.SortFunc(linksBySession[sID], func(a, b model.Link) int {
			return a.Index - b.Index
		})
	}

	return entityIndexes{
		sessionsByID:   sessionsByID,
		windowsByID:    windowsByID,
		panesByID:      panesByID,
		panesByWindow:  panesByWindow,
		linksBySession: linksBySession,
		windowLinks:    windowLinks,
	}
}

func buildGroupRoles(g model.Manifest) groupRoles {
	leaderOf := make(map[string]string, len(g.Groups))
	isFollower := make(map[string]bool, len(g.Sessions))
	groupOf := make(map[string]string, len(g.Sessions))
	nameOf := make(map[string]model.Bytes, len(g.Groups))

	for _, grp := range g.Groups {
		nameOf[grp.ID] = cloneBytes(grp.Name)
		if len(grp.Members) > 0 {
			leader := grp.Members[0]

			leaderOf[grp.ID] = leader
			for _, m := range grp.Members {
				groupOf[m] = grp.ID
			}

			for _, f := range grp.Members[1:] {
				isFollower[f] = true
			}
		}
	}

	return groupRoles{
		leaderOf:   leaderOf,
		isFollower: isFollower,
		groupOf:    groupOf,
		nameOf:     nameOf,
	}
}

func findWindowOwner(links []model.Link, roles groupRoles, sessions []model.Session) (string, string) {
	for _, l := range links {
		if grpID := roles.groupOf[l.Session]; grpID != "" {
			return roles.leaderOf[grpID], grpID
		}
	}

	for _, s := range sessions {
		for _, l := range links {
			if l.Session == s.ID {
				return s.ID, ""
			}
		}
	}

	return "", ""
}

func assignWindowOwnership(g model.Manifest, roles groupRoles, idx entityIndexes) windowOwnership {
	ownerSession := make(map[string]string, len(g.Windows))
	ownerGroup := make(map[string]string, len(g.Windows))

	for _, w := range g.Windows {
		sessionID, groupID := findWindowOwner(idx.windowLinks[w.ID], roles, g.Sessions)
		ownerSession[w.ID] = sessionID
		ownerGroup[w.ID] = groupID
	}

	initialWindowOf := make(map[string]string, len(g.Sessions))
	initialWindowIndex := make(map[string]int, len(g.Sessions))

	for _, s := range g.Sessions {
		if roles.isFollower[s.ID] {
			continue
		}

		for _, l := range idx.linksBySession[s.ID] {
			if ownerSession[l.Window] == s.ID {
				if l.Index == 0 {
					initialWindowOf[s.ID] = l.Window
					initialWindowIndex[s.ID] = l.Index
				}

				break
			}
		}
	}

	return windowOwnership{
		ownerSession:       ownerSession,
		ownerGroup:         ownerGroup,
		initialWindowOf:    initialWindowOf,
		initialWindowIndex: initialWindowIndex,
	}
}

func newStepRegistry(sessionCount, windowCount, paneCount, linkCount int) *stepRegistry {
	return &stepRegistry{
		sessionStepID:        make(map[string]string, sessionCount),
		sessionReadyStepID:   make(map[string]string, sessionCount),
		windowStepID:         make(map[string]string, windowCount),
		paneStepID:           make(map[string]string, paneCount),
		handledLinks:         make(map[linkSlotKey]bool, linkCount),
		linkStepIDs:          make(map[string][]string, sessionCount),
		closeBootstrapStepID: make(map[string]string, sessionCount),
		windowFinalStepID:    make(map[string]string, windowCount),
	}
}

func placeInitialWindow(session, window string, index int, reg *stepRegistry) Step {
	id := "place-initial-window:" + session
	step := newStep(id, StepPlaceInitialWindow, []string{reg.sessionStepID[session]})
	step.PlaceInitialWindow = &PlaceInitialWindowStep{SessionID: session, WindowID: window, Index: index}
	reg.sessionReadyStepID[session] = id

	return step
}

func freeBootstrapIndex(links []model.Link) int {
	used := make(map[int]bool, len(links))
	for _, link := range links {
		used[link.Index] = true
	}

	for index := 0; ; index++ {
		if !used[index] {
			return index
		}
	}
}

func planNonFollowerInitialWindow(
	s model.Session,
	wInit model.Window,
	wIdx int,
	pInitID string,
	splits []splitPlan,
	idx entityIndexes,
	roles groupRoles,
	reg *stepRegistry,
) []Step {
	stepID := "create-session:" + s.ID
	steps := make([]Step, 0, 1+len(splits))

	step := newStep(stepID, StepCreateSession, nil)
	step.CreateSession = &CreateSessionStep{
		SessionID:            s.ID,
		Name:                 cloneBytes(s.Name),
		Directory:            cloneBytes(s.Directory),
		Group:                roles.groupOf[s.ID],
		GroupName:            cloneBytes(roles.nameOf[roles.groupOf[s.ID]]),
		GroupLeaderSessionID: "",
		InitialWindowID:      wInit.ID,
		InitialWindowIndex:   wIdx,
		InitialPaneID:        pInitID,
		Width:                wInit.Width,
		Height:               wInit.Height,
		BootstrapWindow:      false,
		InitialWindowName:    cloneBytes(wInit.Name),
		Options:              cloneOptions(s.Options),
		Environment:          cloneVariables(s.Environment),
	}
	steps = append(steps, step)
	reg.sessionStepID[s.ID] = stepID
	placement := placeInitialWindow(s.ID, wInit.ID, wIdx, reg)
	steps = append(steps, placement)
	reg.windowStepID[wInit.ID] = stepID
	reg.paneStepID[pInitID] = stepID
	reg.handledLinks[linkSlotKey{session: s.ID, window: wInit.ID, index: wIdx}] = true

	previousSplit := placement.ID

	for _, split := range splits {
		pane := idx.panesByID[split.newPaneID]
		splitID := "split-pane:" + split.newPaneID
		targetStep := reg.paneStepID[split.targetPaneID]

		spStep := newStep(splitID, StepSplitPane, []string{targetStep, previousSplit})
		spStep.SplitPane = &SplitPaneStep{
			PaneID:        split.newPaneID,
			WindowID:      wInit.ID,
			TargetPaneID:  split.targetPaneID,
			Axis:          split.axis,
			Width:         pane.Width,
			Height:        pane.Height,
			SubtreeWidth:  split.subtreeWidth,
			SubtreeHeight: split.subtreeHeight,
			Directory:     cloneBytes(pane.Directory),
			Title:         cloneBytes(pane.Title),
			Options:       cloneOptions(pane.Options),
			Launch:        cloneLaunch(pane.Launch),
			Terminal:      cloneTerminal(pane.Terminal),
		}
		steps = append(steps, spStep)
		reg.paneStepID[split.newPaneID] = splitID
		previousSplit = splitID
	}

	return steps
}

func planWindowCreation(
	s model.Session,
	w model.Window,
	own windowOwnership,
	pInitID string,
	splits []splitPlan,
	idx entityIndexes,
	reg *stepRegistry,
) []Step {
	var (
		lowestIdx int
		found     bool
	)

	for _, l := range idx.linksBySession[s.ID] {
		if l.Window == w.ID {
			if !found || l.Index < lowestIdx {
				lowestIdx = l.Index
				found = true
			}
		}
	}

	winStepID := "create-window:" + w.ID
	steps := make([]Step, 0, 1+len(splits))

	cwStep := newStep(winStepID, StepCreateWindow, []string{reg.sessionStepID[s.ID], reg.sessionReadyStepID[s.ID]})
	cwStep.CreateWindow = &CreateWindowStep{
		WindowID:       w.ID,
		OwnerSessionID: s.ID,
		OwnerGroupID:   own.ownerGroup[w.ID],
		Index:          lowestIdx,
		Name:           cloneBytes(w.Name),
		InitialPaneID:  pInitID,
		Width:          w.Width,
		Height:         w.Height,
		Options:        cloneOptions(w.Options),
	}
	steps = append(steps, cwStep)

	reg.windowStepID[w.ID] = winStepID
	reg.paneStepID[pInitID] = winStepID
	reg.handledLinks[linkSlotKey{session: s.ID, window: w.ID, index: lowestIdx}] = true

	previousSplit := winStepID

	for _, split := range splits {
		pane := idx.panesByID[split.newPaneID]
		splitID := "split-pane:" + split.newPaneID
		targetStep := reg.paneStepID[split.targetPaneID]

		spStep := newStep(splitID, StepSplitPane, []string{targetStep, previousSplit})
		spStep.SplitPane = &SplitPaneStep{
			PaneID:        split.newPaneID,
			WindowID:      w.ID,
			TargetPaneID:  split.targetPaneID,
			Axis:          split.axis,
			Width:         pane.Width,
			Height:        pane.Height,
			SubtreeWidth:  split.subtreeWidth,
			SubtreeHeight: split.subtreeHeight,
			Directory:     cloneBytes(pane.Directory),
			Title:         cloneBytes(pane.Title),
			Options:       cloneOptions(pane.Options),
			Launch:        cloneLaunch(pane.Launch),
			Terminal:      cloneTerminal(pane.Terminal),
		}
		steps = append(steps, spStep)
		reg.paneStepID[split.newPaneID] = splitID
		previousSplit = splitID
	}

	return steps
}

func planNonFollowerSessions(
	g model.Manifest,
	roles groupRoles,
	own windowOwnership,
	idx entityIndexes,
	initPanes map[string]string,
	splitsOf map[string][]splitPlan,
	reg *stepRegistry,
) []Step {
	steps := make([]Step, 0, len(g.Sessions))

	ownedWindowsBySession := make(map[string][]model.Window, len(g.Sessions))
	for _, w := range g.Windows {
		owner := own.ownerSession[w.ID]
		if owner != "" {
			ownedWindowsBySession[owner] = append(ownedWindowsBySession[owner], w)
		}
	}

	for _, s := range g.Sessions {
		if roles.isFollower[s.ID] {
			continue
		}

		wInitID := own.initialWindowOf[s.ID]
		if wInitID != "" {
			wInit := idx.windowsByID[wInitID]
			pInitID := initPanes[wInitID]
			wIdx := own.initialWindowIndex[s.ID]
			steps = append(steps, planNonFollowerInitialWindow(
				s, wInit, wIdx, pInitID, splitsOf[wInitID], idx, roles, reg,
			)...)
		} else {
			stepID := "create-session:" + s.ID
			csStep := newStep(stepID, StepCreateSession, nil)
			csStep.CreateSession = &CreateSessionStep{
				SessionID:            s.ID,
				Name:                 cloneBytes(s.Name),
				Directory:            cloneBytes(s.Directory),
				Group:                roles.groupOf[s.ID],
				GroupName:            cloneBytes(roles.nameOf[roles.groupOf[s.ID]]),
				GroupLeaderSessionID: "",
				InitialWindowID:      "",
				InitialWindowName:    nil,
				InitialWindowIndex:   0,
				InitialPaneID:        "",
				Width:                0,
				Height:               0,
				BootstrapWindow:      true,
				Options:              cloneOptions(s.Options),
				Environment:          cloneVariables(s.Environment),
			}
			steps = append(steps, csStep)
			reg.sessionStepID[s.ID] = stepID
			steps = append(steps, placeInitialWindow(s.ID, "", freeBootstrapIndex(idx.linksBySession[s.ID]), reg))
		}

		var createdStepIDs []string

		for _, w := range ownedWindowsBySession[s.ID] {
			if w.ID == wInitID {
				continue
			}

			wSteps := planWindowCreation(s, w, own, initPanes[w.ID], splitsOf[w.ID], idx, reg)

			steps = append(steps, wSteps...)
			for _, st := range wSteps {
				createdStepIDs = append(createdStepIDs, st.ID)
			}
		}

		if wInitID == "" && len(createdStepIDs) > 0 {
			closeStepID := "close-bootstrap:" + s.ID

			slices.Sort(createdStepIDs)
			createdStepIDs = slices.Compact(createdStepIDs)

			cbStep := newStep(closeStepID, StepCloseBootstrapWindow, createdStepIDs)
			cbStep.CloseBootstrapWindow = &CloseBootstrapWindowStep{
				SessionID: s.ID,
			}
			steps = append(steps, cbStep)
			reg.closeBootstrapStepID[s.ID] = closeStepID
		}
	}

	return steps
}

func followerDependencies(leader string, idx entityIndexes, reg *stepRegistry) []string {
	deps := []string{reg.sessionStepID[leader], reg.sessionReadyStepID[leader]}
	if cb := reg.closeBootstrapStepID[leader]; cb != "" {
		deps = append(deps, cb)
	}

	for _, l := range idx.linksBySession[leader] {
		deps = append(deps, reg.windowStepID[l.Window])
		for _, p := range idx.panesByWindow[l.Window] {
			if reg.paneStepID[p.ID] != reg.windowStepID[l.Window] {
				deps = append(deps, reg.paneStepID[p.ID])
			}
		}
	}

	slices.Sort(deps)

	return slices.Compact(deps)
}

func planGroupFollowers(
	g model.Manifest,
	roles groupRoles,
	idx entityIndexes,
	reg *stepRegistry,
) []Step {
	var steps []Step

	for _, grp := range g.Groups {
		leader := roles.leaderOf[grp.ID]
		deps := followerDependencies(leader, idx, reg)

		for _, follower := range grp.Members[1:] {
			stepID := "create-session:" + follower
			folStep := newStep(stepID, StepCreateSession, deps)
			folStep.CreateSession = &CreateSessionStep{
				SessionID:            follower,
				Name:                 cloneBytes(idx.sessionsByID[follower].Name),
				Directory:            cloneBytes(idx.sessionsByID[follower].Directory),
				Group:                grp.ID,
				GroupName:            cloneBytes(roles.nameOf[grp.ID]),
				GroupLeaderSessionID: leader,
				InitialWindowID:      "",
				InitialWindowName:    nil,
				InitialWindowIndex:   0,
				InitialPaneID:        "",
				Width:                0,
				Height:               0,
				BootstrapWindow:      false,
				Options:              cloneOptions(idx.sessionsByID[follower].Options),
				Environment:          cloneVariables(idx.sessionsByID[follower].Environment),
			}
			steps = append(steps, folStep)

			reg.sessionStepID[follower] = stepID

			reg.sessionReadyStepID[follower] = stepID
			for _, l := range idx.linksBySession[follower] {
				reg.handledLinks[linkSlotKey{session: follower, window: l.Window, index: l.Index}] = true
			}
		}
	}

	return steps
}

func planSharedLinks(
	g model.Manifest,
	own windowOwnership,
	roles groupRoles,
	idx entityIndexes,
	reg *stepRegistry,
) []Step {
	var steps []Step

	for _, s := range g.Sessions {
		for _, l := range idx.linksBySession[s.ID] {
			key := linkSlotKey{session: s.ID, window: l.Window, index: l.Index}
			if reg.handledLinks[key] {
				continue
			}

			linkStepID := fmt.Sprintf("link-window:%s:%s:%d", s.ID, l.Window, l.Index)

			deps := []string{reg.sessionStepID[s.ID], reg.sessionReadyStepID[s.ID], reg.windowStepID[l.Window]}
			for _, p := range idx.panesByWindow[l.Window] {
				if reg.paneStepID[p.ID] != reg.windowStepID[l.Window] {
					deps = append(deps, reg.paneStepID[p.ID])
				}
			}

			slices.Sort(deps)
			deps = slices.Compact(deps)

			lwStep := newStep(linkStepID, StepLinkWindow, deps)
			lwStep.LinkWindow = &LinkWindowStep{
				SessionID: s.ID,
				WindowID:  l.Window,
				Index:     l.Index,
				Flags:     cloneBytes(l.Flags),
				Active:    l.Active,
				Last:      l.Last,
			}
			steps = append(steps, lwStep)

			reg.linkStepIDs[s.ID] = append(reg.linkStepIDs[s.ID], linkStepID)
			reg.handledLinks[key] = true
		}
	}

	for _, s := range g.Sessions {
		if reg.closeBootstrapStepID[s.ID] == "" && own.initialWindowOf[s.ID] == "" && !roles.isFollower[s.ID] {
			stepID := "close-bootstrap:" + s.ID
			deps := slices.Clone(reg.linkStepIDs[s.ID])
			slices.Sort(deps)
			deps = slices.Compact(deps)

			cbStep := newStep(stepID, StepCloseBootstrapWindow, deps)
			cbStep.CloseBootstrapWindow = &CloseBootstrapWindowStep{
				SessionID: s.ID,
			}
			steps = append(steps, cbStep)
			reg.closeBootstrapStepID[s.ID] = stepID
		}
	}

	return steps
}

func planWindowFinalization(
	g model.Manifest,
	idx entityIndexes,
	reg *stepRegistry,
) []Step {
	var steps []Step

	for _, w := range g.Windows {
		layoutStepID := "apply-layout:" + w.ID

		deps := []string{reg.windowStepID[w.ID]}
		for _, p := range idx.panesByWindow[w.ID] {
			if reg.paneStepID[p.ID] != reg.windowStepID[w.ID] {
				deps = append(deps, reg.paneStepID[p.ID])
			}
		}

		slices.Sort(deps)
		deps = slices.Compact(deps)

		alStep := newStep(layoutStepID, StepApplyLayout, deps)
		alStep.ApplyLayout = &ApplyLayoutStep{
			WindowID: w.ID,
			Layout:   cloneLayout(w.Layout),
			Width:    w.Width,
			Height:   w.Height,
		}
		steps = append(steps, alStep)
		currentWinStep := layoutStepID

		pInitID := firstLeaf(w.Layout)
		if pInitID != "" {
			titleStepID := fmt.Sprintf("set-pane-title:%s:%s", w.ID, pInitID)
			titleDeps := []string{currentWinStep, reg.paneStepID[pInitID]}
			slices.Sort(titleDeps)
			titleDeps = slices.Compact(titleDeps)

			sptStep := newStep(titleStepID, StepSetPaneTitle, titleDeps)
			sptStep.SetPaneTitle = &SetPaneTitleStep{
				WindowID: w.ID,
				PaneID:   pInitID,
				Title:    cloneBytes(idx.panesByID[pInitID].Title),
			}
			steps = append(steps, sptStep)
			currentWinStep = titleStepID
		}

		var activePaneID string

		for _, p := range idx.panesByWindow[w.ID] {
			if p.Active {
				activePaneID = p.ID
				break
			}
		}

		if activePaneID != "" {
			paneStepID := fmt.Sprintf("select-pane:%s:%s", w.ID, activePaneID)
			spStep := newStep(paneStepID, StepSelectPane, []string{currentWinStep})
			spStep.SelectPane = &SelectPaneStep{
				WindowID: w.ID,
				PaneID:   activePaneID,
			}
			steps = append(steps, spStep)
			currentWinStep = paneStepID
		}

		if w.Zoomed && activePaneID != "" {
			zoomStepID := fmt.Sprintf("apply-zoom:%s:%s", w.ID, activePaneID)
			azStep := newStep(zoomStepID, StepApplyZoom, []string{currentWinStep})
			azStep.ApplyZoom = &ApplyZoomStep{
				WindowID: w.ID,
				PaneID:   activePaneID,
			}
			steps = append(steps, azStep)
			currentWinStep = zoomStepID
		}

		reg.windowFinalStepID[w.ID] = currentWinStep
	}

	return steps
}

func sessionNavigationDependencies(s model.Session, roles groupRoles, idx entityIndexes, reg *stepRegistry) []string {
	deps := []string{reg.sessionStepID[s.ID], reg.sessionReadyStepID[s.ID]}
	for _, l := range idx.linksBySession[s.ID] {
		deps = append(deps, reg.windowFinalStepID[l.Window])
	}

	deps = append(deps, reg.linkStepIDs[s.ID]...)
	if cb := reg.closeBootstrapStepID[s.ID]; cb != "" {
		deps = append(deps, cb)
	}

	if roles.isFollower[s.ID] {
		grpID := roles.groupOf[s.ID]

		leader := roles.leaderOf[grpID]

		deps = append(deps, reg.linkStepIDs[leader]...)
		if cb := reg.closeBootstrapStepID[leader]; cb != "" {
			deps = append(deps, cb)
		}
	}

	slices.Sort(deps)

	return slices.Compact(deps)
}

func planSessionFinalization(
	g model.Manifest,
	roles groupRoles,
	idx entityIndexes,
	reg *stepRegistry,
) []Step {
	steps := make([]Step, 0, len(g.Sessions))

	for _, s := range g.Sessions {
		var activeLink, lastLink *model.Link

		for i := range idx.linksBySession[s.ID] {
			l := &idx.linksBySession[s.ID][i]
			if l.Active {
				activeLink = l
			}

			if l.Last {
				lastLink = l
			}
		}

		stepID := "select-window:" + s.ID

		deps := sessionNavigationDependencies(s, roles, idx, reg)
		lastWinID := ""
		lastIdx := -1

		if lastLink != nil {
			lastWinID = lastLink.Window
			lastIdx = lastLink.Index
		}

		activeWinID := ""
		activeIdx := 0

		if activeLink != nil {
			activeWinID = activeLink.Window
			activeIdx = activeLink.Index
		}

		swStep := newStep(stepID, StepSelectWindow, deps)
		swStep.SelectWindow = &SelectWindowStep{
			SessionID:      s.ID,
			ActiveWindowID: activeWinID,
			ActiveIndex:    activeIdx,
			LastWindowID:   lastWinID,
			LastIndex:      lastIdx,
		}
		steps = append(steps, swStep)
	}

	return steps
}

func generateSteps(g model.Manifest) ([]Step, PlanSummary) {
	idx := buildEntityIndexes(g)
	roles := buildGroupRoles(g)
	own := assignWindowOwnership(g, roles, idx)

	initPanes := make(map[string]string, len(g.Windows))
	splitsOf := make(map[string][]splitPlan, len(g.Windows))

	for _, w := range g.Windows {
		initPane, splits := decomposeLayout(w.Layout)
		initPanes[w.ID] = initPane
		splitsOf[w.ID] = splits
	}

	reg := newStepRegistry(len(g.Sessions), len(g.Windows), len(g.Panes), len(g.Links))

	steps := make([]Step, 0, len(g.Sessions)+len(g.Windows)+len(g.Panes))
	steps = append(steps, planNonFollowerSessions(g, roles, own, idx, initPanes, splitsOf, reg)...)
	steps = append(steps, planGroupFollowers(g, roles, idx, reg)...)
	steps = append(steps, planSharedLinks(g, own, roles, idx, reg)...)
	steps = append(steps, planWindowFinalization(g, idx, reg)...)
	steps = append(steps, planSessionFinalization(g, roles, idx, reg)...)

	sharedWindows := 0
	sharedPanes := 0

	for _, w := range g.Windows {
		if len(idx.windowLinks[w.ID]) > 1 {
			sharedWindows++
			sharedPanes += len(idx.panesByWindow[w.ID])
		}
	}

	summary := PlanSummary{
		SessionCount:      len(g.Sessions),
		GroupCount:        len(g.Groups),
		WindowCount:       len(g.Windows),
		PaneCount:         len(g.Panes),
		LinkCount:         len(g.Links),
		StepCount:         len(steps),
		SharedWindowCount: sharedWindows,
		SharedPaneCount:   sharedPanes,
	}

	return steps, summary
}
