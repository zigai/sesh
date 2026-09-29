package cmddaemon

import (
	"testing"

	"github.com/zigai/sesh/internal/cli/clibase"
)

func TestDaemonTmuxDeclaresLiveOperation(t *testing.T) {
	t.Parallel()

	cmd := newDaemonTmuxCommand()
	if cmd.Annotations[clibase.AnnotationLiveIO] != "true" {
		t.Fatalf("live-io annotation = %q", cmd.Annotations[clibase.AnnotationLiveIO])
	}

	if cmd.Annotations[clibase.AnnotationOperation] != "daemon_serve" {
		t.Fatalf("operation annotation = %q", cmd.Annotations[clibase.AnnotationOperation])
	}
}
