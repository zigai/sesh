package cmddoctor_test

import (
	"testing"

	cmddoctor "github.com/zigai/sesh/internal/cli/cmd/doctor"
)

func TestNewCommand(t *testing.T) {
	cmd := cmddoctor.NewCommand()
	if cmd == nil || cmd.Use != "doctor" {
		t.Fatalf("unexpected doctor command: %#v", cmd)
	}
}
