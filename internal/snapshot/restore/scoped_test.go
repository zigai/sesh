//go:build linux || darwin

package restore

import (
	"slices"
	"testing"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func TestEffectiveEnvironmentPreservesAbsenceRemovalAndEmpty(t *testing.T) {
	global := []model.Variable{
		{Name: model.Bytes("INHERITED"), Value: model.Bytes("global"), State: "set"},
		{Name: model.Bytes("ABSENT"), State: "absent"},
		{Name: model.Bytes("REMOVED"), Value: model.Bytes("global"), State: "set"},
	}
	local := []model.Variable{
		{Name: model.Bytes("INHERITED"), State: "absent"},
		{Name: model.Bytes("ABSENT"), State: "absent"},
		{Name: model.Bytes("REMOVED"), State: "removed"},
		{Name: model.Bytes("EMPTY"), Value: model.Bytes(""), State: "set"},
	}

	got := effectiveEnvironment(global, local)
	if got["INHERITED"] != (environmentShape{value: "global"}) {
		t.Fatalf("absent local entry must inherit global value: %+v", got["INHERITED"])
	}

	if _, ok := got["ABSENT"]; ok {
		t.Fatalf("absent global and local entry must remain absent: %+v", got["ABSENT"])
	}

	if got["REMOVED"] != (environmentShape{unset: true}) {
		t.Fatalf("removed entry lost its state: %+v", got["REMOVED"])
	}

	if got["EMPTY"] != (environmentShape{value: ""}) {
		t.Fatalf("set-empty entry lost its state: %+v", got["EMPTY"])
	}
}

func TestInheritedColorSentinelsAreClassifiedBeforeMutation(t *testing.T) {
	tests := []struct {
		name        string
		option      model.Option
		unsupported bool
		materialize bool
	}{
		{
			name: "pane cursor none",
			option: model.Option{
				Scope: "window", Name: model.Bytes(cursorColorOption), Value: model.Bytes("none"), Present: true, Inherited: true,
			},
			unsupported: true,
			materialize: false,
		},
		{
			name: "prompt cursor none",
			option: model.Option{
				Scope: "session", Name: model.Bytes(promptCursorColorOption), Value: model.Bytes("none"), Present: true, Inherited: true,
			},
			unsupported: true,
			materialize: false,
		},
		{
			name: "writable cursor color",
			option: model.Option{
				Scope: "window", Name: model.Bytes(cursorColorOption), Value: model.Bytes("red"), Present: true, Inherited: true,
			},
			unsupported: false,
			materialize: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := []model.Option{tt.option}
			if got := hasUnsupportedInheritedOption(options); got != tt.unsupported {
				t.Fatalf("unsupported = %v, want %v", got, tt.unsupported)
			}

			if got := hasMaterializableInheritedOption(options); got != tt.materialize {
				t.Fatalf("materialize = %v, want %v", got, tt.materialize)
			}

			_, present := wantedOptions(options, true)[string(tt.option.Name)]
			if present != tt.materialize {
				t.Fatalf("mutation planned = %v, want %v", present, tt.materialize)
			}
		})
	}

	manifest := baseFixture()
	manifest.Sessions[0].Options = []model.Option{tests[1].option}
	manifest.Windows[0].Options = []model.Option{tests[2].option}
	plan := Plan{
		RequestedSessions:   nil,
		AddedSessions:       nil,
		ClosureSessions:     nil,
		SourceGraph:         manifest,
		Steps:               nil,
		MissingRequirements: nil,
		Summary: PlanSummary{
			SessionCount: 0, GroupCount: 0, WindowCount: 0, PaneCount: 0,
			LinkCount: 0, StepCount: 0, SharedWindowCount: 0, SharedPaneCount: 0,
		},
	}

	requirements := ScopedStateRequirements(plan)
	if !slices.Contains(requirements, MissingRequirement{
		Domain: "options", Status: "partial", Reason: "inherited_option_not_representable",
	}) {
		t.Fatalf("unsupported inherited requirement missing: %+v", requirements)
	}

	if !slices.Contains(requirements, MissingRequirement{
		Domain: "options", Status: "partial", Reason: "inherited_options_materialized_locally",
	}) {
		t.Fatalf("materialized inherited requirement missing: %+v", requirements)
	}
}
