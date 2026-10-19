package cli

import (
	"errors"
	"fmt"

	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
)

const (
	exitCodeGeneral = 1
	exitCodeUsage   = 2
	exitCodePipe    = 141
)

type ExitError = clibase.ExitError

type fileDescriptor interface {
	Fd() uintptr
}

func NewExitError(code int, err error) error {
	return clibase.NewExitError(code, err)
}

func ExitCode(err error) int {
	return clibase.ExitCode(err)
}

func onlyErrorCauses(err error, allowed ...error) bool {
	if err == nil {
		return false
	}

	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}

		for _, cause := range causes {
			if !onlyErrorCauses(cause, allowed...) {
				return false
			}
		}

		return true
	}

	if cause := errors.Unwrap(err); cause != nil {
		return onlyErrorCauses(cause, allowed...)
	}

	for _, cause := range allowed {
		if errors.Is(err, cause) {
			return true
		}
	}

	return false
}

func usageArgs(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := validate(cmd, args); err != nil {
			return NewExitError(exitCodeUsage, err)
		}

		return nil
	}
}

func writeOutput(cmd *cobra.Command, format string, args ...any) error {
	_, err := fmt.Fprintf(cmd.OutOrStdout(), format, args...)

	return commandOutputError(err)
}

func commandOutputError(err error) error {
	if err != nil {
		return oops.In("cli").With("operation", "write_output").Wrapf(err, "writing command output")
	}

	return nil
}
