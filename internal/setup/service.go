package setup

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/action"
)

const ManagedMarker = "Managed by sesh. Do not edit."

const (
	PlatformLinux  = "linux"
	PlatformDarwin = "darwin"
)

const (
	KindSystemdService ServiceKind = "systemd-service"
	KindSystemdTimer   ServiceKind = "systemd-timer"
	KindLaunchdAgent   ServiceKind = "launchd-agent"
)

const (
	AutosaveServiceName    = "sesh-autosave.service"
	AutosaveTimerName      = "sesh-autosave.timer"
	TmuxServiceName        = "sesh-tmux.service"
	RemoteServiceName      = "sesh-remote-monitor.service"
	AgentStatusServiceName = "sesh-agent-status.service"
	CoordinatorServiceName = "sesh-coordinator.service"
	RestoreServiceName     = "sesh-restore.service"
	AutosaveAgentName      = "com.zigai.sesh.autosave.plist"
	TmuxAgentName          = "com.zigai.sesh.tmux.plist"
	RemoteAgentName        = "com.zigai.sesh.remote-monitor.plist"
	AgentStatusAgentName   = "com.zigai.sesh.agent-status.plist"
	CoordinatorAgentName   = "com.zigai.sesh.coordinator.plist"
)

const ForegroundServerTmuxMinor = 7

const (
	restoreStartTimeout = "10min"
)

const (
	stopBudgetGrace  = 5 * time.Second
	secondsPerMinute = 60
)

const (
	tmuxForegroundRequirement    = "tmux >= 3.7 foreground server mode is not verified on this host"
	remoteEntryRequirement       = "the native remote monitor entry point is not available in this binary"
	tmuxDarwinRuntimeRequirement = "managed tmux service is unavailable on macOS until the native launchd runtime is exercised on a disposable host"
	coordinatorDarwinRequirement = "lifecycle coordinator daemon is available only on Linux"
)

var (
	ErrUnsupportedPlatform       = errors.New("unsupported service platform")
	ErrServiceProgramRequired    = errors.New("sesh executable path is required to render service definitions")
	ErrServiceProgramNotAbsolute = errors.New("sesh executable path must be absolute")
	ErrServiceSocketRequired     = errors.New("an absolute tmux socket path is required to render service definitions")
	ErrServicePathsRequired      = errors.New("repository, lease root and history paths are required to render service definitions")
	ErrServiceIntervalRequired   = errors.New("autosave interval must be positive to render service definitions")
	ErrServiceDeadlineRequired   = errors.New("final save deadline must be positive to render service definitions")
	ErrServiceTmuxRequired       = errors.New("tmux executable path is required for the managed tmux service")
)

type ServiceKind string

type ServiceInputs struct {
	Program                string
	Tmux                   string
	SocketPath             string
	RepositoryPath         string
	LeaseRootPath          string
	HistoryPath            string
	StatusPath             string
	Interval               time.Duration
	FinalSaveDeadline      time.Duration
	Platform               string
	WithTmuxService        bool
	TmuxForegroundVerified bool
	RequiresTmuxForeground bool
	WithCoordinator        bool
}

type ServiceDefinition struct {
	Name     string
	Kind     ServiceKind
	Purpose  string
	Path     string
	Content  string
	Requires []string
}

func ServiceDirectory(platform string) (string, error) {
	if platform == "" {
		platform = runtime.GOOS
	}

	switch platform {
	case PlatformLinux:
		configHome := os.Getenv("XDG_CONFIG_HOME")
		if configHome == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", oops.In("setup").Code("setup_service_failed").With("operation", "resolve_service_directory").Wrap(err)
			}

			configHome = filepath.Join(home, ".config")
		}

		return filepath.Join(configHome, "systemd", "user"), nil
	case PlatformDarwin:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", oops.In("setup").Code("setup_service_failed").With("operation", "resolve_service_directory").Wrap(err)
		}

		return filepath.Join(home, "Library", "LaunchAgents"), nil
	default:
		return "", oops.In("setup").Code("setup_service_failed").With("operation", "resolve_service_directory").Wrap(ErrUnsupportedPlatform)
	}
}

func Definitions(inputs ServiceInputs) ([]ServiceDefinition, error) {
	platform := inputs.Platform
	if platform == "" {
		platform = runtime.GOOS
	}

	inputs.Platform = platform

	if err := validateServiceInputs(inputs); err != nil {
		return nil, oops.In("setup").Code("setup_service_failed").With("operation", "render_definitions").Wrap(err)
	}

	root, err := ServiceDirectory(platform)
	if err != nil {
		return nil, oops.In("setup").Code("setup_service_failed").With("operation", "render_definitions").Wrap(err)
	}

	switch platform {
	case PlatformLinux:
		return systemdDefinitions(root, inputs)
	case PlatformDarwin:
		return launchdDefinitions(root, inputs)
	default:
		return nil, oops.In("setup").Code("setup_service_failed").With("operation", "render_definitions").Wrap(ErrUnsupportedPlatform)
	}
}

func Relocate(definitions []ServiceDefinition, directory string) []ServiceDefinition {
	relocated := make([]ServiceDefinition, 0, len(definitions))

	for _, definition := range definitions {
		definition.Path = filepath.Join(directory, definition.Name)
		relocated = append(relocated, definition)
	}

	return relocated
}

func RemoteMonitorArgs(inputs ServiceInputs) []string {
	return []string{"daemon", "remote-monitor", "--socket", inputs.SocketPath}
}

func RemoteMonitorDefinition(inputs ServiceInputs, available bool) (ServiceDefinition, error) {
	if inputs.Platform == "" {
		inputs.Platform = runtime.GOOS
	}

	if err := validateServiceInputs(inputs); err != nil {
		return ServiceDefinition{}, oops.In("setup").Code("setup_service_failed").With("operation", "render_remote_definition").Wrap(err)
	}

	root, err := ServiceDirectory(inputs.Platform)
	if err != nil {
		return ServiceDefinition{}, oops.In("setup").Code("setup_service_failed").With("operation", "render_remote_definition").Wrap(err)
	}

	definition := ServiceDefinition{
		Name:     RemoteServiceName,
		Kind:     KindSystemdService,
		Purpose:  "remote session monitor and inventory stream",
		Path:     filepath.Join(root, RemoteServiceName),
		Content:  "",
		Requires: nil,
	}

	switch inputs.Platform {
	case PlatformLinux:
		definition.Kind = KindSystemdService
		definition.Content = renderSystemdRemoteMonitor(inputs)
	case PlatformDarwin:
		definition.Kind = KindLaunchdAgent
		definition.Name = RemoteAgentName
		definition.Path = filepath.Join(root, RemoteAgentName)
		definition.Content = renderLaunchdAgent(
			"com.zigai.sesh.remote-monitor",
			"sesh remote session monitor",
			inputs.Program,
			RemoteMonitorArgs(inputs),
			0,
			false,
		)
	default:
		return ServiceDefinition{}, oops.In("setup").Code("setup_service_failed").With("operation", "render_remote_definition").Wrap(ErrUnsupportedPlatform)
	}

	if !available {
		definition.Requires = append(definition.Requires, remoteEntryRequirement)
	}

	return definition, nil
}

func AgentStatusArgs(inputs ServiceInputs) []string {
	return []string{"daemon", "agent-window-status", "--socket", inputs.SocketPath}
}

func AgentStatusDefinition(inputs ServiceInputs) (ServiceDefinition, error) {
	if inputs.Platform == "" {
		inputs.Platform = runtime.GOOS
	}

	if err := validateServiceInputs(inputs); err != nil {
		return ServiceDefinition{}, oops.In("setup").Code("setup_service_failed").With("operation", "render_agent_status_definition").Wrap(err)
	}

	root, err := ServiceDirectory(inputs.Platform)
	if err != nil {
		return ServiceDefinition{}, oops.In("setup").Code("setup_service_failed").With("operation", "render_agent_status_definition").Wrap(err)
	}

	definition := ServiceDefinition{
		Name:     AgentStatusServiceName,
		Kind:     KindSystemdService,
		Purpose:  "agent window status tracker",
		Path:     filepath.Join(root, AgentStatusServiceName),
		Content:  "",
		Requires: nil,
	}

	switch inputs.Platform {
	case PlatformLinux:
		definition.Content = renderSystemdAgentStatus(inputs)
	case PlatformDarwin:
		definition.Kind = KindLaunchdAgent
		definition.Name = AgentStatusAgentName
		definition.Path = filepath.Join(root, AgentStatusAgentName)
		definition.Content = renderLaunchdAgent(
			"com.zigai.sesh.agent-status",
			"sesh agent window status tracker",
			inputs.Program,
			AgentStatusArgs(inputs),
			0,
			true,
		)
	default:
		return ServiceDefinition{}, oops.In("setup").Code("setup_service_failed").With("operation", "render_agent_status_definition").Wrap(ErrUnsupportedPlatform)
	}

	return definition, nil
}

func CoordinatorArgs(inputs ServiceInputs) []string {
	args := []string{
		"daemon", "coordinator",
		"--socket", inputs.SocketPath,
		"--repository", inputs.RepositoryPath,
		"--lease-root", inputs.LeaseRootPath,
		"--history", inputs.HistoryPath,
	}
	if inputs.StatusPath != "" {
		args = append(args, "--status-file", inputs.StatusPath)
	}

	return args
}

func CoordinatorDefinition(inputs ServiceInputs) (ServiceDefinition, error) {
	if inputs.Platform == "" {
		inputs.Platform = runtime.GOOS
	}

	if err := validateServiceInputs(inputs); err != nil {
		return ServiceDefinition{}, oops.In("setup").Code("setup_service_failed").With("operation", "render_coordinator_definition").Wrap(err)
	}

	root, err := ServiceDirectory(inputs.Platform)
	if err != nil {
		return ServiceDefinition{}, oops.In("setup").Code("setup_service_failed").With("operation", "render_coordinator_definition").Wrap(err)
	}

	definition := ServiceDefinition{
		Name:     CoordinatorServiceName,
		Kind:     KindSystemdService,
		Purpose:  "sleep and shutdown save coordinator",
		Path:     filepath.Join(root, CoordinatorServiceName),
		Content:  "",
		Requires: nil,
	}

	switch inputs.Platform {
	case PlatformLinux:
		definition.Kind = KindSystemdService
		definition.Content = renderSystemdCoordinatorService(inputs)
	case PlatformDarwin:
		definition.Kind = KindLaunchdAgent
		definition.Name = CoordinatorAgentName
		definition.Path = filepath.Join(root, CoordinatorAgentName)
		definition.Content = renderLaunchdAgent(
			"com.zigai.sesh.coordinator",
			"sesh lifecycle coordinator daemon",
			inputs.Program,
			CoordinatorArgs(inputs),
			0,
			false,
		)
		definition.Requires = append(definition.Requires, coordinatorDarwinRequirement)
	default:
		return ServiceDefinition{}, oops.In("setup").Code("setup_service_failed").With("operation", "render_coordinator_definition").Wrap(ErrUnsupportedPlatform)
	}

	return definition, nil
}

func renderSystemdCoordinatorService(inputs ServiceInputs) string {
	var content strings.Builder

	content.WriteString(managedComment())
	content.WriteString("[Unit]\n")
	content.WriteString("Description=sesh sleep and shutdown save coordinator\n")
	content.WriteString("Documentation=man:sesh(1)\n")
	content.WriteString(managedTmuxOrdering(inputs, false) + "\n")
	content.WriteString("[Service]\n")
	content.WriteString("Type=exec\n")
	content.WriteString("ExecStart=" + systemdCommand(inputs.Program, CoordinatorArgs(inputs)) + "\n")
	content.WriteString("SuccessExitStatus=" + strconv.Itoa(action.ExitCodeCanceled) + "\n")
	content.WriteString("Restart=on-failure\n\n")
	content.WriteString("[Install]\n")
	content.WriteString("WantedBy=default.target\n")

	return content.String()
}

func renderSystemdRemoteMonitor(inputs ServiceInputs) string {
	var content strings.Builder

	content.WriteString(managedComment())
	content.WriteString("[Unit]\n")
	content.WriteString("Description=sesh remote session monitor\n")
	content.WriteString("Documentation=man:sesh(1)\n")
	content.WriteString(managedTmuxOrdering(inputs, false) + "\n")
	content.WriteString("[Service]\n")
	content.WriteString("Type=exec\n")
	content.WriteString("ExecStart=" + systemdCommand(inputs.Program, RemoteMonitorArgs(inputs)) + "\n")
	content.WriteString("SuccessExitStatus=" + strconv.Itoa(action.ExitCodeCanceled) + "\n")
	content.WriteString("Restart=on-failure\n\n")
	content.WriteString("[Install]\n")
	content.WriteString("WantedBy=default.target\n")

	return content.String()
}

func renderSystemdAgentStatus(inputs ServiceInputs) string {
	var content strings.Builder

	content.WriteString(managedComment())
	content.WriteString("[Unit]\n")
	content.WriteString("Description=sesh agent window status tracker\n")
	content.WriteString("Documentation=man:sesh(1)\n")
	content.WriteString(managedTmuxOrdering(inputs, false) + "\n")
	content.WriteString("[Service]\n")
	content.WriteString("Type=exec\n")
	content.WriteString("ExecStart=" + systemdCommand(inputs.Program, AgentStatusArgs(inputs)) + "\n")
	content.WriteString("SuccessExitStatus=" + strconv.Itoa(action.ExitCodeCanceled) + "\n")
	content.WriteString("Restart=on-failure\n\n")
	content.WriteString("[Install]\n")
	content.WriteString("WantedBy=default.target\n")

	return content.String()
}

func renderSystemdAutosaveService(inputs ServiceInputs) string {
	var content strings.Builder

	content.WriteString(managedComment())
	content.WriteString("[Unit]\n")
	content.WriteString("Description=sesh periodic tmux snapshot autosave\n")
	content.WriteString("Documentation=man:sesh(1)\n")
	content.WriteString(managedTmuxOrdering(inputs, true) + "\n")
	content.WriteString("[Service]\n")
	content.WriteString("Type=oneshot\n")
	content.WriteString("ExecStart=" + systemdCommand(inputs.Program, autosaveArgs(inputs)) + "\n")

	return content.String()
}

func managedTmuxOrdering(inputs ServiceInputs, partOf bool) string {
	if !inputs.WithTmuxService {
		return ""
	}

	ordering := "After=" + TmuxServiceName + "\n"
	if partOf {
		ordering += "PartOf=" + TmuxServiceName + "\n"
	}

	return ordering
}

func formatSystemdDuration(d time.Duration) string {
	if d%time.Minute == 0 {
		return strconv.Itoa(int(d/time.Minute)) + "min"
	}

	return strconv.Itoa(int(d/time.Second)) + "s"
}

func renderSystemdAutosaveTimer(inputs ServiceInputs) string {
	interval := formatSystemdDuration(inputs.Interval)

	var content strings.Builder
	content.WriteString(managedComment())
	content.WriteString("[Unit]\n")

	if inputs.WithTmuxService {
		content.WriteString("Description=Run the sesh autosave service while the managed tmux server runs\n")
		content.WriteString("PartOf=" + TmuxServiceName + "\n\n")
	} else {
		content.WriteString("Description=Run the sesh autosave service periodically\n\n")
	}

	content.WriteString("[Timer]\n")
	content.WriteString("OnActiveSec=" + interval + "\n")
	content.WriteString("OnUnitInactiveSec=" + interval + "\n")
	content.WriteString("AccuracySec=5s\n")
	content.WriteString("Unit=" + AutosaveServiceName + "\n\n")
	content.WriteString("[Install]\n")
	content.WriteString("WantedBy=default.target\n")

	return content.String()
}

func renderSystemdTmuxService(inputs ServiceInputs) string {
	stopBudget := inputs.FinalSaveDeadline + stopBudgetGrace

	var content strings.Builder

	content.WriteString(managedComment())
	content.WriteString("[Unit]\n")
	content.WriteString("Description=sesh-managed foreground tmux server\n")
	content.WriteString("Documentation=man:sesh(1)\n")
	content.WriteString("Wants=" + RestoreServiceName + "\n\n")
	content.WriteString("[Service]\n")
	content.WriteString("Type=exec\n")
	content.WriteString("ExecStart=" + systemdCommand(inputs.Program, daemonArgs(inputs)) + "\n")
	content.WriteString("ExecStartPost=" + systemdCommand(inputs.Program, daemonReadyArgs(inputs)) + "\n")
	content.WriteString("ExecStop=" + systemdCommand(inputs.Program, finalSaveArgs(inputs)) + "\n")
	content.WriteString("TimeoutStopSec=" + strconv.FormatInt(int64(stopBudget/time.Second), 10) + "s\n")
	content.WriteString("KillMode=control-group\n")
	// One OOM-killed process must cost that process, not the server and every pane with it.
	content.WriteString("OOMPolicy=continue\n")
	content.WriteString("SuccessExitStatus=" + strconv.Itoa(action.ExitCodeCanceled) + "\n")
	content.WriteString("Restart=no\n\n")
	content.WriteString("[Install]\n")
	content.WriteString("WantedBy=default.target\n")

	return content.String()
}

func renderSystemdRestoreService(inputs ServiceInputs) string {
	var content strings.Builder

	content.WriteString(managedComment())
	content.WriteString("[Unit]\n")
	content.WriteString("Description=sesh restore the newest snapshot after tmux starts\n")
	content.WriteString("Documentation=man:sesh(1)\n")
	content.WriteString("After=" + TmuxServiceName + "\n\n")
	content.WriteString("[Service]\n")
	// A separate oneshot keeps a long restore from delaying or timing out the server's start.
	content.WriteString("Type=oneshot\n")
	content.WriteString("ExecStart=" + systemdCommand(inputs.Program, restoreOnStartArgs(inputs)) + "\n")
	content.WriteString("TimeoutStartSec=" + restoreStartTimeout + "\n")

	return content.String()
}

func restoreOnStartArgs(inputs ServiceInputs) []string {
	return []string{
		"autosave", "restore-on-start",
		"--socket", inputs.SocketPath,
		"--repository", inputs.RepositoryPath,
		"--lease-root", inputs.LeaseRootPath,
		"--history", inputs.HistoryPath,
	}
}

func launchdDefinitions(root string, inputs ServiceInputs) ([]ServiceDefinition, error) {
	autosave := ServiceDefinition{
		Name:    AutosaveAgentName,
		Kind:    KindLaunchdAgent,
		Purpose: "periodic tmux snapshot autosave",
		Path:    filepath.Join(root, AutosaveAgentName),
		Content: renderLaunchdAgent(
			"com.zigai.sesh.autosave",
			"sesh periodic tmux snapshot autosave",
			inputs.Program,
			autosaveArgs(inputs),
			int(inputs.Interval/time.Second),
			false,
		),
		Requires: []string{
			"launchd supervision only: logout, sleep and shutdown ordering for the saved processes is not established",
		},
	}

	definitions := []ServiceDefinition{autosave}

	if inputs.WithTmuxService {
		tmux := ServiceDefinition{
			Name:    TmuxAgentName,
			Kind:    KindLaunchdAgent,
			Purpose: "managed foreground tmux server",
			Path:    filepath.Join(root, TmuxAgentName),
			Content: renderLaunchdAgent(
				"com.zigai.sesh.tmux",
				"sesh-managed foreground tmux server",
				inputs.Program,
				daemonArgs(inputs),
				0,
				false,
			),
			Requires: []string{
				tmuxDarwinRuntimeRequirement,
				"launchd stop ordering does not guarantee a final save before the saved processes exit",
			},
		}

		definitions = append(definitions, tmux)
	}

	if inputs.WithCoordinator {
		coord, err := CoordinatorDefinition(inputs)
		if err != nil {
			return nil, oops.In("setup").Code("setup_service_failed").With("operation", "render_definitions").Wrap(err)
		}

		definitions = append(definitions, coord)
	}

	return definitions, nil
}

func renderLaunchdAgent(label, description, program string, args []string, intervalSeconds int, runAtLoad bool) string {
	var content strings.Builder

	content.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	content.WriteString("<!-- " + ManagedMarker + " -->\n")
	content.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	content.WriteString("<plist version=\"1.0\">\n")
	content.WriteString("<dict>\n")
	content.WriteString("\t<key>Label</key>\n\t<string>" + xmlEscape(label) + "</string>\n")
	content.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	content.WriteString("\t\t<string>" + xmlEscape(program) + "</string>\n")

	for _, arg := range args {
		content.WriteString("\t\t<string>" + xmlEscape(arg) + "</string>\n")
	}

	content.WriteString("\t</array>\n")

	if intervalSeconds > 0 {
		content.WriteString("\t<key>StartInterval</key>\n\t<integer>" + strconv.Itoa(intervalSeconds) + "</integer>\n")
	}

	content.WriteString("\t<key>RunAtLoad</key>\n\t<" + strconv.FormatBool(runAtLoad) + "/>\n")
	content.WriteString("\t<key>Description</key>\n\t<string>" + xmlEscape(description) + "</string>\n")
	content.WriteString("</dict>\n")
	content.WriteString("</plist>\n")

	return content.String()
}

func managedComment() string {
	return "# " + ManagedMarker + "\n"
}

func systemdCommand(program string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, systemdEscape(program))

	for _, arg := range args {
		parts = append(parts, systemdEscape(arg))
	}

	return strings.Join(parts, " ")
}

func systemdEscape(value string) string {
	escaped := strings.NewReplacer(
		"%", "%%",
		"$", "$$",
		"\\", "\\\\",
		"\"", "\\\"",
	).Replace(value)

	if escaped == "" {
		return `""`
	}

	if strings.ContainsAny(escaped, " \t\n\"'\\") {
		return `"` + escaped + `"`
	}

	return escaped
}

func xmlEscape(value string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	).Replace(value)
}

func validateServiceInputs(inputs ServiceInputs) error {
	if inputs.Program == "" {
		return oops.In("setup").Code("setup_service_failed").With("operation", "validate_service_inputs").Wrap(ErrServiceProgramRequired)
	}

	if !filepath.IsAbs(inputs.Program) {
		return oops.In("setup").Code("setup_service_failed").With("operation", "validate_service_inputs").Wrap(ErrServiceProgramNotAbsolute)
	}

	if inputs.SocketPath == "" || !filepath.IsAbs(inputs.SocketPath) {
		return oops.In("setup").Code("setup_service_failed").With("operation", "validate_service_inputs").Wrap(ErrServiceSocketRequired)
	}

	if inputs.RepositoryPath == "" || inputs.LeaseRootPath == "" || inputs.HistoryPath == "" {
		return oops.In("setup").Code("setup_service_failed").With("operation", "validate_service_inputs").Wrap(ErrServicePathsRequired)
	}

	if inputs.Interval <= 0 {
		return oops.In("setup").Code("setup_service_failed").With("operation", "validate_service_inputs").Wrap(ErrServiceIntervalRequired)
	}

	if inputs.FinalSaveDeadline <= 0 {
		return oops.In("setup").Code("setup_service_failed").With("operation", "validate_service_inputs").Wrap(ErrServiceDeadlineRequired)
	}

	if inputs.WithTmuxService && inputs.Tmux == "" {
		return oops.In("setup").Code("setup_service_failed").With("operation", "validate_service_inputs").Wrap(ErrServiceTmuxRequired)
	}

	return nil
}

func autosaveArgs(inputs ServiceInputs) []string {
	return []string{
		"autosave", "save-if-due",
		"--socket", inputs.SocketPath,
		"--repository", inputs.RepositoryPath,
		"--lease-root", inputs.LeaseRootPath,
		"--history", inputs.HistoryPath,
	}
}

func finalSaveArgs(inputs ServiceInputs) []string {
	args := []string{
		"autosave", "final-save",
		"--socket", inputs.SocketPath,
		"--repository", inputs.RepositoryPath,
		"--lease-root", inputs.LeaseRootPath,
		"--history", inputs.HistoryPath,
		"--deadline", inputs.FinalSaveDeadline.String(),
	}

	if inputs.StatusPath != "" {
		args = append(args, "--status-file", inputs.StatusPath)
	}

	return args
}

func daemonArgs(inputs ServiceInputs) []string {
	return []string{
		"daemon", "tmux",
		"--socket", inputs.SocketPath,
		"--tmux", inputs.Tmux,
		"--stop-grace", stopBudgetGrace.String(),
	}
}

func daemonReadyArgs(inputs ServiceInputs) []string {
	return []string{"daemon", "wait-ready", "--socket", inputs.SocketPath, "--timeout", stopBudgetGrace.String()}
}

func systemdDefinitions(root string, inputs ServiceInputs) ([]ServiceDefinition, error) {
	definitions := []ServiceDefinition{
		{
			Name:     AutosaveServiceName,
			Kind:     KindSystemdService,
			Purpose:  "periodic tmux snapshot autosave",
			Path:     filepath.Join(root, AutosaveServiceName),
			Content:  renderSystemdAutosaveService(inputs),
			Requires: nil,
		},
		{
			Name:     AutosaveTimerName,
			Kind:     KindSystemdTimer,
			Purpose:  "run the autosave service at the configured interval",
			Path:     filepath.Join(root, AutosaveTimerName),
			Content:  renderSystemdAutosaveTimer(inputs),
			Requires: nil,
		},
	}

	if inputs.WithTmuxService {
		var requires []string
		if inputs.RequiresTmuxForeground && !inputs.TmuxForegroundVerified {
			requires = append(requires, tmuxForegroundRequirement)
		}

		definition := ServiceDefinition{
			Name:     TmuxServiceName,
			Kind:     KindSystemdService,
			Purpose:  "managed foreground tmux server with a bounded final save",
			Path:     filepath.Join(root, TmuxServiceName),
			Content:  renderSystemdTmuxService(inputs),
			Requires: requires,
		}

		definitions = append(definitions, definition, ServiceDefinition{
			Name:     RestoreServiceName,
			Kind:     KindSystemdService,
			Purpose:  "restore the newest snapshot once the managed tmux server starts",
			Path:     filepath.Join(root, RestoreServiceName),
			Content:  renderSystemdRestoreService(inputs),
			Requires: requires,
		})
	}

	if inputs.WithCoordinator {
		coord, err := CoordinatorDefinition(inputs)
		if err != nil {
			return nil, oops.In("setup").Code("setup_service_failed").With("operation", "render_definitions").Wrap(err)
		}

		definitions = append(definitions, coord)
	}

	return definitions, nil
}
