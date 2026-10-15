package doctor

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var (
	errUnknownCommand = errors.New("unknown command")
	errUnknownFlag    = errors.New("unknown flag")
)

// commandLineValidator reports whether root accepts a command line without running it: the words
// resolve to a runnable command, its positional arguments are valid and every flag is defined for
// it. Flag values are not interpreted.
func commandLineValidator(root *cobra.Command) func(args []string) error {
	return func(args []string) error {
		target, rest, err := root.Find(args)
		if err != nil {
			return fmt.Errorf("%w", err)
		}

		// A missing command explains a command line better than the flags it no longer has.
		positionals, flagErr := commandPositionals(target, rest)

		if target.HasAvailableSubCommands() && len(positionals) > 0 {
			return fmt.Errorf("%w %q for %q", errUnknownCommand, positionals[0], target.CommandPath())
		}

		if !target.Runnable() {
			return fmt.Errorf("%w: %q needs a subcommand", errUnknownCommand, target.CommandPath())
		}

		if flagErr != nil {
			return flagErr
		}

		return target.ValidateArgs(positionals)
	}
}

// commandPositionals returns the positional arguments of a command line, skipping the value of
// each flag that takes one, and the first flag that is not defined for target. An undefined flag
// is assumed to take no value.
func commandPositionals(target *cobra.Command, args []string) ([]string, error) {
	var (
		positionals []string
		flagErr     error
	)

	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			return append(positionals, args[index+1:]...), flagErr
		}

		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positionals = append(positionals, arg)

			continue
		}

		name, _, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")

		flag := lookupFlag(target, name, strings.HasPrefix(arg, "--"))
		if flag == nil {
			if flagErr == nil {
				written, _, _ := strings.Cut(arg, "=")
				flagErr = fmt.Errorf("%w: %s", errUnknownFlag, written)
			}

			continue
		}

		if !inline && flag.NoOptDefVal == "" {
			index++
		}
	}

	return positionals, flagErr
}

func lookupFlag(target *cobra.Command, name string, long bool) *pflag.Flag {
	if name == "help" || (!long && name == "h") {
		return &pflag.Flag{Name: "help", NoOptDefVal: "true"}
	}

	for _, set := range []*pflag.FlagSet{target.Flags(), target.InheritedFlags()} {
		if long {
			if flag := set.Lookup(name); flag != nil {
				return flag
			}

			continue
		}

		if len(name) == 1 {
			if flag := set.ShorthandLookup(name); flag != nil {
				return flag
			}
		}
	}

	return nil
}
