package apps

import (
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/zigai/aht/v2/pkg/aht"
	"github.com/zigai/aht/v2/pkg/harness"

	"github.com/zigai/sesh/internal/snapshot/model"
)

const (
	RecoveryRestart    = model.RecoveryRestart
	RecoveryResume     = model.RecoveryResume
	RecoveryUnverified = model.RecoveryUnverified

	ReasonShellRestart       Reason = "shell_restart"
	ReasonEditorRestart      Reason = "editor_restart"
	ReasonEditorReopen       Reason = "editor_reopen"
	ReasonMonitorRestart     Reason = "monitor_restart"
	ReasonAgentResume        Reason = "agent_resume"
	ReasonPagerRestart       Reason = "pager_restart"
	ReasonGitTUIRestart      Reason = "git_tui_restart"
	ReasonFileManagerRestart Reason = "file_manager_restart"
	ReasonTaskPlaceholder    Reason = "task_placeholder"

	ReasonUnsupportedApplication Reason = Reason(model.ReasonUnsupportedApplication)
	ReasonUnsafeArguments        Reason = Reason(model.ReasonUnsafeArguments)
	ReasonAgentMetadataMissing   Reason = Reason(model.ReasonAgentMetadataMissing)
	ReasonAgentMetadataMismatch  Reason = Reason(model.ReasonAgentMetadataMismatch)
	ReasonAgentExecutableInvalid Reason = Reason(model.ReasonAgentExecutableInvalid)
	ReasonEditorEndpointMissing  Reason = "editor_endpoint_missing"
	ReasonEditorIdentityChanged  Reason = "editor_identity_changed"
	ReasonEditorQueryFailed      Reason = "editor_query_failed"
	ReasonEditorResponseInvalid  Reason = "editor_response_invalid"

	maxApplicationArguments = 32
	maxArgumentBytes        = 4096
	maxTotalArgumentBytes   = 65536
	minimumEditorArguments  = 2
)

var agentArgumentPrefixes = map[aht.Harness][]string{
	aht.HarnessClaude:   {"--resume"},
	aht.HarnessCodex:    {"resume"},
	aht.HarnessCursor:   {"--resume"},
	aht.HarnessCopilot:  {"--resume"},
	aht.HarnessCline:    {"--id"},
	aht.HarnessKimiCode: {"--session"},
	aht.HarnessGrok:     {"--resume"},
	aht.HarnessGoose:    {"session", "--resume", "--session-id"},
	aht.HarnessPi:       {"--session"},
	aht.HarnessOmp:      {"--session"},
	aht.HarnessOpenCode: {"--session"},
	aht.HarnessAgy:      {"--conversation"},
	aht.HarnessKilo:     {"--session"},
	aht.HarnessDroid:    {"--resume"},
	aht.HarnessOpenClaw: nil,
	aht.HarnessHermes:   {"--resume"},
}

var agentStopSignals = map[aht.Harness]syscall.Signal{
	aht.HarnessClaude: syscall.SIGTERM,
	aht.HarnessCodex:  syscall.SIGTERM,
	aht.HarnessOmp:    syscall.SIGTERM,
	aht.HarnessPi:     syscall.SIGTERM,
}

type Reason string

type AgentResume struct {
	Harness     string
	SessionID   string
	SessionPath string
	Command     []string
	Executable  string
}

func (r Reason) Supported() bool {
	return r == ReasonShellRestart || r == ReasonEditorRestart || r == ReasonEditorReopen ||
		r == ReasonMonitorRestart || r == ReasonAgentResume ||
		r == ReasonPagerRestart || r == ReasonGitTUIRestart || r == ReasonFileManagerRestart ||
		r == ReasonTaskPlaceholder
}

func Prepare(observed model.Launch, agent *AgentResume) (model.Launch, Reason) {
	fallback := cloneLaunch(observed)
	fallback.Recovery = RecoveryUnverified
	fallback.Reason = ""

	if sleepingTaskPlaceholder(observed) {
		return preparePlaceholder(fallback)
	}

	executable := string(observed.Executable)
	clean := cleanAbsolute(executable) && cleanAbsolute(string(observed.Directory))
	name := executableName(executable)

	if agent != nil {
		return prepareAgent(fallback, agent)
	}

	if !clean {
		return fallback, ReasonUnsupportedApplication
	}

	return prepareApplication(observed, fallback, name)
}

func RunsHarness(launch model.Launch, harnessName string) bool {
	observed, ok := observedAgentHarness(launch)

	return ok && observed == aht.Harness(harnessName)
}

func StopSignal(harnessName string) (syscall.Signal, bool) {
	// Only harnesses whose sleep was verified with a real stop and native resume have a signal.
	signal, ok := agentStopSignals[aht.Harness(harnessName)]

	return signal, ok
}

func prepareApplication(observed, fallback model.Launch, name string) (model.Launch, Reason) {
	switch {
	case isShell(name):
		return restartLaunch(observed, []string{name}), ReasonShellRestart
	case isMonitor(name):
		return prepareMonitor(fallback, name)
	case isEditor(name):
		return prepareEditor(fallback, name)
	case isPager(name):
		return preparePager(fallback, name)
	case isGitTUI(name):
		return prepareGitTUI(fallback, name)
	case isFileManager(name):
		return prepareFileManager(fallback, name)
	default:
		return fallback, ReasonUnsupportedApplication
	}
}

func prepareEditor(fallback model.Launch, name string) (model.Launch, Reason) {
	files, reason := editorFiles(fallback.Arguments)
	if !reason.Supported() {
		return fallback, reason
	}

	if reason == ReasonEditorRestart {
		return restartLaunch(fallback, []string{name}), reason
	}

	argv := append([]string{name, "--"}, files...)

	return restartLaunch(fallback, argv), ReasonEditorReopen
}

func prepareAgent(observed model.Launch, agent *AgentResume) (model.Launch, Reason) {
	harnessID, reason := validateAgentMetadata(agent)
	if reason != "" {
		return observed, reason
	}

	observedHarness, ok := observedAgentHarness(observed)
	if !ok || observedHarness != harnessID {
		return observed, ReasonAgentMetadataMismatch
	}

	executable, arguments, ok := agentInvocation(observed, agent, harnessID)
	if !ok {
		return observed, ReasonAgentExecutableInvalid
	}

	return model.Launch{
		Executable:  model.Bytes(executable),
		Arguments:   stringArguments(arguments),
		Directory:   slices.Clone(observed.Directory),
		Environment: cloneVariables(observed.Environment),
		Birth:       slices.Clone(observed.Birth),
		Recovery:    RecoveryResume,
		Reason:      "",
	}, ReasonAgentResume
}

func validateAgentMetadata(agent *AgentResume) (aht.Harness, Reason) {
	if agent.Harness == "" || len(agent.Command) == 0 {
		return "", ReasonAgentMetadataMissing
	}

	if containsNULStrings(agent.Command) {
		return "", ReasonAgentExecutableInvalid
	}

	harnessID := aht.Harness(agent.Harness)

	commandHarness, ok := harness.FromCommand(agent.Command[0])
	if !ok || commandHarness != harnessID {
		return "", ReasonAgentMetadataMismatch
	}

	if agent.Executable != "" {
		executableHarness, ok := harness.FromCommand(agent.Executable)
		if !cleanAbsolute(agent.Executable) || !ok || executableHarness != harnessID {
			return "", ReasonAgentExecutableInvalid
		}
	}

	if !validAgentCommand(harnessID, agent) {
		return "", ReasonAgentMetadataMismatch
	}

	return harnessID, ""
}

func agentInvocation(observed model.Launch, agent *AgentResume, harnessID aht.Harness) (string, []string, bool) {
	if agent.Executable != "" {
		return agent.Executable, slices.Clone(agent.Command), true
	}

	executable := string(observed.Executable)
	if !cleanAbsolute(executable) {
		return "", nil, false
	}

	if directHarness, ok := harness.FromCommand(executable); ok && directHarness == harnessID {
		return executable, slices.Clone(agent.Command), true
	}

	if !isScriptInterpreter(executableName(executable)) || len(observed.Arguments) < minimumEditorArguments {
		return "", nil, false
	}

	script := string(observed.Arguments[1])
	if !cleanAbsolute(script) || scriptHarness(script) != harnessID {
		return "", nil, false
	}

	arguments := make([]string, 0, len(agent.Command)+1)
	arguments = append(arguments, executableName(executable), script)
	arguments = append(arguments, agent.Command[1:]...)

	return executable, arguments, true
}

func validAgentCommand(harnessID aht.Harness, agent *AgentResume) bool {
	reference := agent.SessionID
	if (harnessID == aht.HarnessPi || harnessID == aht.HarnessOmp) && agent.SessionPath != "" {
		reference = agent.SessionPath
	}

	if reference == "" || strings.IndexByte(reference, 0) >= 0 {
		return false
	}

	prefix, ok := agentArgumentPrefixes[harnessID]
	if !ok || len(prefix) == 0 {
		return false
	}

	arguments := append(slices.Clone(prefix), reference)

	return slices.Equal(agent.Command[1:], arguments)
}

func restartLaunch(observed model.Launch, argv []string) model.Launch {
	return model.Launch{
		Executable:  slices.Clone(observed.Executable),
		Arguments:   stringArguments(argv),
		Directory:   slices.Clone(observed.Directory),
		Environment: cloneVariables(observed.Environment),
		Birth:       slices.Clone(observed.Birth),
		Recovery:    RecoveryRestart,
		Reason:      "",
	}
}

func editorFiles(arguments []model.Bytes) ([]string, Reason) {
	if len(arguments) == 0 || len(arguments) > maxApplicationArguments {
		return nil, ReasonUnsafeArguments
	}

	if len(arguments) < minimumEditorArguments {
		return nil, ReasonEditorRestart
	}

	files := make([]string, 0, len(arguments)-1)
	endOfOptions := false
	totalBytes := 0

	for _, raw := range arguments[1:] {
		file, isEnd, ok := parseEditorFileArg(raw, endOfOptions, &totalBytes)
		if !ok {
			return nil, ReasonUnsafeArguments
		}

		if isEnd {
			endOfOptions = true

			continue
		}

		files = append(files, file)
	}

	if len(files) == 0 {
		return nil, ReasonEditorRestart
	}

	return files, ReasonEditorReopen
}

func parseEditorFileArg(raw model.Bytes, endOfOptions bool, totalBytes *int) (string, bool, bool) {
	if len(raw) == 0 || len(raw) > maxArgumentBytes {
		return "", false, false
	}

	*totalBytes += len(raw)
	if *totalBytes > maxTotalArgumentBytes {
		return "", false, false
	}

	arg := string(raw)
	if strings.IndexByte(arg, 0) >= 0 || !isSafePath(arg) {
		return "", false, false
	}

	if !endOfOptions && arg == "--" {
		return "", true, true
	}

	if !endOfOptions && (strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "+")) {
		return "", false, false
	}

	return arg, false, true
}

func stringArguments(arguments []string) []model.Bytes {
	result := make([]model.Bytes, len(arguments))
	for index, argument := range arguments {
		result[index] = model.Bytes(argument)
	}

	return result
}

func cloneLaunch(launch model.Launch) model.Launch {
	arguments := make([]model.Bytes, len(launch.Arguments))
	for index, argument := range launch.Arguments {
		arguments[index] = slices.Clone(argument)
	}

	return model.Launch{
		Executable:  slices.Clone(launch.Executable),
		Arguments:   arguments,
		Directory:   slices.Clone(launch.Directory),
		Environment: cloneVariables(launch.Environment),
		Birth:       slices.Clone(launch.Birth),
		Recovery:    launch.Recovery,
		Reason:      launch.Reason,
	}
}

func cloneVariables(variables []model.Variable) []model.Variable {
	if variables == nil {
		return nil
	}

	result := make([]model.Variable, len(variables))
	for index, variable := range variables {
		result[index] = model.Variable{
			Name:   slices.Clone(variable.Name),
			Value:  slices.Clone(variable.Value),
			State:  variable.State,
			Hidden: variable.Hidden,
		}
	}

	return result
}

func cleanArguments(arguments []model.Bytes) ([]string, bool) {
	if len(arguments) == 0 || len(arguments) > maxApplicationArguments {
		return nil, false
	}

	totalBytes := 0
	result := make([]string, len(arguments))

	for i, argBytes := range arguments {
		if len(argBytes) == 0 || len(argBytes) > maxArgumentBytes {
			return nil, false
		}

		totalBytes += len(argBytes)
		if totalBytes > maxTotalArgumentBytes {
			return nil, false
		}

		s := string(argBytes)
		if strings.IndexByte(s, 0) >= 0 {
			return nil, false
		}

		result[i] = s
	}

	return result, true
}

func isAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isAlphaNum(b byte) bool {
	return isAlpha(b) || (b >= '0' && b <= '9')
}

func cleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && strings.IndexByte(path, 0) < 0
}

func containsNULStrings(values []string) bool {
	for _, value := range values {
		if strings.IndexByte(value, 0) >= 0 {
			return true
		}
	}

	return false
}

func executableName(path string) string {
	return strings.TrimPrefix(filepath.Base(path), "-")
}

func observedAgentHarness(launch model.Launch) (aht.Harness, bool) {
	if harnessID, ok := harness.FromCommand(string(launch.Executable)); ok {
		return harnessID, true
	}

	if len(launch.Arguments) > 0 {
		if harnessID, ok := harness.FromCommand(string(launch.Arguments[0])); ok {
			return harnessID, true
		}
	}

	if !isScriptInterpreter(executableName(string(launch.Executable))) || len(launch.Arguments) < minimumEditorArguments {
		return "", false
	}

	scriptPath := string(launch.Arguments[1])
	if !filepath.IsAbs(scriptPath) || strings.IndexByte(scriptPath, 0) >= 0 {
		return "", false
	}

	harnessID := scriptHarness(scriptPath)

	return harnessID, harnessID != ""
}

func scriptHarness(scriptPath string) aht.Harness {
	candidate := filepath.Base(filepath.Clean(scriptPath))
	candidate = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(candidate, ".js"), ".mjs"), ".cjs")

	if harnessID, ok := harness.FromCommand(candidate); ok {
		return harnessID
	}

	return ""
}

func isScriptInterpreter(name string) bool {
	return slices.Contains([]string{"bun", "deno", "node", "nodejs"}, name) || strings.HasPrefix(name, "python")
}

func isShell(name string) bool {
	return slices.Contains([]string{"bash", "csh", "dash", "fish", "ksh", "nu", "sh", "tcsh", "zsh"}, name)
}

func isEditor(name string) bool {
	return slices.Contains([]string{"nano", "nvim", "vi", "vim"}, name)
}

func isMonitor(name string) bool {
	return slices.Contains([]string{"btop", "htop", "top"}, name)
}
