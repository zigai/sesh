package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
	cmdlogs "github.com/zigai/sesh/internal/cli/cmd/logs"
	cmdsession "github.com/zigai/sesh/internal/cli/cmd/session"
	cmdsnapshot "github.com/zigai/sesh/internal/cli/cmd/snapshot"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/errorstep"
	"github.com/zigai/sesh/internal/logging"
)

const maxCommandOutput = 32 << 20

var (
	errCommandOutputLimit = errors.New("command output exceeds limit")

	namedOperations = map[string]bool{
		"verify":  true,
		"list":    true,
		"history": true,
		"prune":   true,
		"gc":      true,
		"doctor":  true,
	}
)

type commandRuntime struct {
	cmd         *cobra.Command
	sink        *logging.Sink
	operation   string
	output      *commandBuffer
	diagnostics *commandBuffer
	started     time.Time
}

type commandResultError = clibase.CommandResultError

type commandBuffer struct {
	buffer    bytes.Buffer
	output    io.Writer
	failed    bool
	streaming bool
}

func (b *commandBuffer) Write(data []byte) (int, error) {
	if b.streaming {
		if b.output == nil {
			return len(data), nil
		}

		n, err := b.output.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}

		return n, commandOutputError(err)
	}

	if len(data) > maxCommandOutput-b.buffer.Len() {
		b.failed = true
		return 0, errCommandOutputLimit
	}

	n, err := b.buffer.Write(data)

	return n, commandOutputError(err)
}

func (b *commandBuffer) Bytes() []byte { return b.buffer.Bytes() }

func (b *commandBuffer) Fd() uintptr {
	if file, ok := b.output.(fileDescriptor); ok {
		return file.Fd()
	}

	return ^uintptr(0)
}

func (b *commandBuffer) Unwrap() io.Writer {
	return b.output
}

func (b *commandBuffer) File() *os.File {
	if file, ok := b.output.(*os.File); ok {
		return file
	}

	return nil
}

func ExecuteApplication(ctx context.Context, args []string, input io.Reader, stdout, stderr io.Writer) error {
	root := newRootCommand(ctx)
	root.SetContext(ctx)
	root.SetArgs(args)
	root.SetIn(input)

	var output, diagnostics commandBuffer

	output.output = stdout
	diagnostics.output = stderr
	root.SetOut(&output)
	root.SetErr(&diagnostics)

	runtime := commandRuntime{cmd: root, sink: nil, operation: "", output: &output, diagnostics: &diagnostics, started: time.Time{}}
	root.PersistentPreRunE = runtime.prepare
	ctx = clibase.ContextWithRawArgs(ctx, args)

	executed, runErr := root.ExecuteContextC(ctx)
	if executed == nil {
		executed = root
	}

	if output.failed || diagnostics.failed {
		runErr = errors.Join(runErr, errCommandOutputLimit)
	}

	closeErr := runtime.finish(runErr)

	err := publishCommandOutput(executed, &output, &diagnostics, stdout, stderr, runErr, closeErr)
	if showBindingFailureMessage(executed, err) {
		return nil
	}

	return err
}

func (r *commandRuntime) prepare(cmd *cobra.Command, args []string) error {
	r.cmd = cmd
	if err := validateFlags(cmd); err != nil {
		return NewExitError(exitCodeUsage, err)
	}

	if cmd.Annotations != nil && cmd.Annotations[clibase.AnnotationStreaming] == "true" {
		return r.prepareStreaming(cmd)
	}

	r.operation = commandOperation(cmd)
	if err := validateCommandBeforeConfig(cmd, args); err != nil {
		return err
	}

	if r.operation != "" && cmd.Annotations[clibase.AnnotationSkipRuntimeSetup] != "true" {
		if err := r.setupLogging(cmd); err != nil {
			return err
		}
	}

	if commandUsesLiveIO(cmd) {
		if r.output != nil {
			r.output.streaming = true
		}

		if r.diagnostics != nil {
			r.diagnostics.streaming = true
		}
	}

	return nil
}

func validateFlags(cmd *cobra.Command) error {
	if err := cmd.ValidateRequiredFlags(); err != nil {
		return err
	}

	if err := cmd.ValidateFlagGroups(); err != nil {
		return err
	}

	if path, _ := cmd.Flags().GetString("config"); path == "" && cmd.Flags().Changed("config") {
		return clibase.ErrConfigPathEmpty
	}

	return nil
}

func commandUsesLiveIO(cmd *cobra.Command) bool {
	if cmd.Annotations[clibase.AnnotationLiveIO] == "true" {
		return true
	}

	if cmd.Parent() == nil || !getBoolFlag(cmd, "pick") {
		return false
	}

	return cmd.Name() == "restore" && cmd.Parent().Name() == "snapshot"
}

func (r *commandRuntime) prepareStreaming(cmd *cobra.Command) error {
	if err := cmdlogs.ValidateLogsCommand(cmd); err != nil {
		return err
	}

	pathFlag := getBoolFlag(cmd, "path")
	clearFlag := getBoolFlag(cmd, "clear")

	if !pathFlag && !clearFlag {
		if r.output != nil {
			r.output.streaming = true
		}

		if r.diagnostics != nil {
			r.diagnostics.streaming = true
		}
	}

	return nil
}

func getBoolFlag(cmd *cobra.Command, name string) bool {
	if cmd == nil || cmd.Flags().Lookup(name) == nil {
		return false
	}

	val, _ := cmd.Flags().GetBool(name)

	return val
}

func (r *commandRuntime) setupLogging(cmd *cobra.Command) error {
	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return errors.Join(err, r.openLog(cmd, config.DefaultConfig(), zerolog.InfoLevel))
	}

	level, err := zerolog.ParseLevel(cfg.Settings.LogLevel)
	if err != nil {
		return oops.In("cli").With("operation", "configure_logging").Wrap(err)
	}

	if level == zerolog.Disabled {
		logger := zerolog.Nop()
		cmd.SetContext(logger.WithContext(cmd.Context()))

		return nil
	}

	if quietAgentTracker(cmd, cfg, r.operation) && level < zerolog.WarnLevel {
		level = zerolog.WarnLevel
	}

	if err := r.openLog(cmd, cfg, level); err != nil {
		return err
	}

	if r.operation != "save" && r.operation != "restore" {
		logger := r.logger()
		logger.Info().Str("component", "cli").Str("operation", r.operation).Str("status", "started").Msg("command started")
	}

	return nil
}

func (r *commandRuntime) openLog(cmd *cobra.Command, cfg *config.Config, level zerolog.Level) error {
	logPath, err := config.GetLogFile(cfg)
	if err != nil {
		return oops.In("cli").With("operation", "configure_logging").Wrap(err)
	}

	logPath, err = filepath.Abs(logPath)
	if err != nil {
		return oops.In("cli").With("operation", "configure_logging").Wrap(err)
	}

	r.sink, err = logging.Open(logPath)
	if err != nil {
		return oops.In("cli").Code("logging_failed").With("operation", "configure_logging").Wrap(fmt.Errorf("%w: %w", clibase.ErrLogFileUnavailable, err))
	}

	r.sink.SetLevel(level)

	logger := r.logger()
	cmd.SetContext(logger.WithContext(cmd.Context()))

	r.started = time.Now()

	return nil
}

func (r *commandRuntime) logger() zerolog.Logger {
	fields := r.sink.Logger().With().Int("pid", os.Getpid()).Str("version", version)
	if r.cmd != nil {
		fields = fields.Str("socket", clibase.ResolveSocket(r.cmd))
		if id := r.cmd.Annotations[clibase.AnnotationAction]; id != "" {
			fields = fields.Str("action", id)
		}
	}

	return fields.Logger()
}

func quietAgentTracker(cmd *cobra.Command, cfg *config.Config, operation string) bool {
	return operation == "agent_status_run" && (getBoolFlag(cmd, "quiet") || cfg.ResolvedAgentStatus().Quiet)
}

func (r *commandRuntime) finish(err error) error {
	if r.sink == nil {
		return nil
	}

	if r.ownsOutcome(err) {
		r.logOutcome(err)
	}

	if closeErr := r.sink.Close(); closeErr != nil {
		return oops.In("cli").Code("logging_failed").With("operation", "finalize_logging").Wrap(closeErr)
	}

	return nil
}

func (r *commandRuntime) logOutcome(err error) {
	duration := time.Since(r.started).Milliseconds()
	logger := r.logger()

	if err == nil {
		logger.Info().Str("component", "cli").Str("operation", r.operation).Str("status", "ok").
			Int64("duration_ms", duration).Msg("command finished")

		return
	}

	if clibase.OnlyErrorCauses(err, context.Canceled) {
		logger.Info().Str("component", "cli").Str("operation", r.operation).Str("status", "canceled").
			Int64("duration_ms", duration).Msg("command canceled")

		return
	}

	event := logger.Error().Str("component", "cli").Str("operation", r.operation).Str("status", "failed").
		Str("reason", clibase.JSONErrorCode(err)).Int64("duration_ms", duration).Func(logging.Cause(err))

	if step, ok := errorstep.Owned(err, "cli"); ok {
		event = event.Str("failed_step", step.Operation)
	}

	if step, ok := errorstep.Owned(err, "snapshot.store"); ok {
		event = event.Str("store_step", step.Operation)
	}

	event.Msg("command failed")
}

func (r *commandRuntime) ownsOutcome(err error) bool {
	if err == nil && r.cmd != nil && r.cmd.Annotations[clibase.AnnotationSuccessOwned] == "true" {
		return false
	}

	return !commandOutcomeLogged(err)
}

func commandOperation(cmd *cobra.Command) string {
	if operation := cmd.Annotations[clibase.AnnotationOperation]; operation != "" {
		return operation
	}

	parent := cmd.Parent()
	if parent == nil || !namedOperations[cmd.Name()] {
		return ""
	}

	if parent.Name() == "snapshot" || parent == cmd.Root() {
		return cmd.Name()
	}

	return ""
}

func validateCommandBeforeConfig(cmd *cobra.Command, args []string) error {
	if err := cmdsnapshot.ValidateSnapshotCommand(cmd, args); err != nil {
		return err
	}

	if cmd.Parent() == nil || cmd.Parent().Name() != "session" || cmd.Parent().Parent() != cmd.Root() {
		return nil
	}

	switch cmd.Name() {
	case "list":
		return cmdsession.ValidateListCommand(cmd, args)
	case "scrollback":
		return cmdsession.ValidateScrollbackCommand(cmd, args)
	default:
		return nil
	}
}

func publishCommandOutput(cmd *cobra.Command, output, diagnostics *commandBuffer, stdout, stderr io.Writer, runErr, closeErr error) error {
	if bindingDiscardsReport(cmd, output) {
		stdout = io.Discard
	}

	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	resultErr := errors.Join(runErr, closeErr)
	if closeErr != nil && clibase.JSONOutputEnabled(cmd) && !output.streaming {
		return publishFinalizationFailure(cmd, output, runErr, closeErr)
	}

	if copyErr := copyStagedBuffers(output, diagnostics, stdout, stderr, runErr); copyErr != nil {
		return errors.Join(resultErr, copyErr)
	}

	if closeErr != nil {
		return errors.Join(resultErr, writeFinalizationErrors(cmd, runErr, closeErr))
	}

	if output.streaming {
		if runErr != nil && !clibase.CommandErrorWasRendered(runErr) {
			return errors.Join(resultErr, writeStreamDiagnosticError(cmd, runErr))
		}

		return resultErr
	}

	return errors.Join(resultErr, clibase.WriteCommandError(cmd, runErr))
}

func copyStagedBuffers(output, diagnostics *commandBuffer, stdout, stderr io.Writer, runErr error) error {
	var outputErr error

	_, hasResult := errors.AsType[*commandResultError](runErr)
	if !output.failed && !output.streaming && (runErr == nil || clibase.CommandErrorWasRendered(runErr) || errors.Is(runErr, clibase.ErrSubcommandRequired) || hasResult) {
		_, outputErr = io.Copy(stdout, bytes.NewReader(output.Bytes()))
	}

	var diagnosticErr error
	if !diagnostics.streaming {
		_, diagnosticErr = io.Copy(stderr, bytes.NewReader(diagnostics.Bytes()))
	}

	if outputErr != nil || diagnosticErr != nil {
		return commandOutputError(errors.Join(outputErr, diagnosticErr))
	}

	return nil
}

func writeStreamDiagnosticError(cmd *cobra.Command, err error) error {
	if err == nil || onlyErrorCauses(err, context.Canceled, io.ErrClosedPipe, syscall.EPIPE) {
		return nil
	}

	msg := clibase.HumanErrorMessage(err)
	if errors.Is(err, context.Canceled) {
		msg = "log operation failed during cancellation"
	}

	_, writeErr := fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", msg)

	return commandOutputError(writeErr)
}

func writeFinalizationErrors(cmd *cobra.Command, runErr, closeErr error) error {
	if cause := clibase.UncertainCause(runErr); cause != nil {
		runErr = cause
	}

	if err := clibase.WriteCommandError(cmd, runErr); err != nil {
		return err
	}

	return clibase.WriteCommandError(cmd, closeErr)
}

func publishFinalizationFailure(cmd *cobra.Command, output *commandBuffer, runErr, closeErr error) error {
	combined := errors.Join(runErr, closeErr)

	diagnostic := closeErr
	if cause := clibase.UncertainCause(runErr); cause != nil {
		diagnostic = cause
	}

	var fields map[string]json.RawMessage
	if !output.failed && json.Unmarshal(output.Bytes(), &fields) == nil && fields["result"] != nil && string(fields["result"]) != "null" {
		writeErr := clibase.WriteJSONResultFailure(cmd, fields["result"], diagnostic)

		return NewExitError(exitCodeGeneral, errors.Join(combined, writeErr))
	}

	return NewExitError(exitCodeGeneral, errors.Join(combined, clibase.WriteCommandError(cmd, diagnostic)))
}
