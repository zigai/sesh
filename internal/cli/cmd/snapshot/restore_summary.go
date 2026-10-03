package snapshot

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/restore"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const shortGenerationID = 8

// restoreLabels maps the snapshot IDs in a restore result to what a reader recognizes: session
// names, and each pane's location and program. Zero labels fall back to IDs.
type restoreLabels struct {
	sessions     map[string]string
	sessionOrder []string
	panes        map[string]paneLabel
}

type paneLabel struct {
	location string
	program  string
}

func newRestoreLabels(manifest model.Manifest) restoreLabels {
	labels := restoreLabels{
		sessions:     make(map[string]string, len(manifest.Sessions)),
		sessionOrder: make([]string, 0, len(manifest.Sessions)),
		panes:        make(map[string]paneLabel, len(manifest.Panes)),
	}

	for _, session := range manifest.Sessions {
		labels.sessions[session.ID] = string(session.Name)
		labels.sessionOrder = append(labels.sessionOrder, session.ID)
	}

	links := make(map[string]model.Link, len(manifest.Links))
	for _, link := range manifest.Links {
		if _, seen := links[link.Window]; !seen {
			links[link.Window] = link
		}
	}

	for _, pane := range manifest.Panes {
		label := paneLabel{location: pane.ID, program: filepath.Base(string(pane.Launch.Executable))}
		if link, ok := links[pane.Window]; ok {
			label.location = fmt.Sprintf("%s:%d.%d", labels.sessions[link.Session], link.Index, pane.Index)
		}

		if len(pane.Launch.Executable) == 0 {
			label.program = ""
		}

		labels.panes[pane.ID] = label
	}

	return labels
}

// restoredSessionNames names the restored sessions when they can be told apart from the
// requested and skipped ones; otherwise it returns nil and the summary shows a count.
func (l restoreLabels) restoredSessionNames(requested []string, res engine.RestoreResult) []string {
	if len(l.sessionOrder) == 0 {
		return nil
	}

	skipped := make(map[string]bool, len(res.SessionsSkipped))
	for _, session := range res.SessionsSkipped {
		skipped[session.ID] = true
	}

	var names []string

	for _, id := range l.sessionOrder {
		if !skipped[id] && (len(requested) == 0 || slices.Contains(requested, id)) {
			names = append(names, l.sessions[id])
		}
	}

	if len(names) != len(res.SessionsRestored) {
		return nil
	}

	return names
}

// readRestoreLabels labels a restore summary from the restored generation. The labels only make
// the summary readable, so when the manifest cannot be read the summary shows IDs instead.
func readRestoreLabels(ctx context.Context, repo *store.Repository, generation string) restoreLabels {
	manifest, err := repo.Read(ctx, generation)
	if err != nil {
		return restoreLabels{sessions: nil, sessionOrder: nil, panes: nil}
	}

	return newRestoreLabels(manifest)
}

func (l restoreLabels) pane(id string) paneLabel {
	if label, ok := l.panes[id]; ok {
		return label
	}

	return paneLabel{location: id, program: ""}
}

// writeRestoreSummary writes what a restore did in terms a reader can act on: which sessions
// came back, what happened to their programs, what was skipped and what to do next.
func writeRestoreSummary(out *strings.Builder, res engine.RestoreResult, labels restoreLabels, requested []string) {
	generation := res.Generation
	if len(generation) > shortGenerationID {
		generation = generation[:shortGenerationID]
	}

	switch names := labels.restoredSessionNames(requested, res); {
	case len(res.SessionsRestored) == 0:
		fmt.Fprintf(out, "No sessions restored from generation %s.\n", generation)
	case len(names) > 0:
		fmt.Fprintf(out, "Restored %s from generation %s: %s\n", sessionCount(len(res.SessionsRestored)), generation, strings.Join(names, ", "))
	default:
		fmt.Fprintf(out, "Restored %s from generation %s.\n", sessionCount(len(res.SessionsRestored)), generation)
	}

	if len(res.SessionsRestored) > 0 && !res.Verified {
		out.WriteString("The restored layout does not fully match the snapshot; run with --verbose for the differences.\n")
	}

	writeProgramSummary(out, res.Panes, labels)
	writeSkippedSummary(out, res)

	if res.Uncertain && res.OperationID != "" {
		fmt.Fprintf(out, "Some steps may or may not have happened. Continue without repeating them:\n  sesh snapshot restore --generation %s --operation %s\n", res.Generation, res.OperationID)
	}
}

func sessionCount(count int) string {
	if count == 1 {
		return "1 session"
	}

	return fmt.Sprintf("%d sessions", count)
}

// tallyPaneOutcomes counts resumed and restarted programs and collects the panes a reader must
// look at: programs not brought back and panes started outside their saved directory.
func tallyPaneOutcomes(panes []restore.PaneResult) (int, int, []restore.PaneResult) {
	var resumed, restarted int

	var attention []restore.PaneResult

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

func writeProgramSummary(out *strings.Builder, panes []restore.PaneResult, labels restoreLabels) {
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
		label := labels.pane(pane.PaneID)

		program := ""
		if label.program != "" {
			program = label.program + ": "
		}

		fmt.Fprintf(out, "  %s  %s%s\n", label.location, program, paneOutcomeAdvice(pane))
	}

	if resumed+restarted > 0 {
		out.WriteString("Resumed and restarted programs were launched, but sesh cannot confirm they recovered; check those panes.\n")
	}
}

// paneOutcomeAdvice explains a pane whose program was not brought back, and what to do about it.
func paneOutcomeAdvice(pane restore.PaneResult) string {
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

func writeSkippedSummary(out *strings.Builder, res engine.RestoreResult) {
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
		"    sesh snapshot restore%s --session %s\n", first.Name, generationFlag, first.ID)
}

// writeRestoreDetails writes the per-pane evidence and verification state behind the summary.
func writeRestoreDetails(out *strings.Builder, res engine.RestoreResult) {
	fmt.Fprintf(out, "Operation:          %s\n", res.OperationID)
	fmt.Fprintf(out, "Generation:         %s\n", res.Generation)
	fmt.Fprintf(out, "Status:             %s\n", res.Status)
	fmt.Fprintf(out, "Reason:             %s\n", res.Reason)
	fmt.Fprintf(out, "Sessions Restored:  %s\n", strings.Join(res.SessionsRestored, ", "))
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

	if count := len(res.Advisories); count > 0 {
		fmt.Fprintf(out, "Observed Changes:   %d values differ from the snapshot after launch (for example pane titles that applications rewrite)\n", count)
	}
}
