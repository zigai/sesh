package clibase

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const FlagChoicesAnnotation = "choices"

// SetFlagChoices records the values a flag accepts for help and shell completion. The
// command still validates the value itself.
func SetFlagChoices(cmd *cobra.Command, name string, choices ...string) {
	_ = cmd.Flags().SetAnnotation(name, FlagChoicesAnnotation, choices)
	_ = cmd.RegisterFlagCompletionFunc(name, cobra.FixedCompletions(choices, cobra.ShellCompDirectiveNoFileComp))
}

func FlagChoices(flag *pflag.Flag) []string {
	return flag.Annotations[FlagChoicesAnnotation]
}
