package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/spf13/pflag"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/cli/clibase"

	"github.com/zigai/sesh/internal/integration/process"
	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/capture"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const (
	jsonFlagName      = "json"
	jsonSchemaVersion = 1
)

type jsonEnvelope struct {
	SchemaVersion int               `json:"schemaVersion"`
	Command       string            `json:"command"`
	OK            bool              `json:"ok"`
	Result        any               `json:"result"`
	Error         *jsonErrorPayload `json:"error"`
}

type jsonErrorPayload struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	ExitCode int    `json:"exitCode"`
}
type noopValue struct{}

func (noopValue) String() string {
	return ""
}

func (noopValue) Set(string) error {
	return nil
}

func (noopValue) Type() string {
	return "string"
}

func WriteCommandError(cmd *cobra.Command, err error) error {
	if err == nil || commandErrorWasRendered(err) {
		return nil
	}

	if jsonOutputEnabled(cmd) {
		var stderr io.Writer = os.Stderr
		if cmd != nil {
			stderr = cmd.ErrOrStderr()
		}

		payload := newJSONErrorPayload(err)
		payload.Message = commandErrorMessage(cmd, err)

		return writeJSONEnvelope(stderr, jsonEnvelope{
			SchemaVersion: jsonSchemaVersion,
			Command:       jsonCommandName(cmd),
			OK:            false,
			Result:        nil,
			Error:         payload,
		})
	}

	if onlyErrorCauses(err, context.Canceled) {
		return nil
	}

	var stderr io.Writer = os.Stderr
	if cmd != nil {
		stderr = cmd.ErrOrStderr()
	}

	if _, writeErr := fmt.Fprintln(stderr, humanCommandErrorMessage(cmd, err)); writeErr != nil {
		return oops.In("cli").With("operation", "write_command_error").Wrapf(writeErr, "writing command error")
	}

	return nil
}

// humanErrorMessage is the terminal diagnostic: mapped sentinels keep their wording, other
// causes are reported verbatim so the reader can act on them. JSON envelopes keep the
// sanitized message from safeCommandErrorMessage.
func humanErrorMessage(err error) string {
	if msg, ok := sentinelErrorMessage(err); ok {
		return msg
	}

	if msg := strings.TrimSpace(err.Error()); msg != "" {
		return msg
	}

	return "operation failed"
}

func humanCommandErrorMessage(cmd *cobra.Command, err error) string {
	message := humanErrorMessage(err)
	if cmd == nil || ExitCode(err) != exitCodeUsage {
		return message
	}

	return message + "; run '" + cmd.CommandPath() + " --help' for valid usage"
}

func commandErrorMessage(cmd *cobra.Command, err error) string {
	message := safeCommandErrorMessage(err)
	if cmd == nil || ExitCode(err) != exitCodeUsage {
		return message
	}

	return message + "; run '" + cmd.CommandPath() + " --help' for valid usage"
}

func addJSONFlag(cmd *cobra.Command) {
	if cmd.Flags().Lookup(jsonFlagName) == nil {
		cmd.Flags().Bool(jsonFlagName, false, "Print machine-readable JSON")
	}
}

func jsonOutputEnabled(cmd *cobra.Command) bool {
	if cmd == nil || cmd.Flags().Lookup(jsonFlagName) == nil {
		return false
	}

	enabled, err := cmd.Flags().GetBool(jsonFlagName)
	if err == nil && enabled {
		return true
	}

	if ctx := cmd.Context(); ctx != nil {
		if rawArgs, ok := clibase.RawArgsFromContext(ctx); ok && len(rawArgs) > 0 {
			return probeJSONFlag(cmd.Flags(), rawArgs)
		}
	}

	return false
}

func probeJSONFlag(cmdFlags *pflag.FlagSet, args []string) bool {
	fs := pflag.NewFlagSet("probe", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.ParseErrorsAllowlist.UnknownFlags = true

	var jsonVal bool

	cmdFlags.VisitAll(func(f *pflag.Flag) {
		if f.Name == jsonFlagName {
			fs.BoolVar(&jsonVal, jsonFlagName, false, "")

			return
		}

		fs.AddFlag(&pflag.Flag{
			Name:                f.Name,
			Shorthand:           f.Shorthand,
			Usage:               "",
			Value:               noopValue{},
			DefValue:            f.DefValue,
			NoOptDefVal:         f.NoOptDefVal,
			Deprecated:          "",
			Hidden:              false,
			ShorthandDeprecated: "",
			Annotations:         nil,
		})
	})

	_ = fs.Parse(args)

	return jsonVal
}

func writeJSONResult(cmd *cobra.Command, result any) error {
	return writeJSONEnvelope(cmd.OutOrStdout(), jsonEnvelope{
		SchemaVersion: jsonSchemaVersion,
		Command:       jsonCommandName(cmd),
		OK:            true,
		Result:        result,
		Error:         nil,
	})
}

func writeJSONResultError(cmd *cobra.Command, result any, err error) error {
	if err == nil {
		return writeJSONResult(cmd, result)
	}

	writeErr := writeJSONEnvelope(cmd.OutOrStdout(), jsonEnvelope{
		SchemaVersion: jsonSchemaVersion,
		Command:       jsonCommandName(cmd),
		OK:            false,
		Result:        result,
		Error:         newJSONErrorPayload(err),
	})
	if writeErr != nil {
		return errors.Join(err, writeErr)
	}

	return clibase.NewRenderedCommandError(err)
}

func writeJSONEnvelope(output io.Writer, envelope jsonEnvelope) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")

	if err := encoder.Encode(envelope); err != nil {
		return oops.In("cli").With("operation", "write_json").Wrapf(err, "encoding JSON command output")
	}

	return nil
}

func jsonCommandName(cmd *cobra.Command) string {
	if cmd == nil {
		return "unknown"
	}

	if cmd == cmd.Root() {
		if flag := cmd.Flags().Lookup("version"); flag != nil {
			if showVersion, _ := cmd.Flags().GetBool("version"); showVersion {
				return "version"
			}
		}
	}

	parts := strings.Fields(cmd.CommandPath())
	if len(parts) > 1 {
		parts = parts[1:]
	}

	if len(parts) == 0 {
		return cmd.Name()
	}

	return strings.Join(parts, ".")
}

func newJSONErrorPayload(err error) *jsonErrorPayload {
	return &jsonErrorPayload{
		Code:     jsonErrorCode(err),
		Message:  safeCommandErrorMessage(err),
		ExitCode: ExitCode(err),
	}
}

func safeCommandErrorMessage(err error) string {
	if err == nil {
		return ""
	}

	if msg, ok := sentinelErrorMessage(err); ok {
		return msg
	}

	return cobraErrorMessage(err)
}

func sentinelErrorMessage(err error) (string, bool) {
	if contract, ok := matchCommandError(err, uncertainCommandErrors); ok {
		return contract.message, true
	}

	if msg, ok := storeSentinelMessage(err); ok {
		return msg, true
	}

	if cfgErr, ok := errors.AsType[*configurationError](err); ok {
		return configurationErrorMessage(cfgErr), true
	}

	if msg, ok := captureSentinelMessage(err); ok {
		return msg, true
	}

	if msg, ok := sessionSentinelMessage(err); ok {
		return msg, true
	}

	if contract, ok := matchCommandError(err, workflowCommandErrors); ok {
		return contract.message, true
	}

	return generalSentinelMessage(err)
}

func sessionSentinelMessage(err error) (string, bool) {
	if notFound, ok := errors.AsType[*capture.SessionNotFoundError](err); ok {
		return notFound.Error(), true
	}

	switch {
	case errors.Is(err, gotmux.ErrNoServer):
		return "no tmux server is running on the selected socket; start tmux or pass --socket", true
	case errors.Is(err, session.ErrCurrentSessionAmbiguous):
		return "current pane belongs to multiple sessions", true
	case errors.Is(err, session.ErrCatalogLimitExceeded), errors.Is(err, session.ErrPaneHistoryLimitExceeded):
		return "inventory limit exceeded", true
	case errors.Is(err, session.ErrServerIdentityChanged):
		return "tmux server identity changed during observation", true
	case errors.Is(err, session.ErrInvalidIdentity):
		return "invalid session identity", true
	default:
		return "", false
	}
}

func configurationErrorMessage(err error) string {
	if strictErr, ok := errors.AsType[*toml.StrictMissingError](err); ok && len(strictErr.Errors) > 0 {
		line, column := strictErr.Errors[0].Position()
		return fmt.Sprintf("invalid configuration at line %d, column %d", line, column)
	}

	if decodeErr, ok := errors.AsType[*toml.DecodeError](err); ok {
		line, column := decodeErr.Position()
		return fmt.Sprintf("invalid configuration at line %d, column %d", line, column)
	}

	return "configuration could not be loaded"
}

func storeSentinelMessage(err error) (string, bool) {
	switch {
	case errors.Is(err, store.ErrUncertain):
		return "snapshot publication durability uncertain", true
	case errors.Is(err, errPartialSnapshot):
		return "parity-incomplete save", true
	case errors.Is(err, store.ErrRecoveryRequired):
		return "latest save requires explicit recovery", true
	case errors.Is(err, store.ErrCorrupt):
		return "invalid native repository", true
	case errors.Is(err, store.ErrUnavailable):
		return "snapshot generation unavailable", true
	case errors.Is(err, store.ErrConflict):
		return "repository operation conflict", true
	default:
		return "", false
	}
}

func captureSentinelMessage(err error) (string, bool) {
	switch {
	case errors.Is(err, capture.ErrChanged):
		return "tmux state changed during observation", true
	case errors.Is(err, capture.ErrSelection):
		return "explicit session selection required", true
	case errors.Is(err, capture.ErrUnsupported) || errors.Is(err, process.ErrUnsupported):
		return "state cannot be represented faithfully by published API", true
	case errors.Is(err, integration.ErrEndpoint):
		return "an explicit absolute tmux socket is required", true
	case errors.Is(err, capture.ErrProcessDeleted):
		return "process executable or working directory was deleted", true
	case errors.Is(err, engine.ErrLeaseRootRequired):
		return "explicit shared lease root required", true
	case errors.Is(err, engine.ErrRepositoryRequired):
		return "repository required", true
	case errors.Is(err, model.ErrVersion):
		return "unsupported snapshot schema version", true
	case errors.Is(err, model.ErrInvalid):
		return "invalid snapshot structure", true
	default:
		return "", false
	}
}

func generalSentinelMessage(err error) (string, bool) {
	switch {
	case errors.Is(err, errRepositoryRequired):
		return "--repository is required", true
	case errors.Is(err, errSocketRequired):
		return "--socket is required", true
	case errors.Is(err, errLeaseRootRequired):
		return "--lease-root is required", true
	case errors.Is(err, context.DeadlineExceeded):
		return "operation timed out", true
	case errors.Is(err, context.Canceled):
		return "operation canceled", true
	case errors.Is(err, os.ErrNotExist):
		return "repository does not exist", true
	case errors.Is(err, os.ErrPermission):
		return "permission denied", true
	case errors.Is(err, errUnknownCommand):
		return "unknown command", true
	case errors.Is(err, errHelpShorthand):
		return "-h is not a help flag; use --help", true
	case errors.Is(err, clibase.ErrSubcommandRequired):
		return "subcommand required", true
	default:
		return "", false
	}
}

func cobraErrorMessage(err error) string {
	msg := err.Error()

	// pflag and cobra name only the mistyped flag or command, never a flag value.
	for _, prefix := range []string{"unknown flag: ", "unknown shorthand flag: ", "flag needs an argument: ", "unknown command "} {
		if strings.HasPrefix(msg, prefix) {
			line, _, _ := strings.Cut(msg, "\n")

			return line
		}
	}

	if strings.Contains(msg, "invalid log level: expected trace, debug, info, warn, or error") {
		return "invalid log level; choose trace, debug, info, warn, or error"
	}

	if ExitCode(err) == exitCodeUsage {
		return "invalid command usage"
	}

	return "operation failed"
}

func jsonErrorCode(err error) string {
	if contract, ok := matchCommandError(err, uncertainCommandErrors); ok {
		return contract.code
	}

	if code, ok := storeErrorCode(err); ok {
		return code
	}

	if _, ok := errors.AsType[*configurationError](err); ok {
		return "configurationFailed"
	}

	if code, ok := captureErrorCode(err); ok {
		return code
	}

	if code, ok := sessionErrorCode(err); ok {
		return code
	}

	if contract, ok := matchCommandError(err, workflowCommandErrors); ok {
		return contract.code
	}

	if errors.Is(err, automation.ErrActiveRestores) {
		return "activeRestores"
	}

	return generalErrorCode(err)
}

func sessionErrorCode(err error) (string, bool) {
	if _, ok := errors.AsType[*capture.SessionNotFoundError](err); ok {
		return "sessionNotFound", true
	}

	switch {
	case errors.Is(err, session.ErrCurrentSessionAmbiguous):
		return "currentSessionAmbiguous", true
	case errors.Is(err, session.ErrCatalogLimitExceeded), errors.Is(err, session.ErrPaneHistoryLimitExceeded):
		return "catalogLimitExceeded", true
	case errors.Is(err, session.ErrServerIdentityChanged):
		return "serverChanged", true
	case errors.Is(err, session.ErrInvalidIdentity):
		return "invalidIdentity", true
	default:
		return "", false
	}
}

func storeErrorCode(err error) (string, bool) {
	switch {
	case errors.Is(err, store.ErrUncertain):
		return "publicationUncertain", true
	case errors.Is(err, errPartialSnapshot):
		return "partialSnapshot", true
	case errors.Is(err, store.ErrRecoveryRequired):
		return "recoveryRequired", true
	case errors.Is(err, store.ErrCorrupt):
		return "repositoryCorrupt", true
	case errors.Is(err, store.ErrUnavailable):
		return "snapshotUnavailable", true
	case errors.Is(err, store.ErrConflict):
		return "repositoryConflict", true
	default:
		return "", false
	}
}

func captureErrorCode(err error) (string, bool) {
	switch {
	case errors.Is(err, capture.ErrChanged):
		return "captureChanged", true
	case errors.Is(err, capture.ErrSelection):
		return "selectionRequired", true
	case errors.Is(err, capture.ErrUnsupported) || errors.Is(err, process.ErrUnsupported):
		return "captureUnsupported", true
	case errors.Is(err, integration.ErrEndpoint):
		return "endpointRequired", true
	case errors.Is(err, capture.ErrProcessDeleted):
		return "captureFailed", true
	case errors.Is(err, engine.ErrLeaseRootRequired):
		return "leaseRootRequired", true
	case errors.Is(err, engine.ErrRepositoryRequired):
		return "repositoryRequired", true
	case errors.Is(err, model.ErrVersion):
		return "snapshotVersionUnsupported", true
	case errors.Is(err, model.ErrInvalid):
		return "snapshotInvalid", true
	default:
		return "", false
	}
}

func generalErrorCode(err error) string {
	switch {
	case errors.Is(err, gotmux.ErrNoServer):
		return "tmuxServerUnavailable"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, os.ErrNotExist):
		return "notFound"
	case errors.Is(err, os.ErrPermission):
		return "permissionDenied"
	case errors.Is(err, errUnknownCommand):
		return "unknownCommand"
	case ExitCode(err) == exitCodeUsage:
		return "usageError"
	default:
		return "operationFailed"
	}
}

func commandErrorWasRendered(err error) bool {
	return clibase.CommandErrorWasRendered(err)
}
