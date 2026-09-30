package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/setup"
)

const (
	defaultServiceFinalSaveDeadline = 20 * time.Second
	defaultTmuxProbeTimeout         = 5 * time.Second
	requiredTmuxMajor               = 3
)

var (
	errSetupProgramRequired  = errors.New("the sesh executable path is required")
	errSetupBlockedArtifacts = errors.New("some service definitions have unmet prerequisites")
	errSetupForeignArtifacts = errors.New("some service definitions are not owned by sesh")
	errSetupTmuxRequired     = errors.New("--tmux is required for the managed tmux service definition")
)

type setupServiceJSON struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Purpose  string   `json:"purpose"`
	Path     string   `json:"path"`
	State    string   `json:"state"`
	Changed  bool     `json:"changed"`
	Requires []string `json:"requires,omitempty"`
	Content  string   `json:"content,omitempty"`
}

type setupServicesResultJSON struct {
	Platform string                      `json:"platform"`
	Applied  bool                        `json:"applied"`
	Written  int                         `json:"written"`
	Skipped  []string                    `json:"skipped,omitempty"`
	Blocked  []string                    `json:"blocked,omitempty"`
	Services []setupServiceJSON          `json:"services"`
	Manager  *setup.ServiceRefreshResult `json:"manager,omitempty"`
}

type setupAgentResultJSON struct {
	DryRun    bool                `json:"dryRun"`
	TrackerOp string              `json:"trackerOperation,omitempty"`
	Harnesses []setupAgentJSON    `json:"harnesses"`
	Tracker   setupAgentStateJSON `json:"tracker"`
	Warnings  []string            `json:"warnings,omitempty"`
}

type setupAgentJSON struct {
	Harness  string `json:"harness"`
	Path     string `json:"path,omitempty"`
	Changed  bool   `json:"changed"`
	Message  string `json:"message,omitempty"`
	NextStep string `json:"nextStep,omitempty"`
}

type setupAgentStateJSON struct {
	Platform  string `json:"platform,omitempty"`
	Manager   string `json:"manager,omitempty"`
	Installed bool   `json:"installed"`
	Current   bool   `json:"current"`
	Running   bool   `json:"running"`
	Changed   bool   `json:"changed"`
	Message   string `json:"message,omitempty"`
}

type serviceDurablePaths struct {
	repository string
	leaseRoot  string
	history    string
	status     string
}

func newSetupAgentsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents [agent...]",
		Short: "Configure coding-agent reporting through the published AHT interface",
		Long: "Configure coding-agent integrations and the background tracker.\n\n" +
			"Agent hooks, shims and the tracker service are owned and installed by AHT; sesh never duplicates hook installation. " +
			"Without agent names every supported harness is selected.",
		Args: clibase.UsageArgs(cobra.ArbitraryArgs),
		RunE: runSetupAgents,
	}

	cmd.Flags().Bool("dry-run", false, "Show changes without writing")
	cmd.Flags().Bool("force", false, "Replace integration files that AHT does not own")
	cmd.Flags().Bool("shim", false, "Install a wrapper executable instead of editing harness files")
	cmd.Flags().Bool("status", false, "Report integration and tracker state without changing anything")
	cmd.Flags().String("tracker", setup.TrackerEnable, "Tracker operation")
	clibase.SetFlagChoices(cmd, "tracker", setup.TrackerEnable, setup.TrackerDisable, setup.TrackerStatus)
	cmd.Flags().String("target-binary", "", "Explicit harness executable path for a shim")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func runSetupAgents(cmd *cobra.Command, args []string) error {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	force, _ := cmd.Flags().GetBool("force")
	useShim, _ := cmd.Flags().GetBool("shim")
	statusOnly, _ := cmd.Flags().GetBool("status")
	tracker, _ := cmd.Flags().GetString("tracker")
	targetValue, _ := cmd.Flags().GetString("target-binary")

	request := setup.AgentRequest{
		Agents:       args,
		DryRun:       dryRun,
		Force:        force,
		UseShim:      useShim,
		TargetBinary: targetValue,
		Tracker:      tracker,
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	var (
		result setup.AgentResult
		err    error
	)

	if statusOnly {
		result, err = setup.AgentStatus(ctx, request)
	} else {
		result, err = setup.Agents(ctx, request)
	}

	if err != nil {
		if errors.Is(err, setup.ErrUnknownAgent) || errors.Is(err, setup.ErrUnknownTrackerMode) {
			return clibase.NewExitError(clibase.ExitCodeUsage, err)
		}

		return oops.In("cli").With("operation", "setup_agents").Wrap(err)
	}

	return renderSetupAgents(cmd, result)
}

func renderSetupAgents(cmd *cobra.Command, result setup.AgentResult) error {
	if clibase.JSONOutputEnabled(cmd) {
		payload := setupAgentResultJSON{
			DryRun:    result.DryRun,
			TrackerOp: result.TrackerOp,
			Harnesses: make([]setupAgentJSON, 0, len(result.Harnesses)),
			Tracker: setupAgentStateJSON{
				Platform:  result.Tracker.Platform,
				Manager:   result.Tracker.Manager,
				Installed: result.Tracker.Installed,
				Current:   result.Tracker.Current,
				Running:   result.Tracker.Running,
				Changed:   result.Tracker.Changed,
				Message:   result.Tracker.Message,
			},
			Warnings: nil,
		}

		for _, item := range result.Harnesses {
			payload.Harnesses = append(payload.Harnesses, setupAgentJSON{
				Harness:  item.Harness,
				Path:     item.Path,
				Changed:  item.Changed,
				Message:  item.Message,
				NextStep: item.NextStep,
			})
		}

		return clibase.WriteJSONResult(cmd, payload)
	}

	verb := "Configured"
	if result.DryRun {
		verb = "Would configure"
	}

	if err := clibase.WriteOutput(cmd, "%s %d agent integration(s).\n", verb, len(result.Harnesses)); err != nil {
		return err
	}

	for _, item := range result.Harnesses {
		if err := clibase.WriteOutput(cmd, "  %s: %s\n", item.Harness, agentStatusLine(item)); err != nil {
			return err
		}

		if item.NextStep != "" {
			if err := clibase.WriteOutput(cmd, "    next: %s\n", item.NextStep); err != nil {
				return err
			}
		}
	}

	if result.TrackerOp != "" {
		return clibase.WriteOutput(
			cmd,
			"Tracker (%s): installed=%t running=%t current=%t\n",
			result.TrackerOp,
			result.Tracker.Installed,
			result.Tracker.Running,
			result.Tracker.Current,
		)
	}

	return nil
}

func agentStatusLine(item setup.AgentHarnessResult) string {
	if item.Message != "" {
		return item.Message
	}

	if item.Changed {
		return "current"
	}

	return "unchanged"
}

func newSetupServicesCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "services",
		Short:       "Render or apply the owned service definitions",
		Annotations: map[string]string{clibase.AnnotationOperation: "refresh"},
		Long: "Render or apply the owned service definitions.\n\n" +
			"The periodic autosave timer and the remote monitor are always rendered. --with-tmux-service adds a managed " +
			"foreground tmux server (daemon tmux) whose stop step runs one bounded final save while tmux is still alive. " +
			"--with-coordinator adds the sleep and shutdown save coordinator (daemon coordinator), which holds logind delay " +
			"inhibitors; unit ordering alone does not provide that.\n\n" +
			"Without --apply the plan is printed and nothing is written. Existing files that sesh does not own are reported and " +
			"left untouched. On Linux, --apply reloads the user service manager and refreshes an already running autosave timer " +
			"and remote monitor. Stopped services stay stopped, enabled state is preserved, and the tmux server and in-flight " +
			"autosave are never restarted. An explicit --dir writes definitions only and does not contact the service manager.",
		Args: clibase.UsageArgs(cobra.NoArgs),
		RunE: runSetupServices,
	}

	addServiceSetupFlags(cmd)
	cmd.Flags().Bool("with-tmux-service", false, "Also render the managed tmux server definition")
	cmd.Flags().Bool("with-coordinator", false, "Also render the sleep and shutdown save coordinator definition")
	cmd.Flags().Bool("verify-tmux", false, "Verify the target tmux version through the given socket before rendering the tmux service")

	return cmd
}

func addServiceSetupFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("apply", false, "Write the rendered definitions to the service directory")
	cmd.Flags().Bool("force", false, "Replace service files that sesh does not own")
	cmd.Flags().String("dir", "", "Service directory to render into [default: the platform user service directory]")
	cmd.Flags().String("program", "", "Absolute path of the sesh executable [default: the running sesh executable]")
	cmd.Flags().String("tmux", "", "Explicit absolute path of the tmux executable")
	cmd.Flags().String("socket", "", "Socket of the tmux server the services target [default: the sesh-managed socket with --with-tmux-service, else tmux's default socket]")
	cmd.Flags().String("repository", "", clibase.RepositoryFlagUsage)
	cmd.Flags().String("lease-root", "", clibase.LeaseRootFlagUsage)
	cmd.Flags().String("history", "", clibase.HistoryFlagUsage)
	cmd.Flags().String("status-file", "", clibase.StatusFileFlagUsage)
	cmd.Flags().Duration("interval", 0, "Autosave interval [default: autosave.interval setting]")
	cmd.Flags().Duration("final-save-deadline", defaultServiceFinalSaveDeadline, "Bounded time budget for the shutdown final save")
	clibase.AddJSONFlag(cmd)
}

func resolveServiceInputs(cmd *cobra.Command, withTmux bool) (setup.ServiceInputs, error) {
	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return setup.ServiceInputs{}, err
	}

	program, err := resolveSetupProgram(cmd)
	if err != nil {
		return setup.ServiceInputs{}, err
	}

	socketPath, err := resolveSetupServiceSocket(cmd, withTmux)
	if err != nil {
		return setup.ServiceInputs{}, err
	}

	durable, err := resolveServiceDurablePaths(cmd, cfg)
	if err != nil {
		return setup.ServiceInputs{}, err
	}

	interval, deadline, err := resolveServiceTiming(cmd, cfg)
	if err != nil {
		return setup.ServiceInputs{}, err
	}

	inputs := setup.ServiceInputs{
		Program:                program,
		Tmux:                   "",
		SocketPath:             socketPath,
		RepositoryPath:         durable.repository,
		LeaseRootPath:          durable.leaseRoot,
		HistoryPath:            durable.history,
		StatusPath:             durable.status,
		Interval:               interval,
		FinalSaveDeadline:      deadline,
		Platform:               "",
		WithTmuxService:        withTmux,
		WithCoordinator:        false,
		TmuxForegroundVerified: false,
		RequiresTmuxForeground: false,
	}

	if withTmux {
		tmuxPath, err := resolveSetupTmuxProgram(cmd)
		if err != nil {
			return setup.ServiceInputs{}, err
		}

		inputs.Tmux = tmuxPath
	}

	return inputs, nil
}

func resolveServiceDurablePaths(cmd *cobra.Command, cfg *config.Config) (serviceDurablePaths, error) {
	repoPath, err := resolveSetupServicePath(cmd, "repository", "")
	if err != nil {
		return serviceDurablePaths{}, err
	}

	leaseRoot, err := resolveSetupServicePath(cmd, "lease-root", "")
	if err != nil {
		return serviceDurablePaths{}, err
	}

	dataDir, err := config.GetDataDir(cfg)
	if err != nil {
		return serviceDurablePaths{}, oops.In("cli").With("operation", "resolve_data_dir").Wrap(err)
	}

	if repoPath == "" {
		repoPath = filepath.Join(dataDir, "snapshots")
	}

	if leaseRoot == "" {
		leaseRoot = filepath.Join(dataDir, "leases")
	}

	historyPath, err := resolveSetupServicePath(cmd, "history", filepath.Join(config.GetXDGPaths().StateHome, "sesh", "history.json"))
	if err != nil {
		return serviceDurablePaths{}, err
	}

	statusPath, err := resolveSetupServicePath(cmd, "status-file", filepath.Join(config.GetXDGPaths().StateHome, "sesh", automation.FinalSaveStatusName))
	if err != nil {
		return serviceDurablePaths{}, err
	}

	return serviceDurablePaths{
		repository: repoPath,
		leaseRoot:  leaseRoot,
		history:    historyPath,
		status:     statusPath,
	}, nil
}

func resolveServiceTiming(cmd *cobra.Command, cfg *config.Config) (time.Duration, time.Duration, error) {
	interval, err := cmd.Flags().GetDuration("interval")
	if err != nil {
		return 0, 0, oops.In("cli").With("operation", "read_setup_flag").Wrap(err)
	}

	if interval <= 0 {
		interval = config.AutosaveInterval(cfg)
	}

	deadline, err := cmd.Flags().GetDuration("final-save-deadline")
	if err != nil {
		return 0, 0, oops.In("cli").With("operation", "read_setup_flag").Wrap(err)
	}

	return interval, deadline, nil
}

func runSetupServices(cmd *cobra.Command, _ []string) error {
	return runSetupServicesWithManager(cmd, setup.ServiceManager{Inspect: nil, Run: nil})
}

func runSetupServicesWithManager(cmd *cobra.Command, manager setup.ServiceManager) error {
	withTmux, err := cmd.Flags().GetBool("with-tmux-service")
	if err != nil {
		return oops.In("cli").With("operation", "read_setup_flag").Wrap(err)
	}

	withCoordinator, err := cmd.Flags().GetBool("with-coordinator")
	if err != nil {
		return oops.In("cli").With("operation", "read_setup_flag").Wrap(err)
	}

	inputs, err := resolveServiceInputs(cmd, withTmux)
	if err != nil {
		return err
	}

	inputs.WithCoordinator = withCoordinator

	if withTmux {
		verify, flagErr := cmd.Flags().GetBool("verify-tmux")
		if flagErr != nil {
			return oops.In("cli").With("operation", "read_setup_flag").Wrap(flagErr)
		}

		inputs.RequiresTmuxForeground = verify
		if verify {
			verified, verifyErr := verifySetupTmuxForeground(cmd, inputs.SocketPath)
			if verifyErr != nil {
				return verifyErr
			}

			inputs.TmuxForegroundVerified = verified
		}
	}

	definitions, err := setup.Definitions(inputs)
	if err != nil {
		return clibase.NewExitError(clibase.ExitCodeUsage, err)
	}

	remoteDefinition, err := setup.RemoteMonitorDefinition(inputs, remoteMonitorAvailable(cmd))
	if err != nil {
		return clibase.NewExitError(clibase.ExitCodeUsage, err)
	}

	definitions = append(definitions, remoteDefinition)

	return applySetupDefinitionsWithManager(cmd, definitions, "refresh", manager)
}

func applySetupDefinitionsWithManager(
	cmd *cobra.Command,
	definitions []setup.ServiceDefinition,
	operation string,
	manager setup.ServiceManager,
) error {
	directory, err := resolveSetupServiceDirectory(cmd)
	if err != nil {
		return err
	}

	definitions = setup.Relocate(definitions, directory)

	withContent, err := cmd.Flags().GetBool("apply")
	if err != nil {
		return oops.In("cli").With("operation", "read_setup_flag").Wrap(err)
	}

	force, err := cmd.Flags().GetBool("force")
	if err != nil {
		return oops.In("cli").With("operation", "read_setup_flag").Wrap(err)
	}

	plans, err := setup.PlanDefinitions(definitions)
	if err != nil {
		return oops.In("cli").With("operation", "plan_service_definitions").Wrap(err)
	}

	result := setup.ApplyResult{Written: 0, SkippedNames: nil, BlockedNames: nil}

	var refreshed *setup.ServiceRefreshResult

	if withContent {
		result, refreshed, err = applyServiceChanges(cmd, definitions, plans, force, operation, manager)
	}

	payload := setupServicesResultJSON{
		Platform: inputsPlatform(definitions),
		Applied:  withContent,
		Written:  result.Written,
		Skipped:  planSkippedNames(plans, force),
		Blocked:  planBlockedNames(plans),
		Services: make([]setupServiceJSON, 0, len(definitions)),
		Manager:  refreshed,
	}

	for index, definition := range definitions {
		plan := plans[index]

		entry := setupServiceJSON{
			Name:     definition.Name,
			Kind:     string(definition.Kind),
			Purpose:  definition.Purpose,
			Path:     filepath.Join(directory, definition.Name),
			State:    string(plan.State),
			Changed:  plan.Changed,
			Requires: definition.Requires,
			Content:  definition.Content,
		}

		payload.Services = append(payload.Services, entry)
	}

	outcomeErr := errors.Join(err, setupServiceOutcome(payload))

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResultError(cmd, payload, outcomeErr)
	}

	if err := renderSetupServices(cmd, payload, operation); err != nil {
		return errors.Join(err, outcomeErr)
	}

	return outcomeErr
}

func applyServiceChanges(
	cmd *cobra.Command,
	definitions []setup.ServiceDefinition,
	plans []setup.ArtifactPlan,
	force bool,
	operation string,
	manager setup.ServiceManager,
) (setup.ApplyResult, *setup.ServiceRefreshResult, error) {
	if operation == "refresh" && inputsPlatform(definitions) == setup.PlatformLinux && !cmd.Flags().Changed("dir") {
		result, refreshed, err := setup.RefreshDefinitions(cmd.Context(), definitions, plans, force, manager)
		if err != nil {
			return result, &refreshed, oops.In("cli").With("operation", "refresh_services").Wrap(err)
		}

		return result, &refreshed, nil
	}

	result, err := setup.ApplyDefinitions(definitions, plans, force)
	if err != nil {
		return result, nil, oops.In("cli").With("operation", "apply_service_definitions").Wrap(err)
	}

	return result, nil, nil
}

func setupServiceOutcome(payload setupServicesResultJSON) error {
	switch {
	case len(payload.Blocked) > 0:
		return clibase.NewExitError(
			clibase.ExitCodeGeneral,
			oops.In("cli").Code("setup_blocked").With("operation", "setup_services", "blocked", strings.Join(payload.Blocked, ", ")).Wrap(errSetupBlockedArtifacts),
		)
	case len(payload.Skipped) > 0:
		return clibase.NewExitError(
			clibase.ExitCodeGeneral,
			oops.In("cli").Code("setup_blocked").With("operation", "setup_services", "skipped", strings.Join(payload.Skipped, ", ")).Wrap(errSetupForeignArtifacts),
		)
	default:
		return nil
	}
}

func renderSetupServices(cmd *cobra.Command, payload setupServicesResultJSON, operation string) error {
	verb := "Planned"
	count := len(payload.Services)

	if payload.Applied {
		verb = "Wrote"
		count = payload.Written
	}

	if err := clibase.WriteOutput(cmd, "%s %d service definition(s) for %s (%s).\n", verb, count, operation, payload.Platform); err != nil {
		return err
	}

	for _, service := range payload.Services {
		if err := clibase.WriteOutput(cmd, "  %s [%s] %s\n", service.Name, service.State, service.Path); err != nil {
			return err
		}

		for _, requirement := range service.Requires {
			if err := clibase.WriteOutput(cmd, "    unmet: %s\n", requirement); err != nil {
				return err
			}
		}
	}

	if !payload.Applied {
		for _, service := range payload.Services {
			if err := clibase.WriteOutput(cmd, "\n# %s\n%s", service.Name, service.Content); err != nil {
				return err
			}
		}
	}

	return renderSetupServiceRefresh(cmd, payload.Manager)
}

func renderSetupServiceRefresh(cmd *cobra.Command, result *setup.ServiceRefreshResult) error {
	if result == nil {
		return nil
	}

	if result.Reloaded {
		if err := clibase.WriteOutput(cmd, "Reloaded the user service manager.\n"); err != nil {
			return err
		}
	}

	for _, name := range result.Restarted {
		if err := clibase.WriteOutput(cmd, "Refreshed the running %s.\n", name); err != nil {
			return err
		}
	}

	if len(result.RestartRequired) > 0 {
		return clibase.WriteOutput(cmd, "Tmux startup changes take full effect the next time its server starts; live sessions were preserved.\n")
	}

	return nil
}

func resolveSetupServiceDirectory(cmd *cobra.Command) (string, error) {
	directory, _ := cmd.Flags().GetString("dir")
	if directory == "" {
		resolved, err := setup.ServiceDirectory("")
		if err != nil {
			return "", clibase.NewExitError(clibase.ExitCodeGeneral, err)
		}

		return resolved, nil
	}

	abs, err := filepath.Abs(directory)
	if err != nil {
		return "", oops.In("cli").With("operation", "resolve_service_directory").Wrap(err)
	}

	return abs, nil
}

func resolveSetupProgram(cmd *cobra.Command) (string, error) {
	program, _ := cmd.Flags().GetString("program")
	if program == "" {
		executable, err := os.Executable()
		if err != nil {
			return "", oops.In("cli").With("operation", "resolve_executable").Wrap(errors.Join(errSetupProgramRequired, err))
		}

		program = executable
	}

	abs, err := filepath.Abs(program)
	if err != nil {
		return "", oops.In("cli").With("operation", "resolve_program_path").Wrap(err)
	}

	return abs, nil
}

func resolveSetupTmuxProgram(cmd *cobra.Command) (string, error) {
	tmuxPath, _ := cmd.Flags().GetString("tmux")
	if tmuxPath == "" {
		return "", clibase.NewExitError(clibase.ExitCodeUsage, errSetupTmuxRequired)
	}

	abs, err := filepath.Abs(tmuxPath)
	if err != nil {
		return "", oops.In("cli").With("operation", "resolve_tmux_path").Wrap(err)
	}

	return abs, nil
}

// resolveSetupServiceSocket selects the tmux server the rendered services target: --socket, else
// the sesh-managed server's socket when that server is rendered too, else the server a plain
// `tmux` command uses. Services must never default to a managed socket nothing will create.
func resolveSetupServiceSocket(cmd *cobra.Command, withTmux bool) (string, error) {
	socketPath, _ := cmd.Flags().GetString("socket")
	if socketPath == "" && withTmux {
		runtimeDir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR"))
		if runtimeDir == "" {
			runtimeDir = filepath.Join(os.TempDir(), "sesh-runtime")
		}

		socketPath = filepath.Join(runtimeDir, "sesh", "tmux.sock")
	}

	if socketPath == "" {
		socketPath = clibase.DefaultTmuxSocket()
	}

	abs, err := filepath.Abs(socketPath)
	if err != nil {
		return "", oops.In("cli").With("operation", "resolve_socket_path").Wrap(err)
	}

	return abs, nil
}

func resolveSetupServicePath(cmd *cobra.Command, flag, fallback string) (string, error) {
	value, _ := cmd.Flags().GetString(flag)
	if value == "" {
		value = fallback
	}

	if value == "" {
		return "", nil
	}

	abs, err := filepath.Abs(value)
	if err != nil {
		return "", oops.In("cli").With("operation", "resolve_service_path").Wrap(err)
	}

	return abs, nil
}

func inputsPlatform(definitions []setup.ServiceDefinition) string {
	for _, definition := range definitions {
		switch definition.Kind {
		case setup.KindSystemdService, setup.KindSystemdTimer:
			return setup.PlatformLinux
		case setup.KindLaunchdAgent:
			return setup.PlatformDarwin
		}
	}

	return ""
}

func remoteMonitorAvailable(cmd *cobra.Command) bool {
	root := cmd.Root()
	if root == nil {
		return false
	}

	found, _, err := root.Find([]string{"daemon", "remote-monitor"})
	if err != nil || found == nil {
		return false
	}

	return found.Name() == "remote-monitor"
}

func verifySetupTmuxForeground(cmd *cobra.Command, socketPath string) (bool, error) {
	ctx, cancel := context.WithTimeout(clibase.CommandContext(cmd), defaultTmuxProbeTimeout)
	defer cancel()

	server, err := integration.Open(socketPath)
	if err != nil {
		return false, nil
	}

	version, err := server.Version(ctx)
	if err != nil {
		return false, nil
	}

	return version.AtLeast(requiredTmuxMajor, setup.ForegroundServerTmuxMinor), nil
}

func planBlockedNames(plans []setup.ArtifactPlan) []string {
	names := make([]string, 0, len(plans))

	for _, plan := range plans {
		if len(plan.Requires) > 0 {
			names = append(names, plan.Name)
		}
	}

	return names
}

func planSkippedNames(plans []setup.ArtifactPlan, force bool) []string {
	if force {
		return nil
	}

	names := make([]string, 0, len(plans))

	for _, plan := range plans {
		if plan.State == setup.ArtifactForeign {
			names = append(names, plan.Name)
		}
	}

	return names
}
