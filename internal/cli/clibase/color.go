package clibase

import (
	"os"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

// ColorOutput reports whether command output may carry ANSI color: CLICOLOR_FORCE forces it,
// NO_COLOR, NOCOLOR and TERM=dumb turn it off, and otherwise stdout must be a terminal.
func ColorOutput(cmd *cobra.Command) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("NOCOLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}

	if force := os.Getenv("CLICOLOR_FORCE"); force != "" && force != "0" {
		return true
	}

	output, ok := cmd.OutOrStdout().(fileDescriptor)

	return ok && term.IsTerminal(output.Fd())
}
