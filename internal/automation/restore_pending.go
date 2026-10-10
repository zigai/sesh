//go:build linux || darwin

package automation

import "time"

// UnrestoredSessions names the saved sessions a live tmux server lacks while the newest snapshot
// predates that server's start: the state after a reboot or crash before anything was restored.
// Saving then would make the new server's scratch state the newest snapshot and bury the saved
// sessions. A snapshot finished after the server started, such as an explicit save, shows the live
// state was kept on purpose, so it names nothing.
func UnrestoredSessions(serverStarted, snapshotFinished time.Time, saved, live []string) []string {
	// tmux reports its start in whole seconds, so a snapshot finished within the server's start
	// second, as a stop-time save just before a restart is, counts as older than the server.
	if serverStarted.IsZero() || snapshotFinished.IsZero() || snapshotFinished.Truncate(time.Second).After(serverStarted) {
		return nil
	}

	running := make(map[string]bool, len(live))
	for _, name := range live {
		running[name] = true
	}

	var missing []string

	for _, name := range saved {
		if !running[name] {
			missing = append(missing, name)
		}
	}

	return missing
}
