//go:build linux || darwin

package session

import (
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"
)

func TestSameLiveDeleteTargetRequiresDaemonAndSessionIncarnation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		initialPID int
		currentPID int
		initialID  gotmux.SessionID
		currentID  gotmux.SessionID
		want       bool
	}{
		{name: "same incarnation", initialPID: 41, currentPID: 41, initialID: "$3", currentID: "$3", want: true},
		{name: "daemon replaced", initialPID: 41, currentPID: 42, initialID: "$3", currentID: "$3"},
		{name: "session replaced", initialPID: 41, currentPID: 41, initialID: "$3", currentID: "$4"},
		{name: "daemon identity unavailable", initialPID: 41, currentPID: 0, initialID: "$3", currentID: "$3"},
		{name: "session identity unavailable", initialPID: 41, currentPID: 41, initialID: "$3", currentID: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := sameLiveDeleteTarget(test.initialPID, test.currentPID, test.initialID, test.currentID); got != test.want {
				t.Fatalf("sameLiveDeleteTarget() = %t, want %t", got, test.want)
			}
		})
	}
}
