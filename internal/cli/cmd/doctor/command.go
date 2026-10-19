package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/app"
	"github.com/zigai/sesh/internal/cli/clibase"
	cmdautosave "github.com/zigai/sesh/internal/cli/cmd/autosave"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/daemon"
	"github.com/zigai/sesh/internal/doctor"
	"github.com/zigai/sesh/internal/setup"
)

var ErrDoctorChecksFailed = clibase.ErrDoctorChecksFailed

var envVars = []string{
	"SESH_CONFIG", "TMUX", "TMUX_TMPDIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "WORKTREE_DIR",
}

type Build struct {
	Version string
	Commit  string
	Date    string
}

type Report struct {
	Version string          `json:"version"`
	Build   ReportBuild     `json:"build"`
	Install ReportInstall   `json:"install"`
	Paths   ReportPaths     `json:"paths"`
	Env     []EnvVar        `json:"env"`
	Checks  []doctor.Result `json:"checks"`
	Summary doctor.Summary  `json:"summary"`
}

type ReportBuild struct {
	Commit string `json:"commit"`
	Date   string `json:"date"`
	Target string `json:"target"`
}

type ReportInstall struct {
	Executable string `json:"executable"`
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Location struct {
	Path   string `json:"path"`
	Source string `json:"source,omitempty"`
}

type ReportPaths struct {
	Config    []doctor.ConfigFile `json:"config"`
	Data      Location            `json:"data"`
	State     Location            `json:"state"`
	Log       Location            `json:"log"`
	Snapshots Location            `json:"snapshots"`
	History   Location            `json:"history"`
	Socket    Location            `json:"socket"`
}

func NewCommand(build Build) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check tmux, configuration, snapshots and services",
		Long: "Check tmux, configuration, snapshots and services.\n\n" +
			"Prints where sesh looks, then one row per check with a fix for each problem. It reads but never changes " +
			"files, tmux or services. Exits 1 when a check fails.",
		Args: clibase.UsageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDoctor(cmd, build)
		},
		Annotations: map[string]string{
			// The doctor loads configuration itself so a broken file becomes a failed check, and
			// it opens no log file so a run changes nothing.
			clibase.AnnotationSkipRuntimeSetup: "true",
		},
	}

	clibase.AddSocketFlag(cmd)
	cmd.Flags().String("repository", "", clibase.RepositoryFlagUsage)
	cmd.Flags().String("history", "", clibase.HistoryFlagUsage)
	clibase.AddJSONFlag(cmd)

	return cmd
}

func runDoctor(cmd *cobra.Command, build Build) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	opts, paths, err := resolveDoctorOptions(ctx, cmd)
	if err != nil {
		return err
	}

	results, runErr := doctor.Run(ctx, opts)
	report := newReport(build, opts, paths, results)

	failure := runErr
	if failure == nil && report.Summary.Fail > 0 {
		failure = clibase.NewExitError(clibase.ExitCodeGeneral, ErrDoctorChecksFailed)
	}

	if clibase.JSONOutputEnabled(cmd) {
		if failure != nil {
			return clibase.WriteJSONResultError(cmd, report, failure)
		}

		return clibase.WriteJSONResult(cmd, report)
	}

	renderErr := clibase.CommandOutputError(writeReport(cmd.OutOrStdout(), report, clibase.CommandOutputWidth(cmd), clibase.ColorOutput(cmd)))

	switch {
	case renderErr != nil:
		return errors.Join(failure, renderErr)
	case runErr != nil:
		return clibase.NewCommandResultError(oops.In("cli").With("operation", "doctor_run").Wrap(runErr))
	case failure != nil:
		return clibase.NewRenderedCommandError(failure)
	default:
		return nil
	}
}

func resolveDoctorOptions(ctx context.Context, cmd *cobra.Command) (doctor.Options, ReportPaths, error) {
	var opts doctor.Options

	cfg, configErr := clibase.LoadCommandConfig(ctx, cmd)
	if configErr != nil {
		message, ok := clibase.HumanConfigurationErrorMessage(configErr)
		if !ok {
			message = configErr.Error()
		}

		opts.ConfigError = capitalize(message)
		cfg = config.DefaultConfig()
	}

	workspace, err := app.Resolve(cfg, clibase.CommandLocations(cmd))
	if err != nil {
		return opts, ReportPaths{}, oops.In("cli").With("operation", "resolve_workspace").Wrap(err)
	}

	dataDir, err := config.GetDataDir(cfg)
	if err != nil {
		return opts, ReportPaths{}, oops.In("cli").With("operation", "resolve_data_dir").Wrap(err)
	}

	logPath, err := config.GetLogFile(cfg)
	if err == nil {
		logPath, err = filepath.Abs(logPath)
	}

	if err != nil {
		return opts, ReportPaths{}, oops.In("cli").With("operation", "resolve_log_file").Wrap(err)
	}

	opts.PathEnv = os.Getenv("PATH")
	opts.Executable, _ = os.Executable()
	opts.ConfigFiles = configFiles(cmd, configErr == nil)
	opts.Flags = commandFlags(cmd)
	opts.AutosaveEnabled = cfg.Autosave.Enabled
	opts.Socket = workspace.Socket
	opts.DefaultSocket = clibase.DefaultTmuxSocket()
	opts.Server, opts.ServerError = workspace.OpenTmux()
	opts.SnapshotDir = workspace.Repository
	opts.HistoryPath = workspace.History
	opts.LogPath = logPath
	opts.Daemon = daemonState(ctx, workspace.Socket)
	opts.ServiceDirs = serviceSearchPath()
	opts.UnitActiveState = unitActiveState
	opts.ValidateCommand = commandLineValidator(cmd.Root())

	if configErr == nil {
		for _, name := range cfg.Display.UsedFieldNames() {
			opts.CustomFields = append(opts.CustomFields, doctor.CustomField{Name: name, Command: cfg.Display.Fields[name].Command})
		}
	}

	paths := ReportPaths{
		Config:    opts.ConfigFiles,
		Data:      Location{Path: dataDir, Source: setting(cfg.Paths.DataDir != "", "paths.data_dir")},
		State:     Location{Path: filepath.Join(config.GetXDGPaths().StateHome, "sesh"), Source: ""},
		Log:       Location{Path: logPath, Source: setting(cfg.Paths.LogFile != "", "paths.log_file")},
		Snapshots: Location{Path: workspace.Repository, Source: flagSource(cmd, "repository")},
		History:   Location{Path: workspace.History, Source: flagSource(cmd, "history")},
		Socket:    Location{Path: workspace.Socket, Source: socketSource(cmd)},
	}

	return opts, paths, nil
}

func configFiles(cmd *cobra.Command, loaded bool) []doctor.ConfigFile {
	explicit, _ := cmd.Flags().GetString("config")
	noConfig, _ := cmd.Flags().GetBool("no-config")

	source := "--config"
	if explicit == "" {
		explicit, source = os.Getenv("SESH_CONFIG"), "SESH_CONFIG"
	}

	files := []doctor.ConfigFile{
		{Path: config.SystemConfigPath, Status: "", Source: ""},
		{Path: config.DefaultConfigPath(), Status: "", Source: ""},
	}

	if explicit != "" {
		path := explicit
		if path != "-" {
			path, _ = filepath.Abs(explicit)
		}

		files = []doctor.ConfigFile{{Path: path, Status: "", Source: source}}
	}

	for index := range files {
		file := &files[index]

		_, statErr := os.Stat(file.Path)
		exists := file.Path == "-" || statErr == nil

		switch {
		case noConfig:
			file.Status = doctor.ConfigSkipped
		case !exists:
			file.Status = doctor.ConfigNotFound
		case loaded:
			file.Status = doctor.ConfigLoaded
		default:
			file.Status = doctor.ConfigNotLoaded
		}
	}

	return files
}

func commandFlags(cmd *cobra.Command) map[string]string {
	flags := make(map[string]string)

	for _, name := range []string{"config", "socket", "repository", "history"} {
		if flag := cmd.Flags().Lookup(name); flag != nil && flag.Changed {
			flags["--"+name] = flag.Value.String()
		}
	}

	if noConfig, _ := cmd.Flags().GetBool("no-config"); noConfig {
		flags["--no-config"] = ""
	}

	return flags
}

func daemonState(ctx context.Context, socket string) doctor.Daemon {
	state, status := cmdautosave.DaemonRuntimeState(ctx, socket)
	path, _ := daemon.RuntimeStatusPath(os.Getenv("XDG_RUNTIME_DIR"), socket)

	return doctor.Daemon{State: state, Phase: string(status.Phase), InhibitorsHeld: status.InhibitorsHeld, RuntimePath: path}
}

func unitActiveState(ctx context.Context, unit string) (string, error) {
	states, err := setup.UnitStates(ctx, setup.ServiceManager{Inspect: nil, Run: nil}, []string{unit})
	if err != nil {
		return "", oops.In("cli").With("operation", "inspect_unit_state").Wrap(err)
	}

	return states[0].Active, nil
}

func serviceSearchPath() []string {
	if runtime.GOOS != setup.PlatformLinux {
		return nil
	}

	userDir, err := setup.ServiceDirectory(setup.PlatformLinux)
	if err != nil {
		return nil
	}

	return []string{
		userDir,
		"/etc/systemd/user",
		filepath.Join(config.GetXDGPaths().DataHome, "systemd", "user"),
		"/usr/local/lib/systemd/user",
		"/usr/lib/systemd/user",
	}
}

func flagSource(cmd *cobra.Command, name string) string {
	if flag := cmd.Flags().Lookup(name); flag != nil && flag.Changed {
		return "--" + name
	}

	return ""
}

func socketSource(cmd *cobra.Command) string {
	if source := flagSource(cmd, "socket"); source != "" {
		return source
	}

	if socket, _, _ := strings.Cut(os.Getenv("TMUX"), ","); socket != "" {
		return "TMUX"
	}

	return ""
}

func setting(set bool, name string) string {
	if set {
		return name
	}

	return ""
}

func capitalize(text string) string {
	first, size := utf8.DecodeRuneInString(text)

	return string(unicode.ToUpper(first)) + text[size:]
}

func newReport(build Build, opts doctor.Options, paths ReportPaths, results []doctor.Result) Report {
	env := []EnvVar{}

	for _, name := range envVars {
		if value := os.Getenv(name); value != "" {
			env = append(env, EnvVar{Name: name, Value: doctor.DisplayPath(value)})
		}
	}

	paths.Config = slices.Clone(paths.Config)
	for index := range paths.Config {
		paths.Config[index].Path = doctor.DisplayPath(paths.Config[index].Path)
	}

	for _, location := range []*Location{&paths.Data, &paths.State, &paths.Log, &paths.Snapshots, &paths.History, &paths.Socket} {
		location.Path = doctor.DisplayPath(location.Path)
	}

	return Report{
		Version: build.Version,
		Build:   ReportBuild{Commit: build.Commit, Date: build.Date, Target: runtime.GOOS + "/" + runtime.GOARCH},
		Install: ReportInstall{Executable: doctor.DisplayPath(opts.Executable)},
		Paths:   paths,
		Env:     env,
		Checks:  results,
		Summary: doctor.Summarize(results),
	}
}
