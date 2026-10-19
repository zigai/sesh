//go:build linux || darwin

package automation

import "time"

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
