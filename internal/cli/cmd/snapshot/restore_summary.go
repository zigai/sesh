package snapshot

import (
	"fmt"
	"strings"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/snapshot/restore"
)

const shortGenerationID = 8

// writeRestoreSummary writes what a restore did in terms a reader can act on: which sessions
// came back, what happened to their programs, what was skipped and what to do next.
func writeRestoreSummary(out *strings.Builder, res action.RestoredSnapshot) {
	generation := res.Generation
	if len(generation) > shortGenerationID {
		generation = generation[:shortGenerationID]
	}

	switch names := restoredNames(res.Sessions); {
	case len(res.Sessions) == 0:
		fmt.Fprintf(out, "No sessions restored from generation %s.\n", generation)
	case len(names) == len(res.Sessions):
		fmt.Fprintf(out, "Restored %s from generation %s: %s\n", sessionCount(len(res.Sessions)), generation, strings.Join(names, ", "))
	default:
		fmt.Fprintf(out, "Restored %s from generation %s.\n", sessionCount(len(res.Sessions)), generation)
	}

	if len(res.Mismatches) > 0 {
		out.WriteString("The restored layout does not fully match the snapshot; run with --verbose for the differences.\n")
	}

	writeProgramSummary(out, res.Panes)
	writeSkippedSummary(out, res)

	if res.Uncertain && res.OperationID != "" {
		fmt.Fprintf(out, "Some steps may or may not have happened. Continue without repeating them:\n  sesh snapshot restore --generation %s --operation %s\n", res.Generation, res.OperationID)
	}
}

func restoredNames(sessions []action.SessionRef) []string {
	names := make([]string, 0, len(sessions))
	for _, restored := range sessions {
		if restored.Name != "" {
			names = append(names, restored.Name)
		}
	}

	return names
}

func sessionCount(count int) string {
	if count == 1 {
		return "1 session"
	}

	return fmt.Sprintf("%d sessions", count)
}

// tallyPaneOutcomes counts resumed and restarted programs and collects the panes a reader must
// look at: programs not brought back and panes started outside their saved directory.
func tallyPaneOutcomes(panes []action.RestoredPane) (int, int, []action.RestoredPane) {
	var resumed, restarted int

	var attention []action.RestoredPane

	for _, pane := range panes {
		needsAttention := pane.CWDFallback

		switch pane.Outcome {
		case restore.PaneOutcomeNativeResumeDispatch:
			resumed++
		case restore.PaneOutcomeRestartDispatch:
			restarted++
		case restore.PaneOutcomeDefaultShell:
		default:
			needsAttention = true
		}

		if needsAttention {
			attention = append(attention, pane)
		}
	}

	return resumed, restarted, attention
}

func writeProgramSummary(out *strings.Builder, panes []action.RestoredPane) {
	resumed, restarted, attention := tallyPaneOutcomes(panes)
	if resumed+restarted+len(attention) == 0 {
		return
	}

	var parts []string
	if resumed > 0 {
		parts = append(parts, fmt.Sprintf("%d resumed", resumed))
	}

	if restarted > 0 {
		parts = append(parts, fmt.Sprintf("%d restarted", restarted))
	}

	if len(attention) > 0 {
		parts = append(parts, fmt.Sprintf("%d need attention", len(attention)))
	}

	fmt.Fprintf(out, "Programs: %s\n", strings.Join(parts, ", "))

	for _, pane := range attention {
		program := ""
		if pane.Program != "" {
			program = pane.Program + ": "
		}

		fmt.Fprintf(out, "  %s  %s%s\n", pane.Location, program, paneOutcomeAdvice(pane))
	}

	if resumed+restarted > 0 {
		out.WriteString("Resumed and restarted programs were launched, but sesh cannot confirm they recovered; check those panes.\n")
	}
}

// paneOutcomeAdvice explains a pane whose program was not brought back, and what to do about it.
func paneOutcomeAdvice(pane action.RestoredPane) string {
	if pane.Outcome == restore.PaneOutcomeAllowedCWDFallback || pane.CWDFallback {
		return "started in a fallback directory because the saved one no longer exists"
	}

	switch pane.Reason {
	case "executable_deleted":
		return "its program file was replaced while it ran (for example by an upgrade); start it again yourself"
	case "unsafe_arguments":
		return "its command line is not safe to replay; start it again yourself"
	case restore.PaneReasonUnsupportedLaunch, "unsupported_application":
		return "sesh does not know how to restart this program; start it again yourself"
	case restore.PaneReasonEvidenceUnavailable:
		return "its launch details were not captured; start it again yourself"
	case restore.PaneReasonExecutableUnavailable:
		return "its program is no longer installed at the saved path; start it again yourself"
	case "":
		return "left at a shell; start its program again yourself"
	default:
		return "left at a shell (" + pane.Reason + "); start its program again yourself"
	}
}

func writeSkippedSummary(out *strings.Builder, res action.RestoredSnapshot) {
	if len(res.SessionsSkipped) == 0 {
		return
	}

	out.WriteString("Skipped:\n")

	for _, session := range res.SessionsSkipped {
		reason := session.Reason
		if session.Reason == restore.CollisionNameInUse {
			reason = "a live session already uses this name"
		}

		fmt.Fprintf(out, "  %s (%s): %s\n", session.Name, session.ID, reason)
	}

	first := res.SessionsSkipped[0]

	generationFlag := ""
	if res.Generation != "" {
		generationFlag = " --generation " + res.Generation
	}

	fmt.Fprintf(out, "  To restore one, rename the live session (tmux rename-session -t %q <new-name>), then run:\n"+
		"    sesh snapshot restore%s %s\n", first.Name, generationFlag, first.ID)
}

// writeRestoreDetails writes the per-pane evidence and verification state behind the summary.
func writeRestoreDetails(out *strings.Builder, res action.RestoredSnapshot) {
	fmt.Fprintf(out, "Operation:          %s\n", res.OperationID)
	fmt.Fprintf(out, "Generation:         %s\n", res.Generation)
	fmt.Fprintf(out, "Status:             %s\n", res.Status)
	fmt.Fprintf(out, "Reason:             %s\n", res.Reason)
	fmt.Fprintf(out, "Sessions Restored:  %s\n", strings.Join(restoredIDs(res.Sessions), ", "))
	fmt.Fprintf(out, "Verified:           %t\n", res.Verified)
	fmt.Fprintf(out, "Uncertain:          %t\n", res.Uncertain)

	if len(res.Panes) > 0 {
		out.WriteString("Panes:\n")

		for _, pane := range res.Panes {
			fmt.Fprintf(out, "  %s runtime=%q outcome=%s reason=%q app-verified=%t cwd-fallback=%t\n",
				pane.PaneID, pane.RuntimeID, pane.Outcome, pane.Reason, pane.AppVerified, pane.CWDFallback)
		}
	}

	if len(res.MissingRequirements) > 0 {
		fmt.Fprintf(out, "Missing Parity:     %d unverified or observational requirements\n", len(res.MissingRequirements))
	}

	if len(res.SessionsSkipped) > 0 {
		out.WriteString("Skipped Sessions:\n")

		for _, session := range res.SessionsSkipped {
			fmt.Fprintf(out, "  %s %q reason=%s\n", session.ID, session.Name, session.Reason)
		}
	}

	if len(res.Mismatches) > 0 {
		out.WriteString("Differences:\n")

		for _, mismatch := range res.Mismatches {
			fmt.Fprintf(out, "  %s %s reason=%s\n", mismatch.Domain, mismatch.Entity, mismatch.Reason)
		}
	}

	if count := len(res.Advisories); count > 0 {
		fmt.Fprintf(out, "Observed Changes:   %d values differ from the snapshot after launch (for example pane titles that applications rewrite)\n", count)
	}
}

func restoredIDs(sessions []action.SessionRef) []string {
	ids := make([]string, 0, len(sessions))
	for _, restored := range sessions {
		ids = append(ids, restored.ID)
	}

	return ids
}
