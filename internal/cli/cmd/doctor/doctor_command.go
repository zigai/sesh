package cmddoctor

import (
	"context"
	"errors"
	"path/filepath"

	prettytable "github.com/jedib0t/go-pretty/v6/table"
	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
	cmdautosave "github.com/zigai/sesh/internal/cli/cmd/autosave"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/doctor"
	integration "github.com/zigai/sesh/internal/integration/tmux"
)

const (
	colWidthDoctorCheck   = 16
	colWidthDoctorStatus  = 8
	colWidthDoctorReason  = 12
	colWidthDoctorMessage = 28
	colWidthDoctorFix     = 24
)

var ErrDoctorChecksFailed = clibase.ErrDoctorChecksFailed

type ChecksError struct{ error }

func (e *ChecksError) Unwrap() error { return e.error }

func (e *ChecksError) OutcomeLogged() bool { return true }

func NewChecksError(err error) error {
	return &ChecksError{error: err}
}

type ResultJSON struct {
	Checks      []doctor.Result `json:"checks"`
	HasErrors   bool            `json:"hasErrors"`
	HasWarnings bool            `json:"hasWarnings"`
}

type (
	doctorChecksError = ChecksError
	doctorResultJSON  = ResultJSON
)

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Inspect tmux environment, snapshot repository, and system health",
		Long:  "Inspect tmux environment, snapshot repository, and system health.\n\nPerforms strictly read-only diagnostics with honest capability and parity reporting.",
		Args:  clibase.UsageArgs(cobra.NoArgs),
		RunE:  runDoctor,
		Annotations: map[string]string{
			clibase.AnnotationSuccessOwned: "true",
		},
	}

	cmd.Flags().String("socket", "", "Explicit absolute path to tmux server socket")
	cmd.Flags().String("repository", "", "Snapshot repository directory")
	cmd.Flags().String("history", "", "History state JSON file path")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func resolveDoctorOptions(cmd *cobra.Command) (doctor.Options, error) {
	opts := doctor.Options{
		Server:            nil,
		SnapshotDir:       "",
		HistoryPath:       "",
		DaemonState:       cmdautosave.AutosaveDaemonUnselected,
		DaemonPhase:       "",
		InhibitorsHeld:    false,
		LogindDelayMillis: 0,
	}

	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return opts, err
	}

	socketPath, _ := cmd.Flags().GetString("socket")
	if socketPath != "" {
		server, err := integration.Open(socketPath)
		if err != nil {
			return opts, oops.In("cli").With("operation", "open_tmux").Wrap(err)
		}

		opts.Server = server

		runtimeState, runtimeStatus := cmdautosave.AutosaveDaemonRuntimeState(cmd.Context(), cmd)
		opts.DaemonState = runtimeState
		opts.DaemonPhase = string(runtimeStatus.Phase)
		opts.InhibitorsHeld = runtimeStatus.InhibitorsHeld
		opts.LogindDelayMillis = runtimeStatus.LogindDelayMillis
	}

	repoPath, _ := cmd.Flags().GetString("repository")
	if repoPath != "" {
		absRepo, err := filepath.Abs(repoPath)
		if err != nil {
			return opts, oops.In("cli").With("operation", "read_repository_flag").Wrap(err)
		}

		opts.SnapshotDir = absRepo
	} else {
		dataDir, err := config.GetDataDir(cfg)
		if err != nil {
			return opts, oops.In("cli").With("operation", "resolve_default_repository").Wrap(err)
		}

		opts.SnapshotDir = filepath.Join(dataDir, "snapshots")
	}

	historyPath, _ := cmd.Flags().GetString("history")
	if historyPath != "" {
		absHistory, err := filepath.Abs(historyPath)
		if err != nil {
			return opts, oops.In("cli").With("operation", "read_history_flag").Wrap(err)
		}

		opts.HistoryPath = absHistory
	} else {
		xdg := config.GetXDGPaths()
		opts.HistoryPath = filepath.Join(xdg.StateHome, "sesh", "history.json")
	}

	return opts, nil
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	opts, err := resolveDoctorOptions(cmd)
	if err != nil {
		return err
	}

	results, runErr := doctor.Run(ctx, opts)
	result := newDoctorResult(results)

	if runErr != nil {
		failure := oops.In("cli").With("operation", "doctor_run").Wrap(runErr)
		if len(results) == 0 {
			return failure
		}

		if clibase.JSONOutputEnabled(cmd) {
			return clibase.WriteJSONResultError(cmd, result, failure)
		}

		if renderErr := renderDoctorTable(cmd, results); renderErr != nil {
			return errors.Join(failure, renderErr)
		}

		return clibase.NewCommandResultError(failure)
	}

	if !doctor.HasErrors(results) {
		if clibase.JSONOutputEnabled(cmd) {
			return clibase.WriteJSONResult(cmd, result)
		}

		return renderDoctorTable(cmd, results)
	}

	failure := &doctorChecksError{
		error: clibase.NewExitError(clibase.ExitCodeGeneral, ErrDoctorChecksFailed),
	}

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResultError(cmd, result, failure)
	}

	if renderErr := renderDoctorTable(cmd, results); renderErr != nil {
		return errors.Join(failure, renderErr)
	}

	return clibase.NewCommandResultError(failure)
}

func renderDoctorTable(cmd *cobra.Command, results []doctor.Result) error {
	rows := make([]prettytable.Row, 0, len(results))

	for _, r := range results {
		rows = append(rows, prettytable.Row{
			r.Check,
			r.Status,
			r.Reason,
			r.Message,
			r.Fix,
		})
	}

	columns := []clibase.TableColumn{
		{Heading: "Check", Fixed: true, MinWidth: colWidthDoctorCheck, Wrap: nil},
		{Heading: "Status", Fixed: true, MinWidth: colWidthDoctorStatus, Wrap: nil},
		{Heading: "Reason", Fixed: false, MinWidth: colWidthDoctorReason, Wrap: nil},
		{Heading: "Message", Fixed: false, MinWidth: colWidthDoctorMessage, Wrap: clibase.WrapIdentifier},
		{Heading: "Fix", Fixed: false, MinWidth: colWidthDoctorFix, Wrap: clibase.WrapIdentifier},
	}

	return clibase.WriteResponsiveTable(cmd, columns, rows, false)
}

func newDoctorResult(results []doctor.Result) doctorResultJSON {
	return doctorResultJSON{
		Checks:      results,
		HasErrors:   doctor.HasErrors(results),
		HasWarnings: doctor.HasWarnings(results),
	}
}
