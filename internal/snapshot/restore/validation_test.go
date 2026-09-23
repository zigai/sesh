package restore

import (
	"errors"
	"maps"
	"strconv"
	"testing"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func TestPlanRejectsDependencyExpansionBeforeConstruction(t *testing.T) {
	m := baseFixture()
	m.Panes, m.Links = nil, nil
	m.Windows[0].Width = 599
	m.Windows[0].Layout = model.Layout{Width: 599, Height: 24, Axis: "x"}

	for i := range 300 {
		id := "p" + strconv.Itoa(i)
		m.Panes = append(m.Panes, model.Pane{
			ID: id, Window: "w0", Index: i, Width: 1, Height: 24, Left: 2 * i,
			Directory: model.Bytes("/tmp"), Active: i == 0, Launch: model.Launch{Recovery: statusUnverified},
		})
		m.Links = append(m.Links, model.Link{Session: "s0", Window: "w0", Index: i, Active: i == 0})
		m.Windows[0].Layout.Children = append(m.Windows[0].Layout.Children,
			model.Layout{Pane: id, Width: 1, Height: 24, Left: 2 * i})
	}

	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}

	if _, err := BuildPlan(m, []string{"s0"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expanded plan budget: %v", err)
	}
}

func TestValidatePlanRejectsNestedPayloadChanges(t *testing.T) {
	for name, mutate := range nestedPayloadMutations() {
		t.Run(name, func(t *testing.T) {
			plan, err := BuildPlan(nestedLayoutFixture(), []string{"s0"})
			if err != nil {
				t.Fatal(err)
			}

			for i := range plan.Steps {
				mutate(&plan.Steps[i])
			}

			if err := ValidatePlan(plan); !errors.Is(err, ErrInvalid) {
				t.Fatalf("tampered payload accepted: %v", err)
			}
		})
	}
}

func basePayloadMutations() map[string]func(*Step) {
	return map[string]func(*Step){
		"session environment": func(s *Step) {
			if s.CreateSession != nil {
				s.CreateSession.Environment = []model.Variable{{Name: model.Bytes("EXTRA"), State: "set", Value: model.Bytes("value")}}
			}
		},
		"group name": func(s *Step) {
			if s.CreateSession != nil {
				s.CreateSession.GroupName = model.Bytes("tampered")
			}
		},
		"group leader": func(s *Step) {
			if s.CreateSession != nil {
				s.CreateSession.GroupLeaderSessionID = "s0"
			}
		},
		"session width": func(s *Step) {
			if s.CreateSession != nil {
				s.CreateSession.Width++
			}
		},
		"session height": func(s *Step) {
			if s.CreateSession != nil {
				s.CreateSession.Height++
			}
		},
		"initial placement": func(s *Step) {
			if s.PlaceInitialWindow != nil {
				s.PlaceInitialWindow.Index = 5
			}
		},
		"process arguments": func(s *Step) {
			if s.SplitPane != nil {
				s.SplitPane.Launch.Arguments = []model.Bytes{model.Bytes("unexpected")}
			}
		},
		"terminal": func(s *Step) {
			if s.SplitPane != nil {
				s.SplitPane.Terminal.CursorX++
			}
		},
	}
}

func titlePayloadMutations() map[string]func(*Step) {
	return map[string]func(*Step){
		"initial window name": func(s *Step) {
			if s.CreateSession != nil {
				s.CreateSession.InitialWindowName = model.Bytes("tampered-win-name")
			}
		},
		"set pane title text": func(s *Step) {
			if s.SetPaneTitle != nil {
				s.SetPaneTitle.Title = model.Bytes("tampered-title")
			}
		},
		"set pane title unknown pane": func(s *Step) {
			if s.SetPaneTitle != nil {
				s.SetPaneTitle.PaneID = "p999"
			}
		},
		"set pane title window mismatch": func(s *Step) {
			if s.SetPaneTitle != nil {
				s.SetPaneTitle.WindowID = "w999"
			}
		},
		"set pane title missing deps": func(s *Step) {
			if s.SetPaneTitle != nil {
				s.DependsOn = nil
			}
		},
	}
}

func nestedPayloadMutations() map[string]func(*Step) {
	muts := basePayloadMutations()
	maps.Copy(muts, titlePayloadMutations())

	return muts
}

func TestValidatePlanRejectsCreateWindowPayloadChanges(t *testing.T) {
	m := baseFixture()
	m.Windows = append(m.Windows, model.Window{
		ID: "w1", Name: model.Bytes("second"), Width: 100, Height: 30,
		Layout: model.Layout{Width: 100, Height: 30, Pane: "p1"},
	})
	m.Panes = append(m.Panes, model.Pane{
		ID: "p1", Window: "w1", Index: 0, Width: 100, Height: 30, Active: true,
		Directory: model.Bytes("/tmp"), Launch: model.Launch{Recovery: statusUnverified},
	})
	m.Links = append(m.Links, model.Link{Session: "s0", Window: "w1", Index: 1, Active: false})

	mutations := map[string]func(*Step){
		"window width": func(s *Step) {
			if s.CreateWindow != nil {
				s.CreateWindow.Width++
			}
		},
		"window height": func(s *Step) {
			if s.CreateWindow != nil {
				s.CreateWindow.Height++
			}
		},
	}

	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			plan, err := BuildPlan(m, []string{"s0"})
			if err != nil {
				t.Fatal(err)
			}

			for i := range plan.Steps {
				mutate(&plan.Steps[i])
			}

			if err := ValidatePlan(plan); !errors.Is(err, ErrInvalid) {
				t.Fatalf("tampered payload accepted: %v", err)
			}
		})
	}
}

func TestValidatePlanRejectsMissingPaneTitleFinalization(t *testing.T) {
	plan, err := BuildPlan(nestedLayoutFixture(), []string{"s0"})
	if err != nil {
		t.Fatal(err)
	}

	filtered := make([]Step, 0, len(plan.Steps))
	for _, s := range plan.Steps {
		if s.Kind != StepSetPaneTitle {
			filtered = append(filtered, s)
		}
	}

	plan.Steps = filtered
	plan.Summary.StepCount = len(filtered)

	if err := ValidatePlan(plan); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for missing set_pane_title step, got: %v", err)
	}
}

func TestValidatePlanRejectsDuplicatePaneTitleFinalization(t *testing.T) {
	plan, err := BuildPlan(nestedLayoutFixture(), []string{"s0"})
	if err != nil {
		t.Fatal(err)
	}

	var duplicate Step

	for _, s := range plan.Steps {
		if s.Kind == StepSetPaneTitle {
			duplicate = s
			duplicate.ID = "duplicate-title-step"

			break
		}
	}

	if duplicate.ID == "" {
		t.Fatal("expected at least one set_pane_title step in plan")
	}

	plan.Steps = append(plan.Steps, duplicate)
	plan.Summary.StepCount = len(plan.Steps)

	if err := ValidatePlan(plan); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for duplicate set_pane_title step, got: %v", err)
	}
}

func TestValidatePlanRejectsMissingSetPaneTitlePayload(t *testing.T) {
	plan, err := BuildPlan(nestedLayoutFixture(), []string{"s0"})
	if err != nil {
		t.Fatal(err)
	}

	for i := range plan.Steps {
		if plan.Steps[i].Kind == StepSetPaneTitle {
			plan.Steps[i].SetPaneTitle = nil
			break
		}
	}

	if err := ValidatePlan(plan); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for nil SetPaneTitle payload, got: %v", err)
	}
}

func TestValidatePlanRejectsUnexpectedInitialWindowNameOnBootstrap(t *testing.T) {
	m := baseFixture()
	m.Links[0].Index = 5

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatal(err)
	}

	for i := range plan.Steps {
		if plan.Steps[i].Kind == StepCreateSession && plan.Steps[i].CreateSession.BootstrapWindow {
			plan.Steps[i].CreateSession.InitialWindowName = model.Bytes("illegal-bootstrap-name")
			break
		}
	}

	if err := ValidatePlan(plan); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for unexpected initial window name on bootstrap, got: %v", err)
	}
}

func TestValidatePlanRejectsTamperedInitialWindowName(t *testing.T) {
	plan, err := BuildPlan(baseFixture(), []string{"s0"})
	if err != nil {
		t.Fatal(err)
	}

	for i := range plan.Steps {
		if plan.Steps[i].Kind == StepCreateSession {
			plan.Steps[i].CreateSession.InitialWindowName = model.Bytes("tampered-init-win")
			break
		}
	}

	if err := ValidatePlan(plan); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for tampered initial window name, got: %v", err)
	}
}

func TestValidatePlanRejectsSelectWindowMissingWindowFinalizationDep(t *testing.T) {
	plan, err := BuildPlan(baseFixture(), []string{"s0"})
	if err != nil {
		t.Fatal(err)
	}

	corruptedSteps := append([]Step(nil), plan.Steps...)
	for i := range corruptedSteps {
		if corruptedSteps[i].Kind == StepSelectWindow {
			corruptedSteps[i].DependsOn = []string{"create-session:s0", "place-initial-window:s0"}
			break
		}
	}

	corruptedPlan := plan
	corruptedPlan.Steps = corruptedSteps

	if err := ValidatePlan(corruptedPlan); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected public ValidatePlan to reject tampered plan, got: %v", err)
	}

	tracker := newPlanTracker(len(corruptedSteps), len(plan.SourceGraph.Sessions), len(plan.SourceGraph.Windows), len(plan.SourceGraph.Panes))
	ctx := validationContext{
		graph:      plan.SourceGraph,
		tracker:    &tracker,
		seenSteps:  make(map[string]int, len(corruptedSteps)),
		groupNames: make(map[string]model.Bytes),
	}

	err = validatePlanSteps(corruptedSteps, ctx)
	if err == nil {
		err = tracker.checkFinalizationCoverage(plan.SourceGraph)
	}

	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid when select-window omits window finalization dependency, got: %v", err)
	}
}

func TestValidatePlanRejectsSelectPaneBeforePaneTitle(t *testing.T) {
	plan, err := BuildPlan(baseFixture(), []string{"s0"})
	if err != nil {
		t.Fatal(err)
	}

	var titleIdx, selectIdx int

	for i, s := range plan.Steps {
		if s.Kind == StepSetPaneTitle {
			titleIdx = i
		}

		if s.Kind == StepSelectPane {
			selectIdx = i
		}
	}

	if titleIdx == 0 || selectIdx == 0 {
		t.Fatalf("expected title and select steps in plan, got titleIdx=%d, selectIdx=%d", titleIdx, selectIdx)
	}

	corruptedSteps := append([]Step(nil), plan.Steps...)
	corruptedSteps[selectIdx].DependsOn = []string{"apply-layout:w0"}
	corruptedSteps[titleIdx], corruptedSteps[selectIdx] = corruptedSteps[selectIdx], corruptedSteps[titleIdx]

	corruptedPlan := plan
	corruptedPlan.Steps = corruptedSteps

	if err := ValidatePlan(corruptedPlan); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected public ValidatePlan to reject tampered plan, got: %v", err)
	}

	tracker := newPlanTracker(len(corruptedSteps), len(plan.SourceGraph.Sessions), len(plan.SourceGraph.Windows), len(plan.SourceGraph.Panes))
	ctx := validationContext{
		graph:      plan.SourceGraph,
		tracker:    &tracker,
		seenSteps:  make(map[string]int, len(corruptedSteps)),
		groupNames: make(map[string]model.Bytes),
	}

	err = validatePlanSteps(corruptedSteps, ctx)
	if err == nil {
		err = tracker.checkFinalizationCoverage(plan.SourceGraph)
	}

	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid when select-pane runs before set-pane-title, got: %v", err)
	}
}
