package clibase

import (
	"os"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

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
