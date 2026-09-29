package cmddoctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
	cmdautosave "github.com/zigai/sesh/internal/cli/cmd/autosave"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/doctor"
	integration "github.com/zigai/sesh/internal/integration/tmux"
)

const (
	doctorReportWidth    = 100
	doctorMinDetailWidth = 32
	doctorLabelGap       = 2
)

var ErrDoctorChecksFailed = clibase.ErrDoctorChecksFailed

type ChecksError struct{ error }

func (e *ChecksError) Unwrap() error { return e.error }

func (e *ChecksError) OutcomeLogged() bool { return true }

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

	clibase.AddSocketFlag(cmd)
	cmd.Flags().String("repository", "", clibase.RepositoryFlagUsage)
	cmd.Flags().String("history", "", clibase.HistoryFlagUsage)
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

	if _, err := clibase.LoadCommandConfig(cmd.Context(), cmd); err != nil {
		return opts, err
	}

	server, err := integration.Open(clibase.ResolveSocket(cmd))
	if err != nil {
		return opts, oops.In("cli").With("operation", "open_tmux").Wrap(err)
	}

	opts.Server = server

	runtimeState, runtimeStatus := cmdautosave.AutosaveDaemonRuntimeState(cmd.Context(), cmd)
	opts.DaemonState = runtimeState
	opts.DaemonPhase = string(runtimeStatus.Phase)
	opts.InhibitorsHeld = runtimeStatus.InhibitorsHeld
	opts.LogindDelayMillis = runtimeStatus.LogindDelayMillis

	opts.SnapshotDir, err = clibase.ResolveRepository(cmd)
	if err != nil {
		return opts, err
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

		if renderErr := renderDoctorReport(cmd, results); renderErr != nil {
			return errors.Join(failure, renderErr)
		}

		return clibase.NewCommandResultError(failure)
	}

	if !doctor.HasErrors(results) {
		if clibase.JSONOutputEnabled(cmd) {
			return clibase.WriteJSONResult(cmd, result)
		}

		return renderDoctorReport(cmd, results)
	}

	failure := &doctorChecksError{
		error: clibase.NewExitError(clibase.ExitCodeGeneral, ErrDoctorChecksFailed),
	}

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResultError(cmd, result, failure)
	}

	if renderErr := renderDoctorReport(cmd, results); renderErr != nil {
		return errors.Join(failure, renderErr)
	}

	return clibase.NewCommandResultError(failure)
}

func renderDoctorReport(cmd *cobra.Command, results []doctor.Result) error {
	return clibase.CommandOutputError(writeDoctorReport(cmd.OutOrStdout(), clibase.CommandOutputWidth(cmd), results))
}

// writeDoctorReport prints one line per check with its message and fix wrapped beneath the
// check name. Reason codes stay in --json output, where scripts consume them.
func writeDoctorReport(output io.Writer, width int, results []doctor.Result) error {
	if width <= 0 {
		width = doctorReportWidth
	}

	nameWidth := 0
	for _, r := range results {
		nameWidth = max(nameWidth, len(r.Check))
	}

	indent := strings.Repeat(" ", 2+nameWidth+doctorLabelGap)
	detailWidth := max(width-len(indent), doctorMinDetailWidth)

	var b strings.Builder

	for _, r := range results {
		lines := clibase.WrapText(r.Message, detailWidth)
		if r.Fix != "" {
			lines = append(lines, clibase.WrapText("fix: "+r.Fix, detailWidth)...)
		}

		if len(lines) == 0 {
			lines = []string{""}
		}

		fmt.Fprintf(&b, "%s %-*s%s%s\n", doctorStatusSymbol(r.Status), nameWidth, r.Check, strings.Repeat(" ", doctorLabelGap), lines[0])

		for _, line := range lines[1:] {
			b.WriteString(strings.TrimRight(indent+line, " ") + "\n")
		}
	}

	fmt.Fprintf(&b, "\n%s\n", doctorSummary(results))

	_, err := io.WriteString(output, b.String())

	return err
}

func doctorStatusSymbol(status string) string {
	switch status {
	case doctor.StatusOK:
		return "✓"
	case doctor.StatusWarning:
		return "!"
	default:
		return "✗"
	}
}

func doctorSummary(results []doctor.Result) string {
	warnings, failures := 0, 0

	for _, r := range results {
		switch r.Status {
		case doctor.StatusOK:
		case doctor.StatusWarning:
			warnings++
		default:
			failures++
		}
	}

	return fmt.Sprintf("%d checks: %d ok, %d %s, %d %s",
		len(results), len(results)-warnings-failures,
		warnings, plural(warnings, "warning"), failures, plural(failures, "error"))
}

func plural(count int, noun string) string {
	if count == 1 {
		return noun
	}

	return noun + "s"
}

func newDoctorResult(results []doctor.Result) doctorResultJSON {
	return doctorResultJSON{
		Checks:      results,
		HasErrors:   doctor.HasErrors(results),
		HasWarnings: doctor.HasWarnings(results),
	}
}
