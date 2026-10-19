package clibase

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"syscall"

	"github.com/charmbracelet/x/term"
	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/action"
)

const (
	ExitCodeGeneral = 1
	ExitCodeUsage   = 2
	ExitCodePipe    = 141
)

var (
	ErrUnknownCommand     = errors.New("unknown command")
	ErrSocketRequired     = errors.New("--socket is required")
	ErrRepositoryRequired = errors.New("--repository is required")
	ErrLeaseRootRequired  = errors.New("--lease-root is required")
	ErrDoctorChecksFailed = errors.New("doctor checks failed")
	ErrLogFileUnavailable = errors.New("log file cannot be opened")
	ErrSubcommandRequired = errors.New("subcommand required")
	ErrHelpShorthand      = errors.New("-h is not a help flag; use --help")
	RawArgsContextKey     = rawArgsKey{}
)

type rawArgsKey struct{}

type ExitError struct {
	code int
	err  error
}

type fileDescriptor interface {
	Fd() uintptr
}

func (e *ExitError) Error() string {
	return e.err.Error()
}

func (e *ExitError) Unwrap() error {
	return e.err
}

func (e *ExitError) Code() int {
	return e.code
}

func NewExitError(code int, err error) error {
	if err == nil {
		return nil
	}

	if _, ok := oops.AsOops(err); !ok {
		err = oops.In("cli").With("exit_code", code).Wrap(err)
	}

	return &ExitError{code: code, err: err}
}

func UnknownCommandError(cmd *cobra.Command, arg string) error {
	err := oops.In("cli").
		Code("unknown_command").
		With("operation", "validate_args", "command", cmd.CommandPath()).
		Wrap(fmt.Errorf("%w %q for %q", ErrUnknownCommand, arg, cmd.CommandPath()))

	return &ExitError{code: ExitCodeUsage, err: err}
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}

	if IsUncertaintyError(err) {
		return ExitCodeGeneral
	}

	if OnlyErrorCauses(err, io.ErrClosedPipe, syscall.EPIPE) {
		return ExitCodePipe
	}

	if exitErr, ok := errors.AsType[*ExitError](err); ok {
		return exitErr.code
	}

	if action.CodeOf(err) == action.CodeInvalidArgument {
		return ExitCodeUsage
	}

	if errors.Is(err, ErrUnknownCommand) || IsCobraUsageError(err) {
		return ExitCodeUsage
	}

	if OnlyErrorCauses(err, context.Canceled) {
		return action.ExitCodeCanceled
	}

	return ExitCodeGeneral
}

func UsageArgs(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := validate(cmd, args); err != nil {
			oopsErr := oops.In("cli").
				Code("usage_invalid").
				With("operation", "validate_args", "command", cmd.CommandPath()).
				Wrap(err)

			return &ExitError{code: ExitCodeUsage, err: oopsErr}
		}

		return nil
	}
}

func RequireSubcommand(cmd *cobra.Command, _ []string) error {
	if err := cmd.Help(); err != nil {
		return oops.In("cli").With("operation", "show_help").Wrap(err)
	}

	return NewExitError(ExitCodeUsage, ErrSubcommandRequired)
}

func ContextWithRawArgs(ctx context.Context, args []string) context.Context {
	return context.WithValue(ctx, RawArgsContextKey, args)
}

func RawArgsFromContext(ctx context.Context) ([]string, bool) {
	if ctx == nil {
		return nil, false
	}

	args, ok := ctx.Value(RawArgsContextKey).([]string)

	return args, ok
}

func CommandOutputError(err error) error {
	if err != nil {
		return oops.In("cli").With("operation", "write_output").Wrapf(err, "writing command output")
	}

	return nil
}

func CommandOutputWidth(cmd *cobra.Command) int {
	output, ok := cmd.OutOrStdout().(fileDescriptor)
	if !ok {
		return 0
	}

	width, _, err := term.GetSize(output.Fd())
	if err != nil {
		return 0
	}

	return width
}

func WriteLine(cmd *cobra.Command, value string) error {
	_, err := fmt.Fprintln(cmd.OutOrStdout(), value)

	return CommandOutputError(err)
}

func WriteOutput(cmd *cobra.Command, format string, args ...any) error {
	_, err := fmt.Fprintf(cmd.OutOrStdout(), format, args...)

	return CommandOutputError(err)
}

func OnlyErrorCauses(err error, allowed ...error) bool {
	if err == nil {
		return false
	}

	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}

		for _, cause := range causes {
			if !OnlyErrorCauses(cause, allowed...) {
				return false
			}
		}

		return true
	}

	if cause := errors.Unwrap(err); cause != nil {
		return OnlyErrorCauses(cause, allowed...)
	}

	for _, cause := range allowed {
		if errors.Is(err, cause) {
			return true
		}
	}

	return false
}

func IsUncertaintyError(err error) bool {
	if err == nil {
		return false
	}

	for _, contract := range uncertainErrors {
		if errors.Is(err, contract.cause) {
			return true
		}
	}

	if classified, ok := errors.AsType[*action.Error](err); ok && classified.Code == action.CodeUncertain {
		return true
	}

	type uncertainDetector interface {
		error
		Uncertain() bool
	}

	if detector, ok := errors.AsType[uncertainDetector](err); ok && detector.Uncertain() {
		return true
	}

	return false
}

func IsCobraUsageError(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()

	return strings.HasPrefix(msg, "unknown command ") ||
		strings.HasPrefix(msg, "unknown flag: ") ||
		strings.HasPrefix(msg, "unknown shorthand flag: ") ||
		strings.HasPrefix(msg, "flag needs an argument: ") ||
		strings.HasPrefix(msg, "invalid argument ") ||
		strings.Contains(msg, "accepts at most ") ||
		strings.Contains(msg, "accepts at least ") ||
		strings.Contains(msg, "accepts 0 arg(s)") ||
		strings.Contains(msg, "requires at least ")
}
