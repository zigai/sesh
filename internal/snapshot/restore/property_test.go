package restore

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"pgregory.net/rapid"

	"github.com/zigai/sesh/internal/snapshot/model"
)

const (
	statusVerified   = "verified"
	statusUnverified = "unverified"
)

func TestPlanProperties(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		m := drawRandomManifest(rt)
		if err := m.Validate(); err != nil {
			rt.Fatalf("generated manifest is invalid: %v", err)
		}

		sessionIDs := make([]string, len(m.Sessions))
		for i, s := range m.Sessions {
			sessionIDs[i] = s.ID
		}

		subsetSize := rapid.IntRange(1, len(sessionIDs)).Draw(rt, "subsetSize")
		subset := rapid.SliceOfN(rapid.SampledFrom(sessionIDs), subsetSize, subsetSize).Draw(rt, "subset")

		withDups := append([]string(nil), subset...)

		extraDups := rapid.IntRange(0, 3).Draw(rt, "extraDups")
		for range extraDups {
			withDups = append(withDups, rapid.SampledFrom(subset).Draw(rt, "dup"))
		}

		plan, err := BuildPlan(m, withDups)
		if err != nil {
			rt.Fatalf("BuildPlan failed on valid manifest: %v", err)
		}

		if err := ValidatePlan(plan); err != nil {
			rt.Fatalf("ValidatePlan failed on generated plan: %v", err)
		}

		assertPlanRequestedAndClosure(rt, plan, subset)
		assertPlanCreations(rt, plan)
		assertPlanSummary(rt, plan)
		assertPlanTitlesAndNames(rt, plan)
		assertPlanEquivalence(rt, m, plan, withDups)
	})
}

func assertPlanRequestedAndClosure(rt *rapid.T, plan Plan, subset []string) {
	expectedRequested := deduplicateSort(subset)
	if diff := cmp.Diff(expectedRequested, plan.RequestedSessions); diff != "" {
		rt.Fatalf("unexpected requested sessions diff: %s", diff)
	}

	expectedClosure := make([]string, 0, len(plan.RequestedSessions)+len(plan.AddedSessions))
	expectedClosure = append(expectedClosure, plan.RequestedSessions...)
	expectedClosure = append(expectedClosure, plan.AddedSessions...)
	slices.Sort(expectedClosure)

	if diff := cmp.Diff(expectedClosure, plan.ClosureSessions); diff != "" {
		rt.Fatalf("unexpected closure sessions diff: %s", diff)
	}

	sourceIDs := make([]string, len(plan.SourceGraph.Sessions))
	for i, s := range plan.SourceGraph.Sessions {
		sourceIDs[i] = s.ID
	}

	slices.Sort(sourceIDs)

	if diff := cmp.Diff(plan.ClosureSessions, sourceIDs); diff != "" {
		rt.Fatalf("closure sessions do not match source graph sessions: %s", diff)
	}
}

func countStepCreations(steps []Step) (map[string]int, map[string]int) {
	windowCreateCount := make(map[string]int)
	paneCreateCount := make(map[string]int)

	for _, step := range steps {
		switch step.Kind {
		case StepCreateSession:
			if step.CreateSession.InitialWindowID != "" {
				windowCreateCount[step.CreateSession.InitialWindowID]++
			}

			if step.CreateSession.InitialPaneID != "" {
				paneCreateCount[step.CreateSession.InitialPaneID]++
			}
		case StepCreateWindow:
			windowCreateCount[step.CreateWindow.WindowID]++
			paneCreateCount[step.CreateWindow.InitialPaneID]++
		case StepSplitPane:
			paneCreateCount[step.SplitPane.PaneID]++
		case StepPlaceInitialWindow, StepLinkWindow, StepCloseBootstrapWindow, StepApplyLayout, StepSetPaneTitle, StepSelectPane, StepApplyZoom, StepSelectWindow:
		}
	}

	return windowCreateCount, paneCreateCount
}

func assertPlanCreations(rt *rapid.T, plan Plan) {
	windowCreateCount, paneCreateCount := countStepCreations(plan.Steps)
	for _, w := range plan.SourceGraph.Windows {
		if count := windowCreateCount[w.ID]; count != 1 {
			rt.Fatalf("window %s creation count = %d, want 1", w.ID, count)
		}
	}

	for _, p := range plan.SourceGraph.Panes {
		if count := paneCreateCount[p.ID]; count != 1 {
			rt.Fatalf("pane %s creation count = %d, want 1", p.ID, count)
		}
	}

	if len(windowCreateCount) != len(plan.SourceGraph.Windows) {
		rt.Fatalf("total window creates = %d, want %d", len(windowCreateCount), len(plan.SourceGraph.Windows))
	}

	if len(paneCreateCount) != len(plan.SourceGraph.Panes) {
		rt.Fatalf("total pane creates = %d, want %d", len(paneCreateCount), len(plan.SourceGraph.Panes))
	}
}

func assertPlanSummary(rt *rapid.T, plan Plan) {
	if plan.Summary.SessionCount != len(plan.SourceGraph.Sessions) {
		rt.Fatalf("summary SessionCount = %d, want %d", plan.Summary.SessionCount, len(plan.SourceGraph.Sessions))
	}

	if plan.Summary.WindowCount != len(plan.SourceGraph.Windows) {
		rt.Fatalf("summary WindowCount = %d, want %d", plan.Summary.WindowCount, len(plan.SourceGraph.Windows))
	}

	if plan.Summary.PaneCount != len(plan.SourceGraph.Panes) {
		rt.Fatalf("summary PaneCount = %d, want %d", plan.Summary.PaneCount, len(plan.SourceGraph.Panes))
	}

	if plan.Summary.StepCount != len(plan.Steps) {
		rt.Fatalf("summary StepCount = %d, want %d", plan.Summary.StepCount, len(plan.Steps))
	}
}

func assertCreateSessionTitle(rt *rapid.T, cs *CreateSessionStep, windowsByID map[string]model.Window) {
	if cs.InitialWindowID != "" {
		w, exists := windowsByID[cs.InitialWindowID]
		if !exists {
			rt.Fatalf("initial window %s not found in source graph", cs.InitialWindowID)
		}

		if !bytes.Equal(cs.InitialWindowName, w.Name) {
			rt.Fatalf("initial window name mismatch: got %q, want %q", cs.InitialWindowName, w.Name)
		}
	} else if len(cs.InitialWindowName) != 0 {
		rt.Fatalf("unexpected initial window name on session %s without initial window", cs.SessionID)
	}
}

func assertSetPaneTitlePayload(rt *rapid.T, spt *SetPaneTitleStep, panesByID map[string]model.Pane) {
	p, exists := panesByID[spt.PaneID]
	if !exists {
		rt.Fatalf("pane %s not found in source graph", spt.PaneID)
	}

	if !bytes.Equal(spt.Title, p.Title) {
		rt.Fatalf("pane title mismatch: got %q, want %q", spt.Title, p.Title)
	}
}

func assertSplitPaneTitlePayload(rt *rapid.T, sp *SplitPaneStep, panesByID map[string]model.Pane) {
	p, exists := panesByID[sp.PaneID]
	if !exists {
		rt.Fatalf("pane %s not found in source graph", sp.PaneID)
	}

	if !bytes.Equal(sp.Title, p.Title) {
		rt.Fatalf("split pane title mismatch: got %q, want %q", sp.Title, p.Title)
	}
}

func assertPlanTitlesAndNames(rt *rapid.T, plan Plan) {
	windowsByID := make(map[string]model.Window, len(plan.SourceGraph.Windows))
	for _, w := range plan.SourceGraph.Windows {
		windowsByID[w.ID] = w
	}

	panesByID := make(map[string]model.Pane, len(plan.SourceGraph.Panes))
	for _, p := range plan.SourceGraph.Panes {
		panesByID[p.ID] = p
	}

	for _, step := range plan.Steps {
		switch step.Kind {
		case StepCreateSession:
			assertCreateSessionTitle(rt, step.CreateSession, windowsByID)
		case StepSetPaneTitle:
			assertSetPaneTitlePayload(rt, step.SetPaneTitle, panesByID)
		case StepSplitPane:
			assertSplitPaneTitlePayload(rt, step.SplitPane, panesByID)
		case StepPlaceInitialWindow, StepCreateWindow, StepLinkWindow, StepCloseBootstrapWindow, StepApplyLayout, StepSelectPane, StepApplyZoom, StepSelectWindow:
		}
	}
}

func assertPlanEquivalence(rt *rapid.T, m model.Manifest, plan Plan, withDups []string) {
	shuffled := append([]string(nil), withDups...)
	slices.Reverse(shuffled)

	shuffledPlan, err := BuildPlan(m, shuffled)
	if err != nil {
		rt.Fatalf("BuildPlan failed on shuffled input: %v", err)
	}

	if diff := cmp.Diff(plan, shuffledPlan); diff != "" {
		rt.Fatalf("plan differed under permutation/duplicates: %s", diff)
	}
}

func TestPlanRejectsRandomMalformedSelection(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		m := drawRandomManifest(rt)

		badID := rapid.SampledFrom([]string{
			"",
			"unknown-s999",
			"has space",
			"has:colon",
			"has.dot",
			"-leading-dash",
			"ALL_CAPS_WITH_UNDERSCORES",
		}).Draw(rt, "badID")

		_, err := BuildPlan(m, []string{badID})
		if err == nil {
			rt.Fatalf("expected error for bad session ID %q, got nil", badID)
		}

		if !errors.Is(err, ErrInvalid) {
			rt.Fatalf("expected ErrInvalid for bad session ID %q, got: %v", badID, err)
		}
	})
}

func deduplicateSort(items []string) []string {
	seen := make(map[string]bool, len(items))
	result := make([]string, 0, len(items))

	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}

	slices.Sort(result)

	return result
}

func drawRandomManifest(rt *rapid.T) model.Manifest {
	sessionCount := rapid.IntRange(1, 4).Draw(rt, "sessionCount")
	windowCount := rapid.IntRange(1, 3).Draw(rt, "windowCount")

	m := model.Manifest{
		Format:   "sesh",
		Version:  1,
		Started:  1000,
		Finished: 2000,
		Source: model.Source{
			TmuxVersion:    "3.6",
			LibraryVersion: "v0.3.0",
			Consistency:    "checkpoint",
		},
		Evidence: []model.Evidence{
			{Domain: "S01", Status: statusVerified, Reason: ""},
			{Domain: "S02", Status: statusVerified, Reason: ""},
			{Domain: "S03", Status: statusVerified, Reason: ""},
			{Domain: "S04", Status: statusVerified, Reason: ""},
			{Domain: "S05", Status: statusVerified, Reason: ""},
			{Domain: "S06", Status: statusVerified, Reason: ""},
			{Domain: "S07", Status: statusVerified, Reason: ""},
			{Domain: "S08", Status: statusVerified, Reason: ""},
			{Domain: "S09", Status: statusVerified, Reason: ""},
			{Domain: "S10", Status: statusVerified, Reason: ""},
		},
	}

	for wIdx := range windowCount {
		wID := fmt.Sprintf("w%d", wIdx)
		window, panes := drawRandomWindow(rt, wID, wIdx)
		m.Windows = append(m.Windows, window)
		m.Panes = append(m.Panes, panes...)
	}

	links := drawRandomLinks(rt, sessionCount, windowCount)
	sessions, groups := drawRandomSessions(rt, links)
	m.Sessions = sessions
	m.Groups = groups

	for sIdx := range sessionCount {
		m.Links = append(m.Links, links[sIdx]...)
	}

	return m
}

func drawRandomWindow(rt *rapid.T, windowID string, wIdx int) (model.Window, []model.Pane) {
	width := 80
	height := 24
	paneCount := rapid.IntRange(1, 3).Draw(rt, "paneCount")
	zoomed := rapid.Bool().Draw(rt, "zoomed")

	leaves := make([]model.Layout, paneCount)
	panes := make([]model.Pane, paneCount)

	var layout model.Layout

	switch paneCount {
	case 1:
		pID := fmt.Sprintf("p%d-0", wIdx)
		leaves[0] = model.Layout{Width: width, Height: height, Left: 0, Top: 0, Pane: pID}
		layout = leaves[0]
		panes[0] = model.Pane{
			ID:        pID,
			Window:    windowID,
			Index:     0,
			Title:     model.Bytes("shell"),
			Directory: model.Bytes("/tmp"),
			Width:     width,
			Height:    height,
			Left:      0,
			Top:       0,
			Active:    true,
			Launch:    model.Launch{Recovery: statusUnverified},
		}
	case 2:
		p0ID := fmt.Sprintf("p%d-0", wIdx)
		p1ID := fmt.Sprintf("p%d-1", wIdx)
		leaves[0] = model.Layout{Width: 40, Height: height, Left: 0, Top: 0, Pane: p0ID}
		leaves[1] = model.Layout{Width: 39, Height: height, Left: 41, Top: 0, Pane: p1ID}
		layout = model.Layout{
			Width:    width,
			Height:   height,
			Axis:     "x",
			Children: leaves,
		}
		panes[0] = model.Pane{
			ID:        p0ID,
			Window:    windowID,
			Index:     0,
			Title:     model.Bytes("left"),
			Directory: model.Bytes("/tmp"),
			Width:     40,
			Height:    height,
			Left:      0,
			Top:       0,
			Active:    true,
			Launch:    model.Launch{Recovery: statusUnverified},
		}
		panes[1] = model.Pane{
			ID:        p1ID,
			Window:    windowID,
			Index:     1,
			Title:     model.Bytes("right"),
			Directory: model.Bytes("/tmp"),
			Width:     39,
			Height:    height,
			Left:      41,
			Top:       0,
			Active:    false,
			Launch:    model.Launch{Recovery: statusUnverified},
		}

		if zoomed {
			panes[0].Width = width
		}
	default:
		p0ID := fmt.Sprintf("p%d-0", wIdx)
		p1ID := fmt.Sprintf("p%d-1", wIdx)
		p2ID := fmt.Sprintf("p%d-2", wIdx)
		leaves[0] = model.Layout{Width: 40, Height: height, Left: 0, Top: 0, Pane: p0ID}
		leaves[1] = model.Layout{Width: 39, Height: 12, Left: 41, Top: 0, Pane: p1ID}
		leaves[2] = model.Layout{Width: 39, Height: 11, Left: 41, Top: 13, Pane: p2ID}

		layout = model.Layout{
			Width:  width,
			Height: height,
			Axis:   "x",
			Children: []model.Layout{
				leaves[0],
				{
					Width:    39,
					Height:   height,
					Left:     41,
					Top:      0,
					Axis:     "y",
					Children: []model.Layout{leaves[1], leaves[2]},
				},
			},
		}

		panes[0] = model.Pane{
			ID:        p0ID,
			Window:    windowID,
			Index:     0,
			Title:     model.Bytes("left"),
			Directory: model.Bytes("/tmp"),
			Width:     40,
			Height:    height,
			Left:      0,
			Top:       0,
			Active:    true,
			Launch:    model.Launch{Recovery: statusUnverified},
		}
		panes[1] = model.Pane{
			ID:        p1ID,
			Window:    windowID,
			Index:     1,
			Title:     model.Bytes("top-right"),
			Directory: model.Bytes("/tmp"),
			Width:     39,
			Height:    12,
			Left:      41,
			Top:       0,
			Active:    false,
			Launch:    model.Launch{Recovery: statusUnverified},
		}
		panes[2] = model.Pane{
			ID:        p2ID,
			Window:    windowID,
			Index:     2,
			Title:     model.Bytes("bottom-right"),
			Directory: model.Bytes("/tmp"),
			Width:     39,
			Height:    11,
			Left:      41,
			Top:       13,
			Active:    false,
			Launch:    model.Launch{Recovery: statusUnverified},
		}

		if zoomed {
			panes[0].Width = width
		}
	}

	window := model.Window{
		ID:     windowID,
		Name:   model.Bytes(fmt.Sprintf("win-%d", wIdx)),
		Width:  width,
		Height: height,
		Layout: layout,
		Zoomed: zoomed,
	}

	return window, panes
}

func drawRandomLinks(rt *rapid.T, sessionCount, windowCount int) [][]model.Link {
	links := make([][]model.Link, sessionCount)

	for wIdx := range windowCount {
		sIdx := wIdx % sessionCount
		wID := fmt.Sprintf("w%d", wIdx)
		sID := fmt.Sprintf("s%d", sIdx)
		links[sIdx] = append(links[sIdx], model.Link{
			Session: sID,
			Window:  wID,
			Index:   len(links[sIdx]),
			Active:  len(links[sIdx]) == 0,
			Last:    false,
		})
	}

	for sIdx := range sessionCount {
		sID := fmt.Sprintf("s%d", sIdx)
		if len(links[sIdx]) == 0 {
			wIdx := rapid.IntRange(0, windowCount-1).Draw(rt, fmt.Sprintf("winFor_%d", sIdx))
			wID := fmt.Sprintf("w%d", wIdx)
			links[sIdx] = append(links[sIdx], model.Link{
				Session: sID,
				Window:  wID,
				Index:   0,
				Active:  true,
				Last:    false,
			})
		}
	}

	for sIdx := range sessionCount {
		for i := range links[sIdx] {
			links[sIdx][i].Active = (i == 0)
			links[sIdx][i].Last = false
		}

		if len(links[sIdx]) > 1 && rapid.Bool().Draw(rt, fmt.Sprintf("hasLast_%d", sIdx)) {
			links[sIdx][1].Last = true
		}
	}

	return links
}

func drawRandomSessions(rt *rapid.T, links [][]model.Link) ([]model.Session, []model.Group) {
	sessionCount := len(links)
	sessions := make([]model.Session, sessionCount)

	var groups []model.Group

	clusters := make(map[string][]int)

	for sIdx, sLinks := range links {
		key := linkSignature(sLinks)
		clusters[key] = append(clusters[key], sIdx)
	}

	assignedGroup := make(map[int]string)

	for _, cluster := range clusters {
		if len(cluster) > 1 && rapid.Bool().Draw(rt, "createGroup") {
			gID := fmt.Sprintf("g%d", len(groups))

			memberIDs := make([]string, len(cluster))
			for i, sIdx := range cluster {
				memberIDs[i] = fmt.Sprintf("s%d", sIdx)
				assignedGroup[sIdx] = gID
			}

			groups = append(groups, model.Group{
				ID:      gID,
				Name:    model.Bytes("grp-" + gID),
				Members: memberIDs,
			})
		}
	}

	for sIdx := range sessionCount {
		sID := fmt.Sprintf("s%d", sIdx)
		sessions[sIdx] = model.Session{
			ID:        sID,
			Name:      model.Bytes(fmt.Sprintf("sess-%d", sIdx)),
			Directory: model.Bytes(fmt.Sprintf("/srv/%d", sIdx)),
			Group:     assignedGroup[sIdx],
		}
	}

	return sessions, groups
}

func linkSignature(links []model.Link) string {
	windowSlots := make(map[string]int, len(links))
	for _, l := range links {
		windowSlots[l.Window] = l.Index
	}

	keys := make([]string, 0, len(windowSlots))
	for k := range windowSlots {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	var b []byte
	for _, k := range keys {
		b = fmt.Appendf(b, "%s:%d;", k, windowSlots[k])
	}

	return string(b)
}
