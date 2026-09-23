package restore

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func baseFixture() model.Manifest {
	return model.Manifest{
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
		Sessions: []model.Session{
			{
				ID:        "s0",
				Name:      model.Bytes("main"),
				Directory: model.Bytes("/home/user"),
			},
		},
		Windows: []model.Window{
			{
				ID:     "w0",
				Name:   model.Bytes("code"),
				Width:  80,
				Height: 24,
				Layout: model.Layout{
					Width:  80,
					Height: 24,
					Left:   0,
					Top:    0,
					Pane:   "p0",
				},
				Zoomed: false,
			},
		},
		Links: []model.Link{
			{
				Session: "s0",
				Window:  "w0",
				Index:   0,
				Active:  true,
				Last:    false,
			},
		},
		Panes: []model.Pane{
			{
				ID:        "p0",
				Window:    "w0",
				Index:     0,
				Title:     model.Bytes("shell"),
				Directory: model.Bytes("/home/user"),
				Width:     80,
				Height:    24,
				Left:      0,
				Top:       0,
				Active:    true,
				Dead:      false,
				Launch: model.Launch{
					Recovery: statusUnverified,
				},
			},
		},
	}
}

func TestPracticalRecoveryReadinessRejectsUnknownRequirement(t *testing.T) {
	m := baseFixture()

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	if !plan.PracticalRecoveryReady() {
		t.Fatalf("known fidelity limits blocked practical recovery: %+v", plan.MissingRequirements)
	}

	classification := ClassifyPracticalRequirements(append(plan.MissingRequirements, MissingRequirement{
		Domain: "launch", Status: "unknown", Reason: "future_effect_unknown",
	}))
	if len(classification.Blockers) != 1 || classification.Blockers[0].Reason != "future_effect_unknown" {
		t.Fatalf("classification = %+v, want one fail-closed blocker", classification)
	}
}

func TestPracticalRecoveryClassifiesNonrestartablePaneFidelityLimits(t *testing.T) {
	nonrestartableReasons := []string{
		"dead_pane",
		"directory_deleted",
		"executable_deleted",
		"launch_context_nonrestartable",
		"nonrestartable_pane",
		"pane_dead",
		"pane_launch_nonrestartable",
		"pane_not_restartable",
		"pane_process_deleted",
		"pane_process_terminated",
		"pane_process_unavailable",
		"process_cwd_deleted",
		"process_deleted",
		"process_executable_deleted",
		"process_identity_unavailable",
		"working_directory_deleted",
	}

	for _, reason := range nonrestartableReasons {
		classification := ClassifyPracticalRequirements([]MissingRequirement{
			{Domain: "launch", Status: "unverified", Reason: reason},
		})
		if len(classification.Blockers) != 0 {
			t.Fatalf("reason %q was unexpectedly classified as blocker: %+v", reason, classification.Blockers)
		}

		if len(classification.FidelityLimits) != 1 || classification.FidelityLimits[0].Reason != reason {
			t.Fatalf("reason %q not classified as fidelity limit: %+v", reason, classification.FidelityLimits)
		}
	}
}

func assertStepKinds(t *testing.T, steps []Step, expected []StepKind) {
	t.Helper()

	if len(steps) != len(expected) {
		t.Fatalf("expected %d steps, got %d", len(expected), len(steps))
	}

	for i, exp := range expected {
		if steps[i].Kind != exp {
			t.Errorf("step %d kind = %s, want %s", i, steps[i].Kind, exp)
		}
	}
}

func assertSimpleSummary(t *testing.T, s PlanSummary) {
	t.Helper()

	if s.SessionCount != 1 || s.WindowCount != 1 || s.PaneCount != 1 || s.SharedWindowCount != 0 {
		t.Fatalf("unexpected summary counts: %+v", s)
	}
}

func assertSimpleSessionStep(t *testing.T, cs *CreateSessionStep) {
	t.Helper()

	if cs.SessionID != "s0" || cs.InitialWindowID != "w0" || string(cs.InitialWindowName) != "code" || cs.InitialPaneID != "p0" {
		t.Fatalf("unexpected create session payload: %+v", cs)
	}
}

func TestStandaloneSessionSimple(t *testing.T) {
	m := baseFixture()

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	if diff := cmp.Diff([]string{"s0"}, plan.RequestedSessions); diff != "" {
		t.Fatalf("unexpected requested sessions: %s", diff)
	}

	if len(plan.AddedSessions) != 0 {
		t.Fatalf("expected 0 added sessions, got %v", plan.AddedSessions)
	}

	if diff := cmp.Diff([]string{"s0"}, plan.ClosureSessions); diff != "" {
		t.Fatalf("unexpected closure sessions: %s", diff)
	}

	if !plan.SourceEvidenceComplete() {
		t.Fatal("expected source evidence complete for verified checkpoint manifest")
	}

	assertSimpleSummary(t, plan.Summary)

	expectedKinds := []StepKind{
		StepCreateSession,
		StepPlaceInitialWindow,
		StepApplyLayout,
		StepSetPaneTitle,
		StepSelectPane,
		StepSelectWindow,
	}
	assertStepKinds(t, plan.Steps, expectedKinds)

	assertSimpleSessionStep(t, plan.Steps[0].CreateSession)
}

func countCreationsAndZoom(steps []Step) (int, int, bool) {
	var (
		windowCreates, paneCreates int
		hasZoom                    bool
	)

	for _, step := range steps {
		switch step.Kind {
		case StepCreateSession:
			if step.CreateSession.InitialWindowID != "" {
				windowCreates++
			}

			if step.CreateSession.InitialPaneID != "" {
				paneCreates++
			}
		case StepCreateWindow:
			windowCreates++
			paneCreates++
		case StepSplitPane:
			paneCreates++
		case StepApplyZoom:
			hasZoom = true
		case StepPlaceInitialWindow, StepLinkWindow, StepCloseBootstrapWindow, StepApplyLayout, StepSetPaneTitle, StepSelectPane, StepSelectWindow:
		}
	}

	return windowCreates, paneCreates, hasZoom
}

func TestMultiWindowMultiPane(t *testing.T) {
	m := baseFixture()

	m.Windows[0].Layout = model.Layout{
		Width:  80,
		Height: 24,
		Axis:   "x",
		Children: []model.Layout{
			{Width: 40, Height: 24, Left: 0, Top: 0, Pane: "p0"},
			{
				Width: 39, Height: 24, Left: 41, Top: 0, Axis: "y",
				Children: []model.Layout{
					{Width: 39, Height: 12, Left: 41, Top: 0, Pane: "p1"},
					{Width: 39, Height: 11, Left: 41, Top: 13, Pane: "p2"},
				},
			},
		},
	}
	m.Panes[0].Width = 40

	m.Panes = append(
		m.Panes,
		model.Pane{
			ID:        "p1",
			Window:    "w0",
			Index:     1,
			Title:     model.Bytes("pane1"),
			Directory: model.Bytes("/home/user"),
			Width:     39,
			Height:    12,
			Left:      41,
			Top:       0,
			Active:    false,
			Launch:    model.Launch{Recovery: statusUnverified},
		},
		model.Pane{
			ID:        "p2",
			Window:    "w0",
			Index:     2,
			Title:     model.Bytes("pane2"),
			Directory: model.Bytes("/home/user"),
			Width:     39,
			Height:    11,
			Left:      41,
			Top:       13,
			Active:    false,
			Launch:    model.Launch{Recovery: statusUnverified},
		},
	)

	m.Windows = append(m.Windows, model.Window{
		ID:     "w1",
		Name:   model.Bytes("docs"),
		Width:  80,
		Height: 24,
		Layout: model.Layout{
			Width:  80,
			Height: 24,
			Axis:   "y",
			Children: []model.Layout{
				{Width: 80, Height: 12, Left: 0, Top: 0, Pane: "p3"},
				{Width: 80, Height: 11, Left: 0, Top: 13, Pane: "p4"},
			},
		},
		Zoomed: true,
	})

	m.Links = append(m.Links, model.Link{
		Session: "s0",
		Window:  "w1",
		Index:   1,
		Active:  false,
		Last:    true,
	})

	m.Panes = append(
		m.Panes,
		model.Pane{
			ID:        "p3",
			Window:    "w1",
			Index:     0,
			Title:     model.Bytes("pane3"),
			Directory: model.Bytes("/home/user"),
			Width:     80,
			Height:    24,
			Left:      0,
			Top:       0,
			Active:    true,
			Launch:    model.Launch{Recovery: statusUnverified},
		},
		model.Pane{
			ID:        "p4",
			Window:    "w1",
			Index:     1,
			Title:     model.Bytes("pane4"),
			Directory: model.Bytes("/home/user"),
			Width:     80,
			Height:    11,
			Left:      0,
			Top:       13,
			Active:    false,
			Launch:    model.Launch{Recovery: statusUnverified},
		},
	)

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %+v", err)
	}

	if plan.Summary.WindowCount != 2 || plan.Summary.PaneCount != 5 {
		t.Fatalf("unexpected summary: %+v", plan.Summary)
	}

	windowCreates, paneCreates, hasZoom := countCreationsAndZoom(plan.Steps)
	if windowCreates != 2 {
		t.Fatalf("expected 2 window creations, got %d", windowCreates)
	}

	if paneCreates != 5 {
		t.Fatalf("expected 5 pane creations, got %d", paneCreates)
	}

	if !hasZoom {
		t.Fatal("expected apply-zoom step for zoomed window w1")
	}
}

func checkSharedWindowSteps(steps []Step, winID, sessID string) (int, bool) {
	var (
		creations int
		linked    bool
	)

	for _, step := range steps {
		if (step.Kind == StepCreateWindow && step.CreateWindow.WindowID == winID) ||
			(step.Kind == StepCreateSession && step.CreateSession.InitialWindowID == winID) {
			creations++
		}

		if step.Kind == StepLinkWindow && step.LinkWindow.SessionID == sessID && step.LinkWindow.WindowID == winID {
			linked = true
		}
	}

	return creations, linked
}

func TestSharedWindowsAcrossUnrelatedSessions(t *testing.T) {
	m := baseFixture()

	m.Sessions = append(m.Sessions, model.Session{
		ID:        "s1",
		Name:      model.Bytes("peer"),
		Directory: model.Bytes("/home/peer"),
	})

	m.Windows = append(m.Windows, model.Window{
		ID:     "w1",
		Name:   model.Bytes("shared-win"),
		Width:  80,
		Height: 24,
		Layout: model.Layout{Width: 80, Height: 24, Pane: "p1"},
	})
	m.Panes = append(m.Panes, model.Pane{
		ID:        "p1",
		Window:    "w1",
		Index:     0,
		Title:     model.Bytes("shared-pane"),
		Directory: model.Bytes("/tmp"),
		Width:     80,
		Height:    24,
		Active:    true,
		Launch:    model.Launch{Recovery: statusUnverified},
	})

	m.Links = append(
		m.Links,
		model.Link{Session: "s0", Window: "w1", Index: 1, Active: false, Last: true},
		model.Link{Session: "s1", Window: "w1", Index: 0, Active: true, Last: false},
	)

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	if diff := cmp.Diff([]string{"s0"}, plan.RequestedSessions); diff != "" {
		t.Fatalf("unexpected requested: %s", diff)
	}

	if diff := cmp.Diff([]string{"s1"}, plan.AddedSessions); diff != "" {
		t.Fatalf("unexpected added: %s", diff)
	}

	if diff := cmp.Diff([]string{"s0", "s1"}, plan.ClosureSessions); diff != "" {
		t.Fatalf("unexpected closure: %s", diff)
	}

	if plan.Summary.SharedWindowCount != 1 {
		t.Fatalf("expected 1 shared window, got %d", plan.Summary.SharedWindowCount)
	}

	w1Creations, s1LinksW1 := checkSharedWindowSteps(plan.Steps, "w1", "s1")
	if w1Creations != 1 {
		t.Fatalf("expected exactly 1 creation of shared window w1, got %d", w1Creations)
	}

	if !s1LinksW1 {
		t.Fatal("expected s1 to link shared window w1 via LinkWindow")
	}
}

func inspectGroupSessionSteps(steps []Step) (*Step, *Step, int, int) {
	var (
		s0Step, s1Step       *Step
		linkWins, createWins int
	)

	for i := range steps {
		step := &steps[i]
		if step.Kind == StepCreateSession {
			if step.CreateSession.SessionID == "s0" {
				s0Step = step
			}

			if step.CreateSession.SessionID == "s1" {
				s1Step = step
			}
		}

		if step.Kind == StepLinkWindow {
			linkWins++
		}

		if step.Kind == StepCreateWindow {
			createWins++
		}
	}

	return s0Step, s1Step, linkWins, createWins
}

func TestNativeGroups(t *testing.T) {
	m := baseFixture()

	m.Sessions[0].Group = "g0"
	m.Sessions = append(m.Sessions, model.Session{
		ID:        "s1",
		Name:      model.Bytes("follower"),
		Directory: model.Bytes("/home/user"),
		Group:     "g0",
	})

	m.Groups = []model.Group{
		{
			ID:      "g0",
			Name:    model.Bytes("my-group"),
			Members: []string{"s0", "s1"},
		},
	}

	m.Links = append(m.Links, model.Link{
		Session: "s1",
		Window:  "w0",
		Index:   0,
		Active:  true,
		Last:    false,
	})

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	if diff := cmp.Diff([]string{"s0"}, plan.RequestedSessions); diff != "" {
		t.Fatalf("unexpected requested: %s", diff)
	}

	if diff := cmp.Diff([]string{"s1"}, plan.AddedSessions); diff != "" {
		t.Fatalf("unexpected added: %s", diff)
	}

	assertNativeGroupsSteps(t, plan.Steps)
}

func assertNativeGroupsSteps(t *testing.T, steps []Step) {
	t.Helper()

	s0Step, s1Step, linkWindowCount, createWindowCount := inspectGroupSessionSteps(steps)

	if s0Step == nil || s1Step == nil {
		t.Fatal("missing session steps for s0 or s1")
	}

	if s0Step.CreateSession.Group != "g0" || s0Step.CreateSession.GroupLeaderSessionID != "" {
		t.Fatalf("leader s0 should record source group g0 and empty leader id, got: %+v", s0Step.CreateSession)
	}

	if s1Step.CreateSession.Group != "g0" || s1Step.CreateSession.GroupLeaderSessionID != "s0" {
		t.Fatalf("follower s1 should join group g0 with leader s0, got: %+v", s1Step.CreateSession)
	}

	if s1Step.CreateSession.InitialWindowID != "" {
		t.Fatalf("follower s1 should have no initial window, got: %s", s1Step.CreateSession.InitialWindowID)
	}

	if linkWindowCount != 0 || createWindowCount != 0 {
		t.Fatalf("expected 0 link/create window steps, got links=%d creates=%d", linkWindowCount, createWindowCount)
	}
}

func TestRepeatedLinksAndSparseIndexes(t *testing.T) {
	m := baseFixture()

	m.Links[0].Index = 3
	m.Links = append(m.Links, model.Link{
		Session: "s0",
		Window:  "w0",
		Index:   10,
		Active:  false,
		Last:    true,
	})

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	if !plan.Steps[0].CreateSession.BootstrapWindow {
		t.Fatal("expected BootstrapWindow: true for sparse initial index 3")
	}

	foundCreate3 := false
	foundLink10 := false

	for _, step := range plan.Steps {
		if step.Kind == StepCreateWindow && step.CreateWindow.WindowID == "w0" && step.CreateWindow.Index == 3 {
			foundCreate3 = true
		}

		if step.Kind == StepLinkWindow && step.LinkWindow.WindowID == "w0" && step.LinkWindow.Index == 10 {
			foundLink10 = true
		}
	}

	if !foundCreate3 {
		t.Fatal("expected CreateWindow step for w0 at sparse slot 3")
	}

	if !foundLink10 {
		t.Fatal("expected LinkWindow step for repeated link at slot 10")
	}
}

func TestActiveLastSelection(t *testing.T) {
	m := baseFixture()

	m.Windows = append(m.Windows, model.Window{
		ID:     "w1",
		Name:   model.Bytes("logs"),
		Width:  80,
		Height: 24,
		Layout: model.Layout{Width: 80, Height: 24, Pane: "p1"},
	})
	m.Panes = append(m.Panes, model.Pane{
		ID:        "p1",
		Window:    "w1",
		Index:     0,
		Title:     model.Bytes("logs"),
		Directory: model.Bytes("/tmp"),
		Width:     80,
		Height:    24,
		Active:    true,
		Launch:    model.Launch{Recovery: statusUnverified},
	})

	m.Links = []model.Link{
		{Session: "s0", Window: "w0", Index: 0, Active: true, Last: false},
		{Session: "s0", Window: "w1", Index: 1, Active: false, Last: true},
	}

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	var selectStep *SelectWindowStep

	for _, step := range plan.Steps {
		if step.Kind == StepSelectWindow && step.SelectWindow.SessionID == "s0" {
			selectStep = step.SelectWindow
		}
	}

	if selectStep == nil {
		t.Fatal("missing select-window step for s0")
	}

	if selectStep.ActiveWindowID != "w0" || selectStep.ActiveIndex != 0 {
		t.Fatalf("unexpected active window: %+v", selectStep)
	}

	if selectStep.LastWindowID != "w1" || selectStep.LastIndex != 1 {
		t.Fatalf("unexpected last window: %+v", selectStep)
	}
}

func TestZoom(t *testing.T) {
	m := baseFixture()
	m.Windows[0].Zoomed = true

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	foundZoom := false

	for _, step := range plan.Steps {
		if step.Kind == StepApplyZoom {
			foundZoom = true

			if step.ApplyZoom.WindowID != "w0" || step.ApplyZoom.PaneID != "p0" {
				t.Fatalf("unexpected zoom payload: %+v", step.ApplyZoom)
			}
		}
	}

	if !foundZoom {
		t.Fatal("expected apply-zoom step for zoomed window")
	}

	m.Windows[0].Zoomed = false

	planUnzoomed, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	for _, step := range planUnzoomed.Steps {
		if step.Kind == StepApplyZoom {
			t.Fatal("unexpected apply-zoom step for unzoomed window")
		}
	}
}

func TestIsolatedSelection(t *testing.T) {
	m := baseFixture()

	m.Sessions = append(m.Sessions, model.Session{
		ID:        "s1",
		Name:      model.Bytes("other"),
		Directory: model.Bytes("/tmp"),
	})
	m.Windows = append(m.Windows, model.Window{
		ID:     "w1",
		Name:   model.Bytes("other-win"),
		Width:  80,
		Height: 24,
		Layout: model.Layout{Width: 80, Height: 24, Pane: "p1"},
	})
	m.Links = append(m.Links, model.Link{
		Session: "s1",
		Window:  "w1",
		Index:   0,
		Active:  true,
		Last:    false,
	})
	m.Panes = append(m.Panes, model.Pane{
		ID:        "p1",
		Window:    "w1",
		Index:     0,
		Title:     model.Bytes("other-pane"),
		Directory: model.Bytes("/tmp"),
		Width:     80,
		Height:    24,
		Active:    true,
		Launch:    model.Launch{Recovery: statusUnverified},
	})

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	if len(plan.RequestedSessions) != 1 || plan.RequestedSessions[0] != "s0" {
		t.Fatalf("unexpected requested: %v", plan.RequestedSessions)
	}

	if len(plan.AddedSessions) != 0 {
		t.Fatalf("expected 0 added sessions, got: %v", plan.AddedSessions)
	}

	if len(plan.SourceGraph.Sessions) != 1 || plan.SourceGraph.Sessions[0].ID != "s0" {
		t.Fatalf("expected SourceGraph to contain only s0, got: %v", plan.SourceGraph.Sessions)
	}

	if len(plan.SourceGraph.Windows) != 1 || plan.SourceGraph.Windows[0].ID != "w0" {
		t.Fatalf("expected SourceGraph to contain only w0, got: %v", plan.SourceGraph.Windows)
	}
}

func TestClosureChains(t *testing.T) {
	m := baseFixture()

	m.Sessions = append(
		m.Sessions,
		model.Session{ID: "s1", Name: model.Bytes("s1"), Directory: model.Bytes("/tmp")},
		model.Session{ID: "s2", Name: model.Bytes("s2"), Directory: model.Bytes("/tmp")},
		model.Session{ID: "s3", Name: model.Bytes("s3"), Directory: model.Bytes("/tmp")},
	)

	m.Windows = append(
		m.Windows,
		model.Window{ID: "w1", Name: model.Bytes("w1"), Width: 80, Height: 24, Layout: model.Layout{Width: 80, Height: 24, Pane: "p1"}},
		model.Window{ID: "w2", Name: model.Bytes("w2"), Width: 80, Height: 24, Layout: model.Layout{Width: 80, Height: 24, Pane: "p2"}},
	)

	m.Panes = append(
		m.Panes,
		model.Pane{ID: "p1", Window: "w1", Index: 0, Title: model.Bytes("p1"), Directory: model.Bytes("/tmp"), Width: 80, Height: 24, Active: true, Launch: model.Launch{Recovery: statusUnverified}},
		model.Pane{ID: "p2", Window: "w2", Index: 0, Title: model.Bytes("p2"), Directory: model.Bytes("/tmp"), Width: 80, Height: 24, Active: true, Launch: model.Launch{Recovery: statusUnverified}},
	)

	m.Links = []model.Link{
		{Session: "s0", Window: "w0", Index: 0, Active: true},
		{Session: "s1", Window: "w0", Index: 0, Active: true},
		{Session: "s1", Window: "w1", Index: 1, Active: false, Last: true},
		{Session: "s2", Window: "w1", Index: 0, Active: true},
		{Session: "s2", Window: "w2", Index: 1, Active: false, Last: true},
		{Session: "s3", Window: "w2", Index: 0, Active: true},
	}

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	if diff := cmp.Diff([]string{"s0"}, plan.RequestedSessions); diff != "" {
		t.Fatalf("unexpected requested: %s", diff)
	}

	if diff := cmp.Diff([]string{"s1", "s2", "s3"}, plan.AddedSessions); diff != "" {
		t.Fatalf("unexpected added: %s", diff)
	}

	if diff := cmp.Diff([]string{"s0", "s1", "s2", "s3"}, plan.ClosureSessions); diff != "" {
		t.Fatalf("unexpected closure: %s", diff)
	}

	if plan.Summary.WindowCount != 3 || plan.Summary.PaneCount != 3 {
		t.Fatalf("unexpected window/pane counts: %+v", plan.Summary)
	}
}

func TestNamesWithUnusualBytes(t *testing.T) {
	m := baseFixture()
	m.Sessions[0].Name = model.Bytes("space and \U0001F680 rocket")
	m.Windows[0].Name = model.Bytes("win with \xff high bytes")
	m.Panes[0].Title = model.Bytes("title with 'single' and \"double\" quotes")

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	if string(plan.Steps[0].CreateSession.Name) != "space and \U0001F680 rocket" {
		t.Fatalf("session name not preserved: %s", plan.Steps[0].CreateSession.Name)
	}

	if string(plan.Steps[0].CreateSession.InitialWindowName) != "win with \xff high bytes" {
		t.Fatalf("initial window name step payload not preserved: %s", plan.Steps[0].CreateSession.InitialWindowName)
	}

	var titleStepPayload string

	for _, s := range plan.Steps {
		if s.Kind == StepSetPaneTitle {
			titleStepPayload = string(s.SetPaneTitle.Title)
			break
		}
	}

	if titleStepPayload != "title with 'single' and \"double\" quotes" {
		t.Fatalf("set_pane_title step payload not preserved: %s", titleStepPayload)
	}

	if string(plan.SourceGraph.Windows[0].Name) != "win with \xff high bytes" {
		t.Fatalf("window name not preserved in source graph: %s", plan.SourceGraph.Windows[0].Name)
	}

	if string(plan.SourceGraph.Panes[0].Title) != "title with 'single' and \"double\" quotes" {
		t.Fatalf("pane title not preserved in source graph: %s", plan.SourceGraph.Panes[0].Title)
	}
}

func TestOrderAndDuplicatesIdempotency(t *testing.T) {
	m := baseFixture()
	m.Sessions = append(m.Sessions, model.Session{
		ID:        "s1",
		Name:      model.Bytes("peer"),
		Directory: model.Bytes("/tmp"),
	})
	m.Windows = append(m.Windows, model.Window{
		ID:     "w1",
		Name:   model.Bytes("w1"),
		Width:  80,
		Height: 24,
		Layout: model.Layout{Width: 80, Height: 24, Pane: "p1"},
	})
	m.Panes = append(m.Panes, model.Pane{
		ID:        "p1",
		Window:    "w1",
		Index:     0,
		Title:     model.Bytes("p1"),
		Directory: model.Bytes("/tmp"),
		Width:     80,
		Height:    24,
		Active:    true,
		Launch:    model.Launch{Recovery: statusUnverified},
	})
	m.Links = append(m.Links, model.Link{
		Session: "s1",
		Window:  "w1",
		Index:   0,
		Active:  true,
	})

	p1, err := BuildPlan(m, []string{"s0", "s1"})
	if err != nil {
		t.Fatalf("p1 failed: %v", err)
	}

	p2, err := BuildPlan(m, []string{"s1", "s0"})
	if err != nil {
		t.Fatalf("p2 failed: %v", err)
	}

	p3, err := BuildPlan(m, []string{"s0", "s0", "s1", "s1", "s0"})
	if err != nil {
		t.Fatalf("p3 failed: %v", err)
	}

	if diff := cmp.Diff(p1, p2); diff != "" {
		t.Fatalf("plan p1 and p2 differ under permutation: %s", diff)
	}

	if diff := cmp.Diff(p1, p3); diff != "" {
		t.Fatalf("plan p1 and p3 differ under duplicates: %s", diff)
	}
}

func TestDeepAliasMutation(t *testing.T) {
	m := baseFixture()

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	m.Sessions[0].Name[0] = 'X'
	if plan.SourceGraph.Sessions[0].Name[0] == 'X' {
		t.Fatal("source manifest mutation leaked into plan source graph")
	}

	plan.SourceGraph.Sessions[0].Name[0] = 'Z'
	if m.Sessions[0].Name[0] == 'Z' {
		t.Fatal("plan source graph mutation leaked back into original manifest")
	}
}

type malformedCase struct {
	name      string
	selection []string
	mutate    func(*model.Manifest)
	reason    string
	targetErr error
}

func checkMalformedCase(t *testing.T, tc malformedCase) {
	t.Helper()

	man := baseFixture()
	if tc.mutate != nil {
		tc.mutate(&man)
	}

	_, err := BuildPlan(man, tc.selection)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.Is(err, tc.targetErr) {
		t.Fatalf("expected errors.Is(%v), got %v", tc.targetErr, err)
	}

	oopsErr, ok := oops.AsOops(err)
	if !ok {
		t.Fatalf("expected oops error, got %T", err)
	}

	if len(oopsErr.Layers()) == 0 || oopsErr.Layers()[0].Domain != "snapshot.restore" {
		t.Fatalf("expected owning boundary domain snapshot.restore, got %v", oopsErr.Layers())
	}

	if tc.reason != "" {
		ctx := oopsErr.Context()
		if ctx["reason"] != tc.reason {
			t.Fatalf("expected reason %s, got %v", tc.reason, ctx["reason"])
		}
	}
}

func TestMalformedInputs(t *testing.T) {
	cases := []malformedCase{
		{
			name:      "empty selection",
			selection: []string{},
			mutate:    nil,
			reason:    "empty_selection",
			targetErr: ErrInvalid,
		},
		{
			name:      "oversized selection",
			selection: make([]string, model.MaxEntities+1),
			mutate:    nil,
			reason:    "bounds",
			targetErr: ErrInvalid,
		},
		{
			name:      "malformed id space",
			selection: []string{"s 0"},
			mutate:    nil,
			reason:    "malformed_session",
			targetErr: ErrInvalid,
		},
		{
			name:      "malformed id empty string",
			selection: []string{""},
			mutate:    nil,
			reason:    "malformed_session",
			targetErr: ErrInvalid,
		},
		{
			name:      "name passed instead of id",
			selection: []string{"main"},
			mutate:    nil,
			reason:    "unknown_session",
			targetErr: ErrInvalid,
		},
		{
			name:      "unknown id",
			selection: []string{"s999"},
			mutate:    nil,
			reason:    "unknown_session",
			targetErr: ErrInvalid,
		},
		{
			name:      "invalid manifest format",
			selection: []string{"s0"},
			mutate: func(man *model.Manifest) {
				man.Format = "resurrect"
			},
			reason:    "invalid_manifest",
			targetErr: ErrInvalid,
		},
		{
			name:      "unsupported manifest version",
			selection: []string{"s0"},
			mutate: func(man *model.Manifest) {
				man.Version = 999
			},
			reason:    "",
			targetErr: ErrVersion,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checkMalformedCase(t, tc)
		})
	}
}

func TestEvidenceGaps(t *testing.T) {
	m := baseFixture()
	m.Source.Consistency = "observed"
	m.Evidence[5] = model.Evidence{
		Domain: "S06",
		Status: "observed",
		Reason: "terminal_checkpoint_unavailable",
	}
	m.Evidence[6] = model.Evidence{
		Domain: "S07",
		Status: statusUnverified,
		Reason: "ownership_unverified",
	}

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	if plan.SourceEvidenceComplete() {
		t.Fatal("expected plan.SourceEvidenceComplete() == false when evidence gaps exist")
	}

	expectedGaps := []MissingRequirement{
		{Domain: "S00_Consistency", Status: "observed", Reason: "capture_not_atomic"},
		{Domain: "S06", Status: "observed", Reason: "terminal_checkpoint_unavailable"},
		{Domain: "S07", Status: statusUnverified, Reason: "ownership_unverified"},
		{Domain: "atomicity", Status: "unsupported", Reason: "atomic_restore_transaction_unavailable"},
		{Domain: "launch", Status: "unsupported", Reason: "exactly_once_process_launch_unavailable"},
		{Domain: "ownership", Status: "unsupported", Reason: "immutable_ownership_enforcement_unavailable"},
		{Domain: "target", Status: "unsupported", Reason: "target_options_and_group_names_require_adaptation"},
		{Domain: "terminal", Status: "unsupported", Reason: "terminal_state_checkpoint_unavailable"},
	}
	if diff := cmp.Diff(expectedGaps, plan.MissingRequirements); diff != "" {
		t.Fatalf("unexpected missing requirements: %s", diff)
	}
}

func TestValidatePlanInvariantsDirect(t *testing.T) {
	plan, err := BuildPlan(baseFixture(), []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	t.Run("duplicate step id", func(t *testing.T) {
		corrupted := plan
		corrupted.Steps = append([]Step(nil), plan.Steps...)

		corrupted.Steps = append(corrupted.Steps, plan.Steps[0])
		if err := ValidatePlan(corrupted); err == nil {
			t.Fatal("expected error on duplicate step ID")
		}
	})

	t.Run("dangling dependency", func(t *testing.T) {
		corrupted := plan
		corrupted.Steps = append([]Step(nil), plan.Steps...)

		corrupted.Steps[1].DependsOn = []string{"non-existent-step"}
		if err := ValidatePlan(corrupted); err == nil {
			t.Fatal("expected error on dangling dependency")
		}
	})

	t.Run("forward dependency", func(t *testing.T) {
		corrupted := plan
		corrupted.Steps = append([]Step(nil), plan.Steps...)

		corrupted.Steps[0].DependsOn = []string{corrupted.Steps[1].ID}
		if err := ValidatePlan(corrupted); err == nil {
			t.Fatal("expected error on forward/cyclic dependency")
		}
	})
}

func TestPlanEvidenceCompletenessReflectsManifest(t *testing.T) {
	m := baseFixture()

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	if !plan.SourceEvidenceComplete() {
		t.Fatal("plan.SourceEvidenceComplete() must reflect verified source manifest")
	}

	if len(plan.MissingRequirements) != 5 {
		t.Fatalf("expected 5 runtime/target capability prerequisites, got: %+v", plan.MissingRequirements)
	}
}

func TestPlanRejectsWindowNameWithNULByte(t *testing.T) {
	m := baseFixture()
	m.Windows[0].Name = model.Bytes("bad\x00window")

	_, err := BuildPlan(m, []string{"s0"})
	if err == nil {
		t.Fatal("expected BuildPlan to reject window name containing NUL byte")
	}

	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for NUL byte in window name, got: %v", err)
	}
}

func TestGroupFollowerSelectWindowDependsOnSessionCreation(t *testing.T) {
	m := baseFixture()
	m.Sessions[0].Group = "g0"
	m.Groups = []model.Group{
		{
			ID:      "g0",
			Name:    model.Bytes("pair"),
			Members: []string{"s0", "s1"},
		},
	}
	m.Sessions = append(m.Sessions, model.Session{
		ID:        "s1",
		Name:      model.Bytes("follower"),
		Group:     "g0",
		Directory: model.Bytes("/tmp"),
	})
	m.Links = append(m.Links, model.Link{
		Session: "s1",
		Window:  "w0",
		Index:   0,
		Active:  true,
	})

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	var followerSelectWindow *Step

	for i := range plan.Steps {
		if plan.Steps[i].Kind == StepSelectWindow && plan.Steps[i].SelectWindow.SessionID == "s1" {
			followerSelectWindow = &plan.Steps[i]
			break
		}
	}

	if followerSelectWindow == nil {
		t.Fatal("missing select-window step for follower session s1")
	}

	hasSessionDep := slices.Contains(followerSelectWindow.DependsOn, "create-session:s1")

	if !hasSessionDep {
		t.Fatalf("follower select-window step does not depend on create-session:s1; dependsOn: %v", followerSelectWindow.DependsOn)
	}
}

func TestGroupLeaderAndFollowerWindowSharing(t *testing.T) {
	m := baseFixture()
	m.Sessions[0].Group = "g0"
	m.Groups = []model.Group{
		{
			ID:      "g0",
			Name:    model.Bytes("pair"),
			Members: []string{"s0", "s1"},
		},
	}
	m.Sessions = append(m.Sessions, model.Session{
		ID:        "s1",
		Name:      model.Bytes("follower"),
		Group:     "g0",
		Directory: model.Bytes("/tmp"),
	})
	m.Links = append(m.Links, model.Link{
		Session: "s1",
		Window:  "w0",
		Index:   0,
		Active:  true,
	})

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	assertLeaderAndFollowerSteps(t, plan.Steps)
}

func assertLeaderAndFollowerSteps(t *testing.T, steps []Step) {
	t.Helper()

	leader, follower, _, _ := inspectGroupSessionSteps(steps)
	if leader == nil || follower == nil {
		t.Fatal("missing create-session step for leader s0 or follower s1")
	}

	leaderCreate, followerCreate := leader.CreateSession, follower.CreateSession

	if leaderCreate.Group != "g0" || leaderCreate.GroupLeaderSessionID != "" {
		t.Errorf("leader CreateSession group/leader = (%q, %q), want (g0, empty)", leaderCreate.Group, leaderCreate.GroupLeaderSessionID)
	}

	if leaderCreate.InitialWindowID != "w0" || leaderCreate.InitialWindowIndex != 0 {
		t.Errorf("leader CreateSession initial window = (%s, %d), want (w0, 0)", leaderCreate.InitialWindowID, leaderCreate.InitialWindowIndex)
	}

	if followerCreate.Group != "g0" || followerCreate.GroupLeaderSessionID != "s0" {
		t.Errorf("follower CreateSession group ref = (%q, %q), want (g0, s0)", followerCreate.Group, followerCreate.GroupLeaderSessionID)
	}

	if followerCreate.InitialWindowID != "" {
		t.Errorf("follower CreateSession.InitialWindowID = %q, want empty", followerCreate.InitialWindowID)
	}
}

func TestSparseSlotWindowCreationUsesBootstrapWindow(t *testing.T) {
	m := baseFixture()
	m.Links[0].Index = 5

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	assertSparseSlotSteps(t, plan.Steps)
}

func assertSparseSlotSteps(t *testing.T, steps []Step) {
	t.Helper()

	var (
		createSess        *CreateSessionStep
		hasCreateWindow   bool
		hasCloseBootstrap bool
	)

	for _, s := range steps {
		switch s.Kind {
		case StepCreateSession:
			createSess = s.CreateSession
		case StepCreateWindow:
			hasCreateWindow = true

			if s.CreateWindow.Index != 5 {
				t.Errorf("CreateWindow.Index = %d, want 5", s.CreateWindow.Index)
			}
		case StepCloseBootstrapWindow:
			hasCloseBootstrap = true
		case StepPlaceInitialWindow, StepSplitPane, StepLinkWindow, StepApplyLayout, StepSetPaneTitle, StepSelectPane, StepApplyZoom, StepSelectWindow:
		}
	}

	if createSess == nil || !createSess.BootstrapWindow || !hasCreateWindow || !hasCloseBootstrap {
		t.Fatalf("sparse slot step assertion failed: sess=%+v, createWin=%v, closeBoot=%v", createSess, hasCreateWindow, hasCloseBootstrap)
	}
}

func TestValidatePlanRejectsInvalidSplitDimensions(t *testing.T) {
	m := baseFixture()
	m.Windows[0].Layout = model.Layout{
		Width:  80,
		Height: 24,
		Left:   0,
		Top:    0,
		Axis:   "x",
		Children: []model.Layout{
			{Width: 40, Height: 24, Left: 0, Top: 0, Pane: "p0"},
			{Width: 39, Height: 24, Left: 41, Top: 0, Pane: "p1"},
		},
	}
	m.Panes[0].Width = 40
	m.Panes = append(m.Panes, model.Pane{
		ID:        "p1",
		Window:    "w0",
		Index:     1,
		Title:     model.Bytes("p1"),
		Directory: model.Bytes("/home/user"),
		Width:     39,
		Height:    24,
		Left:      41,
		Top:       0,
		Active:    false,
		Launch:    model.Launch{Recovery: statusUnverified},
	})

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %+v", err)
	}

	for i := range plan.Steps {
		if plan.Steps[i].Kind == StepSplitPane {
			badPlan := plan
			badPlan.Steps = make([]Step, len(plan.Steps))
			copy(badPlan.Steps, plan.Steps)
			badSP := *badPlan.Steps[i].SplitPane
			badSP.Width = 0
			badPlan.Steps[i].SplitPane = &badSP

			if err := ValidatePlan(badPlan); err == nil {
				t.Fatal("expected ValidatePlan to reject Width < 1")
			}

			break
		}
	}
}

func TestDeepOwnershipStepsImmutability(t *testing.T) {
	m := baseFixture()

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	if len(plan.Steps[0].CreateSession.Name) > 0 {
		plan.Steps[0].CreateSession.Name[0] = 'Z'
		if plan.SourceGraph.Sessions[0].Name[0] == 'Z' {
			t.Fatal("mutating plan.Steps[0].CreateSession.Name mutated plan.SourceGraph.Sessions[0].Name (aliasing bug)")
		}
	}

	if len(plan.Steps[0].CreateSession.Directory) > 0 {
		plan.Steps[0].CreateSession.Directory[0] = 'X'
		if plan.SourceGraph.Sessions[0].Directory[0] == 'X' {
			t.Fatal("mutating plan.Steps[0].CreateSession.Directory mutated plan.SourceGraph.Sessions[0].Directory (aliasing bug)")
		}
	}
}

func TestValidatePlanRejectsTamperedPayloadsAndCounts(t *testing.T) {
	m := baseFixture()

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	t.Run("tampered summary session count", func(t *testing.T) {
		bad := plan

		bad.Summary.SessionCount = 999
		if err := ValidatePlan(bad); err == nil {
			t.Fatal("ValidatePlan accepted tampered Summary.SessionCount")
		}
	})

	t.Run("tampered summary step count", func(t *testing.T) {
		bad := plan

		bad.Summary.StepCount = 999
		if err := ValidatePlan(bad); err == nil {
			t.Fatal("ValidatePlan accepted tampered Summary.StepCount")
		}
	})

	t.Run("tampered requested sessions", func(t *testing.T) {
		bad := plan

		bad.RequestedSessions = []string{"s99"}
		if err := ValidatePlan(bad); err == nil {
			t.Fatal("ValidatePlan accepted tampered RequestedSessions")
		}
	})

	t.Run("tampered closure sessions", func(t *testing.T) {
		bad := plan

		bad.ClosureSessions = []string{"s0", "s1"}
		if err := ValidatePlan(bad); err == nil {
			t.Fatal("ValidatePlan accepted tampered ClosureSessions")
		}
	})

	t.Run("tampered create session directory", func(t *testing.T) {
		bad := plan
		bad.Steps = make([]Step, len(plan.Steps))
		copy(bad.Steps, plan.Steps)
		badCS := *bad.Steps[0].CreateSession
		badCS.Directory = model.Bytes("/tampered")

		bad.Steps[0].CreateSession = &badCS
		if err := ValidatePlan(bad); err == nil {
			t.Fatal("ValidatePlan accepted tampered CreateSession.Directory")
		}
	})

	t.Run("tampered select window active index", func(t *testing.T) {
		bad := plan
		bad.Steps = make([]Step, len(plan.Steps))
		copy(bad.Steps, plan.Steps)
		lastIdx := len(bad.Steps) - 1
		badSW := *bad.Steps[lastIdx].SelectWindow
		badSW.ActiveIndex = 99

		bad.Steps[lastIdx].SelectWindow = &badSW
		if err := ValidatePlan(bad); err == nil {
			t.Fatal("ValidatePlan accepted tampered SelectWindow.ActiveIndex")
		}
	})
}

func TestSimulatedTmuxSlotOccupancyAndGroupLinkPropagation(t *testing.T) {
	m := baseFixture()
	m.Sessions[0].Group = "g0"

	m.Sessions = append(
		m.Sessions,
		model.Session{
			ID:        "s1",
			Name:      model.Bytes("follower"),
			Group:     "g0",
			Directory: model.Bytes("/home/user"),
		},
		model.Session{
			ID:        "s2",
			Name:      model.Bytes("standalone"),
			Directory: model.Bytes("/tmp"),
		},
	)

	m.Groups = []model.Group{
		{
			ID:      "g0",
			Name:    model.Bytes("g0"),
			Members: []string{"s0", "s1"},
		},
	}

	m.Windows = append(m.Windows, model.Window{
		ID:     "w1",
		Name:   model.Bytes("w1"),
		Width:  80,
		Height: 24,
		Layout: model.Layout{Width: 80, Height: 24, Pane: "p1"},
	})

	m.Panes = append(m.Panes, model.Pane{
		ID:        "p1",
		Window:    "w1",
		Index:     0,
		Title:     model.Bytes("p1"),
		Directory: model.Bytes("/tmp"),
		Width:     80,
		Height:    24,
		Active:    true,
		Launch:    model.Launch{Recovery: statusUnverified},
	})

	m.Links = []model.Link{
		{Session: "s0", Window: "w0", Index: 0, Active: true},
		{Session: "s1", Window: "w0", Index: 0, Active: true},
		{Session: "s2", Window: "w1", Index: 0, Active: true},
		{Session: "s2", Window: "w0", Index: 7, Active: false, Last: true},
	}

	plan, err := BuildPlan(m, []string{"s0", "s2"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	for _, base := range []int{0, 1, 5} {
		assertSimulatedSlots(t, plan, base)
	}
}
