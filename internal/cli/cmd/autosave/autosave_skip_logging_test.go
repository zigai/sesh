package autosave

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/cli/clibase"
)

func TestAutomaticSnapshotSkipsLogOnlyFixedReasonsWithoutPrivateOutcomeFields(t *testing.T) {
	for _, reason := range []string{
		automation.ReasonDisabled, automation.ReasonNotDue, automation.ReasonRepositoryMissing,
		autosaveReasonLockBusy, autosaveReasonTmuxNotRunning,
	} {
		t.Run(reason, func(t *testing.T) {
			var logs bytes.Buffer

			cmd := &cobra.Command{Annotations: map[string]string{clibase.AnnotationOperation: "restore-on-start"}}
			cmd.SetContext(zerolog.New(&logs).WithContext(t.Context()))

			outcome := newAutosaveOutcome(autosaveStatusSkipped, reason)
			outcome.generation, outcome.attempt = "PRIVATE_GENERATION", "PRIVATE_ATTEMPT"
			logAutosaveSkip(cmd, outcome)

			var record map[string]any
			if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
				t.Fatal(err)
			}

			want := map[string]any{
				"level": "info", "component": "snapshot", "operation": "restore-on-start", "status": "skipped",
				"reason": reason, "message": "automatic snapshot operation skipped",
			}
			if diff := cmp.Diff(want, record); diff != "" {
				t.Fatalf("incorrect skip schema: %s", diff)
			}

			if cmd.Annotations[clibase.AnnotationSuccessOwned] != "true" {
				t.Fatal("skip must own its success outcome")
			}
		})
	}
}

func TestAutomaticSnapshotSkipLoggingLeavesDomainOutcomesAndTerminalFailuresToTheirOwners(t *testing.T) {
	for _, outcome := range []autosaveOutcome{
		newAutosaveOutcome(autosaveStatusSaved, automation.ReasonDurableGeneration),
		newAutosaveOutcome(autosaveStatusSkipped, automation.ReasonRecoveryRequired),
		newAutosaveOutcome(autosaveStatusSkipped, autosaveReasonRestorePending),
		newAutosaveOutcome(autosaveStatusSkipped, "PRIVATE_REASON"),
	} {
		var logs bytes.Buffer

		cmd := &cobra.Command{Annotations: map[string]string{clibase.AnnotationOperation: "save-if-due"}}
		cmd.SetContext(zerolog.New(&logs).WithContext(t.Context()))
		logAutosaveSkip(cmd, outcome)

		if logs.Len() != 0 {
			t.Fatalf("duplicate or unsafe skip log: %s", logs.String())
		}
	}
}
