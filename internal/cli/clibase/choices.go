package clibase

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const FlagChoicesAnnotation = "choices"

// SetFlagChoices only feeds help and shell completion; the command still validates the value.
func SetFlagChoices(cmd *cobra.Command, name string, choices ...string) {
	_ = cmd.Flags().SetAnnotation(name, FlagChoicesAnnotation, choices)
	_ = cmd.RegisterFlagCompletionFunc(name, cobra.FixedCompletions(choices, cobra.ShellCompDirectiveNoFileComp))
}

func FlagChoices(flag *pflag.Flag) []string {
	return flag.Annotations[FlagChoicesAnnotation]
}
