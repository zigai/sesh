package automation_test

import (
	"testing"

	"github.com/zigai/sesh/internal/automation"
)

func TestEvaluateStartupRestore_Guards(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		state      automation.StartupState
		wantAllow  bool
		wantReason string
	}{
		{
			name:       "disabled",
			state:      automation.StartupState{Enabled: false, RepositoryPresent: true, Generation: "g"},
			wantReason: "disabled",
		},
		{
			name:       "missing repository",
			state:      automation.StartupState{Enabled: true, RepositoryPresent: false},
			wantReason: "repository_missing",
		},
		{
			name: "pending journaled operation is refused before replay",
			state: automation.StartupState{
				Enabled:           true,
				RepositoryPresent: true,
				PendingOperations: 1,
				Generation:        "g",
			},
			wantReason: "pending_operation",
		},
		{
			name: "unresolved save outcome",
			state: automation.StartupState{
				Enabled:           true,
				RepositoryPresent: true,
				RecoveryRequired:  true,
			},
			wantReason: "recovery_required",
		},
		{
			name: "observational save is not full recovery",
			state: automation.StartupState{
				Enabled:           true,
				RepositoryPresent: true,
				ObservationOnly:   true,
				Generation:        "g",
			},
			wantReason: "observation_incomplete",
		},
		{
			name: "explicit practical profile admits observational generation",
			state: automation.StartupState{
				Enabled:           true,
				RepositoryPresent: true,
				ObservationOnly:   true,
				Generation:        "g",
				Practical:         true,
			},
			wantAllow:  true,
			wantReason: "admitted",
		},
		{
			name: "no readable generation",
			state: automation.StartupState{
				Enabled:           true,
				RepositoryPresent: true,
			},
			wantReason: "observation_incomplete",
		},
		{
			name: "complete generation",
			state: automation.StartupState{
				Enabled:           true,
				RepositoryPresent: true,
				Generation:        "g",
			},
			wantAllow:  true,
			wantReason: "admitted",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			admission := automation.EvaluateStartupRestore(testCase.state)

			if admission.Allowed != testCase.wantAllow {
				t.Fatalf("Allowed = %t, want %t", admission.Allowed, testCase.wantAllow)
			}

			if admission.Reason != testCase.wantReason {
				t.Fatalf("Reason = %q, want %q", admission.Reason, testCase.wantReason)
			}
		})
	}
}

func TestEvaluateStartupRestore_AdmissionCarriesGenerationIdentity(t *testing.T) {
	t.Parallel()

	admission := automation.EvaluateStartupRestore(automation.StartupState{
		Enabled:           true,
		RepositoryPresent: true,
		Generation:        "generation-1",
	})

	if !admission.Allowed {
		t.Fatalf("admission = %+v, want allowed", admission)
	}

	if admission.Generation != "generation-1" {
		t.Fatalf("Generation = %q, want generation-1", admission.Generation)
	}
}
