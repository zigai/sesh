package restore

import (
	"slices"
	"testing"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func nestedLayoutFixture() model.Manifest {
	m := baseFixture()
	m.Links[0].Index = 5
	m.Windows[0].Layout = model.Layout{Width: 80, Height: 24, Axis: "x", Children: []model.Layout{
		{Width: 39, Height: 24, Axis: "y", Children: []model.Layout{
			{Width: 39, Height: 8, Pane: "p0"},
			{Width: 39, Height: 15, Top: 9, Pane: "p1"},
		}},
		{Width: 40, Height: 24, Left: 40, Axis: "y", Children: []model.Layout{
			{Width: 40, Height: 17, Left: 40, Pane: "p2"},
			{Width: 40, Height: 6, Left: 40, Top: 18, Pane: "p3"},
		}},
	}}

	m.Panes = nil
	for i, rect := range []model.Layout{
		{Pane: "p0", Width: 39, Height: 8},
		{Pane: "p1", Width: 39, Height: 15, Top: 9},
		{Pane: "p2", Width: 40, Height: 17, Left: 40},
		{Pane: "p3", Width: 40, Height: 6, Left: 40, Top: 18},
	} {
		m.Panes = append(m.Panes, model.Pane{
			ID: rect.Pane, Window: "w0", Index: i, Directory: model.Bytes("/tmp"),
			Width: rect.Width, Height: rect.Height, Left: rect.Left, Top: rect.Top,
			Active: i == 0, Launch: model.Launch{Recovery: statusUnverified},
		})
	}

	return m
}

func TestSplitDependenciesPreserveIntermediateSubtrees(t *testing.T) {
	plan, err := BuildPlan(nestedLayoutFixture(), []string{"s0"})
	if err != nil {
		t.Fatal(err)
	}

	steps := append([]Step(nil), plan.Steps...)
	completed := make(map[string]bool)

	var splitOrder []string

	for len(steps) != 0 {
		chosen := -1

		for i, step := range slices.Backward(steps) {
			if dependenciesComplete(step, completed) {
				chosen = i
				break
			}
		}

		if chosen < 0 {
			t.Fatal("no schedulable step")
		}

		step := steps[chosen]

		completed[step.ID] = true
		if step.SplitPane != nil {
			splitOrder = append(splitOrder, step.SplitPane.PaneID)
		}

		steps = append(steps[:chosen], steps[chosen+1:]...)
	}

	if len(splitOrder) != 3 || splitOrder[0] != "p2" || splitOrder[1] != "p1" || splitOrder[2] != "p3" {
		t.Fatalf("subtree split order: %v", splitOrder)
	}
}

func dependenciesComplete(step Step, completed map[string]bool) bool {
	for _, dependency := range step.DependsOn {
		if !completed[dependency] {
			return false
		}
	}

	return true
}
