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

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
	cmdlogs "github.com/zigai/sesh/internal/cli/cmd/logs"
	cmdsession "github.com/zigai/sesh/internal/cli/cmd/session"
	cmdsnapshot "github.com/zigai/sesh/internal/cli/cmd/snapshot"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/logging"
)

const (
	maxCommandOutput         = 32 << 20
	AnnotationLiveIO         = "live-io"
	annotationLiveIO         = AnnotationLiveIO
	annotationOperation      = "operation"
	annotationSuccessOwned   = "success-owned"
	annotationSkipConfigLoad = "skip-config-load"
	annotationOwnedValue     = "true"
)

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
}

type (
	saveBoundaryError  = clibase.SaveBoundaryError
	commandResultError = clibase.CommandResultError
)

type configurationError = clibase.ConfigurationError

type commandConfigKey struct{}

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

// ExecuteApplication runs the sesh command line through the production runtime.
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

	runtime := commandRuntime{cmd: root, sink: nil, operation: "", output: &output, diagnostics: &diagnostics}
	root.PersistentPreRunE = runtime.prepare
	ctx = clibase.ContextWithRawArgs(ctx, args)

	executed, runErr := root.ExecuteContextC(ctx)
	if executed == nil {
		executed = root
	}

	if output.failed || diagnostics.failed {
		runErr = errors.Join(runErr, errCommandOutputLimit)
	}

	closeErr := runtime.finish(ctx, runErr)

	err := publishCommandOutput(executed, &output, &diagnostics, stdout, stderr, runErr, closeErr)
	if reportBindingFailure(executed, err, stdout, stderr) {
		return nil
	}

	return err
}

func (r *commandRuntime) prepare(cmd *cobra.Command, args []string) error {
	r.cmd = cmd
	if err := cmd.ValidateRequiredFlags(); err != nil {
		return NewExitError(exitCodeUsage, err)
	}

	if err := cmd.ValidateFlagGroups(); err != nil {
		return NewExitError(exitCodeUsage, err)
	}

	if cmd.Annotations != nil && cmd.Annotations[clibase.AnnotationStreaming] == "true" {
		return r.prepareStreaming(cmd)
	}

	r.operation = commandOperation(cmd)
	if err := validateCommandBeforeConfig(cmd, args); err != nil {
		return err
	}

	if r.operation != "" && cmd.Annotations[annotationSkipConfigLoad] != "true" {
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

func commandUsesLiveIO(cmd *cobra.Command) bool {
	if cmd.Annotations[clibase.AnnotationLiveIO] == "true" {
		return true
	}

	if cmd.Parent() == nil || !getBoolFlag(cmd, "pick") {
		return false
	}

	switch cmd.Name() {
	case "restore":
		return cmd.Parent().Name() == "snapshot"
	case "snapshot-restore":
		return cmd.Parent().Name() == "action"
	default:
		return false
	}
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
	cfg, err := loadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return err
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
		return oops.In("cli").Code("logging_failed").With("operation", "configure_logging").Wrap(err)
	}

	r.sink.SetLevel(level)
	logger := r.sink.Logger()
	cmd.SetContext(logger.WithContext(cmd.Context()))

	if r.operation != "save" && r.operation != "restore" {
		zerolog.Ctx(cmd.Context()).Info().Str("component", "cli").Str("operation", r.operation).Str("status", "started").Msg("command started")
	}

	return nil
}

func quietAgentTracker(cmd *cobra.Command, cfg *config.Config, operation string) bool {
	return operation == "agent_status_run" && (getBoolFlag(cmd, "quiet") || cfg.ResolvedAgentStatus().Quiet)
}

func loadCommandConfig(ctx context.Context, cmd *cobra.Command) (*config.Config, error) {
	if cfg, ok := ctx.Value(commandConfigKey{}).(*config.Config); ok {
		return cfg.Clone(), nil
	}

	path, _ := cmd.Flags().GetString("config")
	noConfig, _ := cmd.Flags().GetBool("no-config")

	options := []config.LoadOption{config.WithExplicitPath(path), config.WithInput(cmd.InOrStdin())}
	if noConfig {
		options = append(options, config.WithoutConfig())
	}

	cfg, err := config.Load(options...)
	if err != nil {
		return nil, clibase.NewConfigurationError(oops.In("cli").Code("config_failed").With("operation", "load_configuration").Wrap(err))
	}

	cmd.SetContext(context.WithValue(ctx, commandConfigKey{}, cfg.Clone()))

	return cfg, nil
}

func (r *commandRuntime) finish(ctx context.Context, err error) error {
	if r.sink == nil {
		return nil
	}

	logger := r.sink.Logger()
	ctx = logger.WithContext(ctx)

	if r.ownsOutcome(err) {
		if err != nil {
			zerolog.Ctx(ctx).Error().Str("component", "cli").Str("operation", r.operation).Str("status", "failed").Str("reason", jsonErrorCode(err)).Msg("command failed")
		} else {
			zerolog.Ctx(ctx).Info().Str("component", "cli").Str("operation", r.operation).Str("status", "ok").Msg("command finished")
		}
	}

	if closeErr := r.sink.Close(); closeErr != nil {
		return oops.In("cli").Code("logging_failed").With("operation", "finalize_logging").Wrap(closeErr)
	}

	return nil
}

func (r *commandRuntime) ownsOutcome(err error) bool {
	if r.operation == "restore" && restoreOutcomeLogged(err) {
		return false
	}

	if err == nil && r.cmd != nil && r.cmd.Annotations[annotationSuccessOwned] == "true" {
		return false
	}

	if commandOutcomeLogged(err) {
		return false
	}

	if r.operation == "save" {
		_, saveOwned := errors.AsType[*saveBoundaryError](err)
		return err != nil && !saveOwned && !errors.Is(err, errPartialSnapshot)
	}

	return true
}

func commandOperation(cmd *cobra.Command) string {
	if operation := cmd.Annotations[annotationOperation]; operation != "" {
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
	if !output.streaming && !jsonOutputEnabled(cmd) && invokedByTmuxBinding(cmd, stdout, stderr) {
		stdout = io.Discard
	}

	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	resultErr := errors.Join(runErr, closeErr)
	if closeErr != nil && jsonOutputEnabled(cmd) && !output.streaming {
		return publishFinalizationFailure(cmd, output, runErr, closeErr)
	}

	if copyErr := copyStagedBuffers(output, diagnostics, stdout, stderr, runErr); copyErr != nil {
		return errors.Join(resultErr, copyErr)
	}

	if closeErr != nil {
		return errors.Join(resultErr, writeFinalizationErrors(cmd, runErr, closeErr))
	}

	if output.streaming {
		if runErr != nil && !commandErrorWasRendered(runErr) {
			return errors.Join(resultErr, writeStreamDiagnosticError(cmd, runErr))
		}

		return resultErr
	}

	return errors.Join(resultErr, WriteCommandError(cmd, runErr))
}

func copyStagedBuffers(output, diagnostics *commandBuffer, stdout, stderr io.Writer, runErr error) error {
	var outputErr error

	_, hasResult := errors.AsType[*commandResultError](runErr)
	if !output.failed && !output.streaming && (runErr == nil || commandErrorWasRendered(runErr) || errors.Is(runErr, errPartialSnapshot) || errors.Is(runErr, clibase.ErrSubcommandRequired) || hasResult) {
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

	msg := humanErrorMessage(err)
	if errors.Is(err, context.Canceled) {
		msg = "log operation failed during cancellation"
	}

	_, writeErr := fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", msg)

	return commandOutputError(writeErr)
}

func writeFinalizationErrors(cmd *cobra.Command, runErr, closeErr error) error {
	if contract, uncertain := matchCommandError(runErr, uncertainCommandErrors); uncertain {
		runErr = contract.cause
	}

	if err := WriteCommandError(cmd, runErr); err != nil {
		return err
	}

	return WriteCommandError(cmd, closeErr)
}

func publishFinalizationFailure(cmd *cobra.Command, output *commandBuffer, runErr, closeErr error) error {
	combined := errors.Join(runErr, closeErr)

	diagnostic := closeErr
	if contract, uncertain := matchCommandError(runErr, uncertainCommandErrors); uncertain {
		diagnostic = contract.cause
	}

	var fields map[string]json.RawMessage
	if !output.failed && json.Unmarshal(output.Bytes(), &fields) == nil && fields["result"] != nil && string(fields["result"]) != "null" {
		writeErr := writeJSONEnvelope(cmd.OutOrStdout(), jsonEnvelope{
			SchemaVersion: jsonSchemaVersion, Command: jsonCommandName(cmd), OK: false,
			Result: fields["result"], Error: newJSONErrorPayload(diagnostic),
		})

		return NewExitError(exitCodeGeneral, errors.Join(combined, writeErr))
	}

	return NewExitError(exitCodeGeneral, errors.Join(combined, WriteCommandError(cmd, diagnostic)))
}
