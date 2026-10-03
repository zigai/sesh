package autosave

import (
	"fmt"
	"strings"
	"time"

	"github.com/zigai/sesh/internal/automation"
)

const shortGenerationID = 8

// skippedHeadlines say why a save or startup restore did nothing. Reasons without an entry are
// shown by their code.
var skippedHeadlines = map[string]string{
	autosaveReasonLockBusy:             "Skipped: another save or restore is running.",
	autosaveReasonTmuxNotRunning:       "Skipped: tmux is not running.",
	autosaveReasonRestorableSessions:   "Skipped: tmux is not running.",
	autosaveReasonRestorePending:       "Skipped: tmux started after the newest snapshot and its saved sessions are not restored yet.",
	automation.ReasonRestoreDisabled:   "Not restoring: restore_last_on_start is off.",
	automation.ReasonRepositoryMissing: "Nothing to restore: no snapshots saved yet.",
	automation.ReasonPendingOperation:  "Not restoring: an earlier restore is still running or was interrupted.",
	automation.ReasonRecoveryRequired:  "Not restoring: the snapshot repository needs recovery; run sesh doctor.",
	automation.ReasonObservationOnly:   "Not restoring: no finished save to restore from.",
}

// autosaveSummary is the human form of a save, skip or startup restore: one line saying what
// happened, then only what needs the reader's attention. --json keeps every field.
func autosaveSummary(env autosaveEnv, outcome autosaveOutcome) string {
	var out strings.Builder

	out.WriteString(autosaveHeadline(env, outcome))
	out.WriteString("\n")
	out.WriteString(restorableLine(outcome))
	out.WriteString(abandonedLine(outcome.abandoned))

	return out.String()
}

func autosaveHeadline(env autosaveEnv, outcome autosaveOutcome) string {
	if outcome.restore {
		return restoredHeadline(outcome)
	}

	switch outcome.status {
	case autosaveStatusSaved:
		return savedHeadline(outcome)
	case autosaveStatusSkipped:
		if headline, ok := skippedHeadlines[outcome.reason]; ok {
			return headline
		}

		return "Skipped (" + outcome.reason + ")."
	case "not_due":
		return fmt.Sprintf("Not due: last save %s ago, saves run every %s.", shortDuration(env.decision.Elapsed), shortDuration(env.interval))
	case "disabled":
		return "Autosave is off."
	case automation.CompletionUncertain:
		return "The save may or may not have finished; the next save or sesh doctor reconciles it."
	case autosaveStatusDegraded:
		return "Autosave history could not be read."
	default:
		return "Save failed (" + outcome.reason + ")."
	}
}

func savedHeadline(outcome autosaveOutcome) string {
	switch outcome.defaultSession {
	case "":
	case automation.DefaultSessionRenamed:
		return fmt.Sprintf("Renamed the startup session %q to %q.", outcome.defaultFrom, outcome.defaultTo)
	case automation.DefaultSessionSwitched:
		return fmt.Sprintf("Switched from the startup session %q to %q.", outcome.defaultFrom, outcome.defaultTo)
	default:
		return "Default session unchanged."
	}

	if outcome.generation == "" {
		return "Saved."
	}

	return "Saved generation " + shortGeneration(outcome.generation) + "."
}

func restoredHeadline(outcome autosaveOutcome) string {
	headline := fmt.Sprintf("Restored %s from generation %s.", sessionCount(outcome.restored), shortGeneration(outcome.generation))

	switch {
	case outcome.skipped > 0:
		headline += fmt.Sprintf("\nSkipped %s whose names are already in use; sesh snapshot restore shows them and how to restore them.", sessionCount(outcome.skipped))
	case outcome.status != "complete" && outcome.status != "best_effort":
		headline += "\nThe restored sessions do not fully match the snapshot; sesh snapshot restore --verbose shows the differences."
	}

	return headline
}

// autosaveStatusSummary is the human form of autosave status: whether saves run, whether there is
// something to restore and whether saves happen before sleep and shutdown.
func autosaveStatusSummary(env autosaveEnv, outcome autosaveOutcome) string {
	var out strings.Builder

	fmt.Fprintf(&out, "%-17s%s\n", "Autosave:", autosaveScheduleState(env, outcome))
	fmt.Fprintf(&out, "%-17s%s\n", "Snapshots:", snapshotsState(outcome.repository))

	if state := shutdownSaveState(outcome.daemonState); state != "" {
		fmt.Fprintf(&out, "%-17s%s\n", "Before shutdown:", state)
	}

	if state := lastFinalSaveState(outcome.finalSave); state != "" {
		fmt.Fprintf(&out, "%-17s%s\n", "Last final save:", state)
	}

	return out.String()
}

func autosaveScheduleState(env autosaveEnv, outcome autosaveOutcome) string {
	every := "on, every " + shortDuration(env.interval)

	switch {
	case outcome.history == autosaveHistoryUnavailable:
		return "history could not be read; the next save time is unknown"
	case outcome.status == "disabled":
		return "off"
	case outcome.reason == automation.ReasonNeverRun:
		return every + "; no save recorded yet"
	case outcome.status == "due":
		return every + "; last save " + shortDuration(env.decision.Elapsed) + " ago, the next one is due"
	default:
		return every + "; last save " + shortDuration(env.decision.Elapsed) + " ago"
	}
}

func snapshotsState(repository string) string {
	switch repository {
	case autosaveRepositoryReady:
		return "ready to restore"
	case autosaveRepositoryEmpty, autosaveRepositoryMissing:
		return "none saved yet"
	case autosaveRepositoryObservation:
		return "no finished save yet"
	case autosaveRepositoryRecovery:
		return "a restore is unfinished; run sesh doctor"
	default:
		return "could not be read; run sesh doctor"
	}
}

func shutdownSaveState(daemonState string) string {
	switch daemonState {
	case autosaveDaemonReady:
		return "saves run before sleep and shutdown"
	case autosaveDaemonNotReady:
		return "the coordinator runs but cannot delay shutdown yet; run sesh doctor"
	case autosaveDaemonAbsent:
		return "off; sesh-coordinator.service is not running"
	case autosaveDaemonStale:
		return "off; the coordinator stopped without cleaning up"
	case autosaveDaemonUnverifiable:
		return "unknown; run sesh doctor"
	default:
		return ""
	}
}

func lastFinalSaveState(finalSave string) string {
	switch finalSave {
	case automation.FinalSaveSaved:
		return "finished"
	case automation.FinalSaveIncomplete:
		return "cut short; restore uses the newest finished save"
	case automation.FinalSaveFailed:
		return "failed"
	case automation.FinalSaveUncertain:
		return "may or may not have finished"
	default:
		return finalSave
	}
}

func abandonedLine(count int) string {
	if count == 0 {
		return ""
	}

	return fmt.Sprintf("Abandoned %s whose tmux server no longer exists.\n", countNoun(count, "interrupted restore", "interrupted restores"))
}

func restorableLine(outcome autosaveOutcome) string {
	switch {
	case outcome.restorable == 0:
		return ""
	case outcome.reason == autosaveReasonRestorePending:
		return countNoun(outcome.restorable, "saved session is", "saved sessions are") + " not running. Restore with: sesh snapshot restore\n" +
			"Or keep the current state, which resumes autosave: sesh snapshot save\n"
	default:
		return countNoun(outcome.restorable, "saved session", "saved sessions") + " can be restored. Start tmux, then run: sesh snapshot restore\n"
	}
}

func sessionCount(count int) string {
	return countNoun(count, "session", "sessions")
}

func countNoun(count int, singular, plural string) string {
	if count == 1 {
		return "1 " + singular
	}

	return fmt.Sprintf("%d %s", count, plural)
}

func shortGeneration(generation string) string {
	return generation[:min(len(generation), shortGenerationID)]
}

// shortDuration writes a duration the way a reader says it: 3m, 1m20s, 2h5m.
func shortDuration(duration time.Duration) string {
	text := duration.Round(time.Second).String()
	if strings.HasSuffix(text, "m0s") {
		text = strings.TrimSuffix(text, "0s")
	}

	if strings.HasSuffix(text, "h0m") {
		text = strings.TrimSuffix(text, "0m")
	}

	return text
}
