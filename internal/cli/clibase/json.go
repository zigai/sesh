package clibase

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

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/remote"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/capture"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/restore"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const (
	JSONFlagName      = "json"
	JSONSchemaVersion = 1
)

var (
	uncertainErrors = []commandErrorContract{
		{store.ErrUncertain, "publicationUncertain", "snapshot publication durability uncertain"},
		{restore.ErrExecutionUncertain, "operationUncertain", "restore effect outcome uncertain"},
		{restore.ErrPriorUncertainty, "priorEffectUncertain", "previous attempt left an uncertain effect; explicit reconciliation required"},
		{remote.ErrActionUncertain, "remoteOutcomeUncertain", "remote action outcome is uncertain; reconcile the original invocation"},
	}

	workflowErrors = []commandErrorContract{
		{ErrDoctorChecksFailed, "doctorChecksFailed", "doctor checks failed"},
		{engine.ErrRetentionBudgetUnmet, "retentionBudgetUnmet", "protected snapshot bytes exceed retention budget"},
		{engine.ErrCollectionQuarantined, "collectionQuarantined", "snapshot collection preserved contradictory evidence"},
		{gotmux.ErrOutputLimit, "outputLimitExceeded", "tmux command output exceeded its byte limit"},
		{config.ErrConfigExists, "configExists", "configuration already exists; use --force to replace it"},
		{config.ErrConfigSymlink, "configSymlink", "configuration target must not be a symbolic link"},
		{ErrPartialSnapshot, "partialSnapshot", "parity-incomplete save"},
		{ErrPartialRestore, "partialRestore", "parity-incomplete restore"},
		{remote.ErrActionPartial, "partialRemoteAction", "remote action completed with incomplete recovery"},
		{remote.ErrActionFailed, "remoteActionFailed", "remote action failed"},
		{restore.ErrPriorConflict, "priorEffectConflicted", "previous restore effects conflict with recorded state; explicit reconciliation required"},
		{restore.ErrJournalRequired, "journalUnavailable", "restore journal unavailable"},
		{restore.ErrJournalIncomplete, "journalUnavailable", "restore journal unavailable"},
		{session.ErrNotInsideTmux, "notInsideTmux", "switch requires an attached tmux client"},
		{session.ErrInvokingClientUnavailable, "clientUnavailable", "no attached client displays the invoking session"},
		{session.ErrInvokingClientAmbiguous, "clientAmbiguous", "multiple attached clients display the invoking session"},
		{session.ErrSwitchUnverified, "switchUnverified", "client switch could not be verified"},
		{session.ErrTerminalRequired, "interactiveTerminalRequired", "attach requires an interactive terminal"},
		{session.ErrSessionNotFound, "notFound", "session not found"},
		{session.ErrSessionAlreadyExists, "sessionAlreadyExists", "session already exists"},
		{session.ErrSessionAttached, "sessionAttached", "session is attached"},
		{session.ErrSessionReplaced, "sessionReplaced", "session changed before the operation could finish"},
		{session.ErrSavedGenerationsProtected, "savedGenerationsProtected", "saved generations containing the session are protected"},
		{session.ErrSavedSessionScopeConflict, "savedSessionScopeConflict", "saved session source endpoint is unverified"},
		{agents.ErrRegistrySchema, "agentRegistrySchema", "agent registry schema is not supported"},
		{agents.ErrRegistryCorrupt, "agentRegistryCorrupt", "agent registry is corrupt"},
		{agents.ErrRegistryUnavailable, "agentRegistryUnavailable", "agent registry is unavailable"},
		{agents.ErrRegistryTooLarge, "agentRegistryTooLarge", "agent registry exceeds its size limit"},
		{agents.ErrStyleOwnershipCorrupt, "agentStyleOwnershipCorrupt", "agent style ownership record is corrupt"},
		{agents.ErrCurrentContextUnknown, "agentContextUnknown", "current tmux context could not be determined; pass --pane, --client, or --session"},
		{agents.ErrCurrentPaneNotLive, "agentPaneNotLive", "agent pane is not a live tmux pane"},
		{agents.ErrPaneNotLive, "agentPaneNotLive", "agent pane is not a live tmux pane"},
		{agents.ErrNoTargetPane, "agentPaneNotFound", "no agent pane needs attention"},
		{agents.ErrEmptyNavigationHistory, "agentHistoryEmpty", "no agent navigation history"},
	}
)

type JSONEnvelope struct {
	SchemaVersion int               `json:"schemaVersion"`
	Command       string            `json:"command"`
	OK            bool              `json:"ok"`
	Result        any               `json:"result"`
	Error         *JSONErrorPayload `json:"error"`
}

type JSONErrorPayload struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	ExitCode int    `json:"exitCode"`
}

type RenderedCommandError struct {
	err error
}
type commandErrorContract struct {
	cause   error
	code    string
	message string
}

type noopValue struct{}

func (e *RenderedCommandError) Error() string {
	return e.err.Error()
}

func (e *RenderedCommandError) Unwrap() error {
	return e.err
}

func (e *RenderedCommandError) RenderedCommandError() bool {
	return true
}

func NewRenderedCommandError(err error) error {
	if err == nil {
		return nil
	}

	return &RenderedCommandError{err: err}
}

func (noopValue) String() string   { return "" }
func (noopValue) Set(string) error { return nil }
func (noopValue) Type() string     { return "string" }

func AddJSONFlag(cmd *cobra.Command) {
	if cmd.Flags().Lookup(JSONFlagName) == nil {
		cmd.Flags().Bool(JSONFlagName, false, "Print machine-readable JSON")
	}
}

func CommandErrorWasRendered(err error) bool {
	if _, ok := errors.AsType[*RenderedCommandError](err); ok {
		return true
	}

	var marker interface{ RenderedCommandError() bool }
	if errors.As(err, &marker) && marker.RenderedCommandError() {
		return true
	}

	return false
}

func JSONCommandName(cmd *cobra.Command) string {
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

func JSONErrorCode(err error) string {
	if contract, ok := matchErrorContract(err, uncertainErrors); ok {
		return contract.code
	}

	if code, ok := storeErrorCode(err); ok {
		return code
	}

	if _, ok := errors.AsType[*ConfigurationError](err); ok {
		return "configurationFailed"
	}

	if _, ok := errors.AsType[*toml.StrictMissingError](err); ok {
		return "configurationFailed"
	}

	if _, ok := errors.AsType[*toml.DecodeError](err); ok {
		return "configurationFailed"
	}

	if code, ok := captureErrorCode(err); ok {
		return code
	}

	if code, ok := sessionErrorCode(err); ok {
		return code
	}

	if contract, ok := matchErrorContract(err, workflowErrors); ok {
		return contract.code
	}

	return generalErrorCode(err)
}

func JSONOutputEnabled(cmd *cobra.Command) bool {
	if cmd == nil || cmd.Flags().Lookup(JSONFlagName) == nil {
		return false
	}

	enabled, err := cmd.Flags().GetBool(JSONFlagName)
	if err == nil && enabled {
		return true
	}

	if ctx := cmd.Context(); ctx != nil {
		if rawArgs, ok := RawArgsFromContext(ctx); ok && len(rawArgs) > 0 {
			return ProbeJSONFlag(cmd.Flags(), rawArgs)
		}
	}

	return false
}

func NewJSONErrorPayload(err error) *JSONErrorPayload {
	return &JSONErrorPayload{
		Code:     JSONErrorCode(err),
		Message:  SafeCommandErrorMessage(err),
		ExitCode: ExitCode(err),
	}
}

func ProbeJSONFlag(cmdFlags *pflag.FlagSet, args []string) bool {
	fs := pflag.NewFlagSet("probe", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.ParseErrorsAllowlist.UnknownFlags = true

	var jsonVal bool

	cmdFlags.VisitAll(func(f *pflag.Flag) {
		if f.Name == JSONFlagName {
			fs.BoolVar(&jsonVal, JSONFlagName, false, "")

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

func SafeCommandErrorMessage(err error) string {
	if err == nil {
		return ""
	}

	if msg, ok := sentinelErrorMessage(err); ok {
		return msg
	}

	return cobraErrorMessage(err)
}

func WriteCommandError(cmd *cobra.Command, err error) error {
	if err == nil || CommandErrorWasRendered(err) {
		return nil
	}

	if JSONOutputEnabled(cmd) {
		var stderr io.Writer = os.Stderr
		if cmd != nil {
			stderr = cmd.ErrOrStderr()
		}

		payload := NewJSONErrorPayload(err)
		payload.Message = commandErrorMessage(cmd, err)

		return writeJSONEnvelope(stderr, JSONEnvelope{
			SchemaVersion: JSONSchemaVersion,
			Command:       JSONCommandName(cmd),
			OK:            false,
			Result:        nil,
			Error:         payload,
		})
	}

	if OnlyErrorCauses(err, context.Canceled) {
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
// sanitized message from SafeCommandErrorMessage.
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
	if cmd == nil || ExitCode(err) != ExitCodeUsage {
		return message
	}

	return message + "; run '" + cmd.CommandPath() + " --help' for valid usage"
}

func commandErrorMessage(cmd *cobra.Command, err error) string {
	message := SafeCommandErrorMessage(err)
	if cmd == nil || ExitCode(err) != ExitCodeUsage {
		return message
	}

	return message + "; run '" + cmd.CommandPath() + " --help' for valid usage"
}

func WriteJSONResult(cmd *cobra.Command, result any) error {
	return writeJSONEnvelope(cmd.OutOrStdout(), JSONEnvelope{
		SchemaVersion: JSONSchemaVersion,
		Command:       JSONCommandName(cmd),
		OK:            true,
		Result:        result,
		Error:         nil,
	})
}

func WriteJSONResultError(cmd *cobra.Command, result any, err error) error {
	if err == nil {
		return WriteJSONResult(cmd, result)
	}

	writeErr := WriteJSONResultFailure(cmd, result, err)
	if writeErr != nil {
		return errors.Join(err, writeErr)
	}

	return &RenderedCommandError{err: err}
}

func WriteJSONResultFailure(cmd *cobra.Command, result any, err error) error {
	return writeJSONEnvelope(cmd.OutOrStdout(), JSONEnvelope{
		SchemaVersion: JSONSchemaVersion,
		Command:       JSONCommandName(cmd),
		OK:            false,
		Result:        result,
		Error:         NewJSONErrorPayload(err),
	})
}

func captureErrorCode(err error) (string, bool) {
	switch {
	case errors.Is(err, capture.ErrChanged):
		return "captureChanged", true
	case errors.Is(err, capture.ErrSelection):
		return "selectionRequired", true
	case errors.Is(err, capture.ErrUnsupported) || errors.Is(err, process.ErrUnsupported):
		return "captureUnsupported", true
	case errors.Is(err, tmux.ErrEndpoint):
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

func captureSentinelMessage(err error) (string, bool) {
	switch {
	case errors.Is(err, capture.ErrChanged):
		return "tmux state changed during observation", true
	case errors.Is(err, capture.ErrSelection):
		return "explicit session selection required", true
	case errors.Is(err, capture.ErrUnsupported) || errors.Is(err, process.ErrUnsupported):
		return "state cannot be represented faithfully by published API", true
	case errors.Is(err, tmux.ErrEndpoint):
		return "explicit absolute tmux socket is required", true
	case errors.Is(err, capture.ErrProcessDeleted):
		return "process state changed during capture", true
	case errors.Is(err, engine.ErrLeaseRootRequired):
		return "--lease-root is required", true
	case errors.Is(err, engine.ErrRepositoryRequired):
		return "--repository is required", true
	case errors.Is(err, model.ErrVersion):
		return "snapshot version is not supported", true
	case errors.Is(err, model.ErrInvalid):
		return "snapshot payload invalid", true
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

	if ExitCode(err) == ExitCodeUsage {
		return "invalid command usage"
	}

	return "operation failed"
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
	case errors.Is(err, ErrUnknownCommand):
		return "unknownCommand"
	case ExitCode(err) == ExitCodeUsage:
		return "usageError"
	default:
		return "operationFailed"
	}
}

func generalSentinelMessage(err error) (string, bool) {
	switch {
	case errors.Is(err, gotmux.ErrNoServer):
		return "no tmux server is running on the selected socket; start tmux or pass --socket", true
	case errors.Is(err, ErrRepositoryRequired):
		return "--repository is required", true
	case errors.Is(err, ErrSocketRequired):
		return "--socket is required", true
	case errors.Is(err, ErrLeaseRootRequired):
		return "--lease-root is required", true
	case errors.Is(err, context.DeadlineExceeded):
		return "operation timed out", true
	case errors.Is(err, context.Canceled):
		return "operation canceled", true
	case errors.Is(err, os.ErrNotExist):
		return "repository does not exist", true
	case errors.Is(err, os.ErrPermission):
		return "permission denied", true
	case errors.Is(err, ErrUnknownCommand):
		return "unknown command", true
	case errors.Is(err, ErrSubcommandRequired):
		return "subcommand required", true
	default:
		return "", false
	}
}

func matchErrorContract(err error, contracts []commandErrorContract) (commandErrorContract, bool) {
	for _, contract := range contracts {
		if errors.Is(err, contract.cause) {
			return contract, true
		}
	}

	return commandErrorContract{cause: nil, code: "", message: ""}, false
}

func sessionErrorCode(err error) (string, bool) {
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

func sessionSentinelMessage(err error) (string, bool) {
	switch {
	case errors.Is(err, session.ErrCurrentSessionAmbiguous):
		return "current pane belongs to multiple sessions", true
	case errors.Is(err, session.ErrCatalogLimitExceeded), errors.Is(err, session.ErrPaneHistoryLimitExceeded):
		return "inventory limit exceeded", true
	case errors.Is(err, session.ErrServerIdentityChanged):
		return "server identity changed", true
	case errors.Is(err, session.ErrInvalidIdentity):
		return "invalid identity", true
	default:
		return "", false
	}
}

func storeErrorCode(err error) (string, bool) {
	switch {
	case errors.Is(err, store.ErrUncertain):
		return "publicationUncertain", true
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

func storeSentinelMessage(err error) (string, bool) {
	switch {
	case errors.Is(err, store.ErrUncertain):
		return "snapshot publication durability uncertain", true
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

func writeJSONEnvelope(output io.Writer, envelope JSONEnvelope) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")

	if err := encoder.Encode(envelope); err != nil {
		return oops.In("cli").With("operation", "write_json").Wrapf(err, "encoding JSON command output")
	}

	return nil
}

func sentinelErrorMessage(err error) (string, bool) {
	if contract, ok := matchErrorContract(err, uncertainErrors); ok {
		return contract.message, true
	}

	if msg, ok := storeSentinelMessage(err); ok {
		return msg, true
	}

	if cfgErr, ok := errors.AsType[*ConfigurationError](err); ok {
		return configurationErrorMessage(cfgErr.err), true
	}

	if strictErr, ok := errors.AsType[*toml.StrictMissingError](err); ok {
		return configurationErrorMessage(strictErr), true
	}

	if decodeErr, ok := errors.AsType[*toml.DecodeError](err); ok {
		return configurationErrorMessage(decodeErr), true
	}

	if msg, ok := captureSentinelMessage(err); ok {
		return msg, true
	}

	if msg, ok := sessionSentinelMessage(err); ok {
		return msg, true
	}

	if contract, ok := matchErrorContract(err, workflowErrors); ok {
		return contract.message, true
	}

	return generalSentinelMessage(err)
}
