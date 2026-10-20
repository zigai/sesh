package autosave

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zigai/sesh/internal/automation"
)

const shortGenerationID = 8

var skippedHeadlines = map[string]string{
	reasonLockBusy:                     "Skipped: another save or restore is running.",
	reasonTmuxNotRunning:               "Skipped: tmux is not running.",
	reasonRestorableSessions:           "Skipped: tmux is not running.",
	reasonCaptureChanged:               "Skipped: tmux changed during the save; the next run retries.",
	reasonRestorePending:               "Skipped: tmux started after the newest snapshot and its saved sessions are not restored yet.",
	automation.ReasonRestoreDisabled:   "Not restoring: restore_last_on_start is off.",
	automation.ReasonRepositoryMissing: "Nothing to restore: no snapshots saved yet.",
	automation.ReasonPendingOperation:  "Not restoring: an earlier restore is still running or was interrupted.",
	automation.ReasonRecoveryRequired:  "Not restoring: the snapshot repository needs recovery; run sesh doctor.",
	automation.ReasonObservationOnly:   "Not restoring: no finished save to restore from.",
}

func summary(env Env, outcome outcome) string {
	var out strings.Builder

	out.WriteString(headline(env, outcome))
	out.WriteString("\n")
	out.WriteString(restorableLine(outcome))
	out.WriteString(abandonedLine(outcome.abandoned))

	if outcome.maintenance == maintenanceFailed {
		out.WriteString("Removing old snapshots failed; the sesh log has the cause.\n")
	}

	return out.String()
}

func headline(env Env, outcome outcome) string {
	if outcome.restore != nil {
		return restoredHeadline(outcome.generation, outcome.status, *outcome.restore)
	}

	switch outcome.status {
	case statusSaved:
		return savedHeadline(outcome)
	case statusSkipped:
		if headline, ok := skippedHeadlines[outcome.reason]; ok {
			return headline
		}

		return "Skipped (" + outcome.reason + ")."
	case "not_due":
		return fmt.Sprintf("Not due: last save %s ago, saves run every %s.", shortDuration(env.decision.Elapsed), shortDuration(env.interval))
	case "disabled":
		return "Autosave is off."
	case automation.CompletionUncertain:
		return "The save may or may not have finished; the next save reconciles it."
	case statusDegraded:
		return "Autosave history could not be read."
	default:
		return "Save failed (" + outcome.reason + ")."
	}
}

func savedHeadline(outcome outcome) string {
	if outcome.generation == "" {
		return "Saved."
	}

	return "Saved generation " + shortGeneration(outcome.generation) + "."
}

func restoredHeadline(generation, status string, counts restoreCounts) string {
	headline := fmt.Sprintf("Restored %s from generation %s.", sessionCount(counts.restored), shortGeneration(generation))

	switch {
	case counts.skipped > 0:
		headline += fmt.Sprintf("\nSkipped %s whose names are already in use; sesh snapshot restore shows them and how to restore them.", sessionCount(counts.skipped))
	case status != "complete" && status != "best_effort":
		headline += "\nThe restored sessions do not fully match the snapshot; sesh snapshot restore --verbose shows the differences."
	}

	return headline
}

func statusSummary(env Env, outcome outcome) string {
	var out strings.Builder

	fmt.Fprintf(&out, "%-17s%s\n", "Autosave:", scheduleState(env, outcome))
	fmt.Fprintf(&out, "%-17s%s\n", "Snapshots:", snapshotsState(outcome.repository))

	if state := shutdownSaveState(outcome.daemonState); state != "" {
		fmt.Fprintf(&out, "%-17s%s\n", "Before shutdown:", state)
	}

	if state := lastFinalSaveState(outcome.finalSave, outcome.finalSaveAge); state != "" {
		fmt.Fprintf(&out, "%-17s%s\n", "Last final save:", state)
	}

	return out.String()
}

func scheduleState(env Env, outcome outcome) string {
	every := "on, every " + shortDuration(env.interval)

	switch {
	case outcome.history == historyUnavailable:
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
	case repositoryReady:
		return "ready to restore"
	case repositoryEmpty, repositoryMissing:
		return "none saved yet"
	case repositoryRecovery:
		return "a restore is unfinished; run sesh doctor"
	default:
		return "could not be read; run sesh doctor"
	}
}

func shutdownSaveState(daemonState string) string {
	switch daemonState {
	case daemonReady:
		return "saves run before sleep and shutdown"
	case daemonNotReady:
		return "the coordinator runs but cannot delay shutdown yet; run sesh doctor"
	case daemonAbsent:
		return "off; sesh-coordinator.service is not running"
	case daemonStale:
		return "off; the coordinator stopped without cleaning up"
	case daemonUnverifiable:
		return "unknown; run sesh doctor"
	default:
		return ""
	}
}

func lastFinalSaveState(finalSave string, age time.Duration) string {
	when := ""
	if age > 0 {
		when = " " + shortDuration(age) + " ago"
	}

	switch finalSave {
	case "":
		return ""
	case automation.FinalSaveSaved:
		return "finished" + when
	case automation.FinalSaveIncomplete:
		return "cut short" + when + "; restore uses the newest finished save"
	case automation.FinalSaveFailed:
		return "failed" + when
	case automation.FinalSaveUncertain:
		return "may or may not have finished" + when
	default:
		return finalSave + when
	}
}

func abandonedLine(count int) string {
	if count == 0 {
		return ""
	}

	return fmt.Sprintf("Abandoned %s whose tmux server no longer exists.\n", countNoun(count, "interrupted restore", "interrupted restores"))
}

func restorableLine(outcome outcome) string {
	switch {
	case outcome.restorable == 0:
		return ""
	case outcome.reason == reasonRestorePending:
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

func shortDuration(duration time.Duration) string {
	const day = 24 * time.Hour

	if duration >= day {
		hours := duration.Round(time.Hour)
		text := strconv.FormatInt(int64(hours/day), 10) + "d"

		if rest := hours % day; rest > 0 {
			text += strconv.FormatInt(int64(rest/time.Hour), 10) + "h"
		}

		return text
	}

	text := duration.Round(time.Second).String()
	if strings.HasSuffix(text, "m0s") {
		text = strings.TrimSuffix(text, "0s")
	}

	if strings.HasSuffix(text, "h0m") {
		text = strings.TrimSuffix(text, "0m")
	}

	return text
}
