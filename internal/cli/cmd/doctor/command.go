package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
	cmdautosave "github.com/zigai/sesh/internal/cli/cmd/autosave"
	"github.com/zigai/sesh/internal/doctor"
	"github.com/zigai/sesh/internal/setup"
)

const (
	reportWidth    = 100
	minDetailWidth = 32
	labelGap       = 2
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
		DaemonState:       cmdautosave.DaemonUnselected,
		DaemonPhase:       "",
		InhibitorsHeld:    false,
		LogindDelayMillis: 0,
		ServiceDir:        "",
		Executable:        "",
		ValidateCommand:   nil,
		CustomFields:      nil,
	}

	workspace, err := clibase.ResolveWorkspace(cmd)
	if err != nil {
		return opts, err
	}

	for _, name := range workspace.Config.Display.UsedFieldNames() {
		opts.CustomFields = append(opts.CustomFields, doctor.CustomField{Name: name, Command: workspace.Config.Display.Fields[name].Command})
	}

	server, err := workspace.OpenTmux()
	if err != nil {
		return opts, err
	}

	opts.Server = server

	runtimeState, runtimeStatus := cmdautosave.DaemonRuntimeState(cmd.Context(), workspace.Socket)
	opts.DaemonState = runtimeState
	opts.DaemonPhase = string(runtimeStatus.Phase)
	opts.InhibitorsHeld = runtimeStatus.InhibitorsHeld
	opts.LogindDelayMillis = runtimeStatus.LogindDelayMillis
	opts.SnapshotDir = workspace.Repository
	opts.HistoryPath = workspace.History

	resolveServiceCommandCheck(cmd, &opts)

	return opts, nil
}

// resolveServiceCommandCheck points the service-commands check at the systemd user unit
// directory. Launchd agents are not checked.
func resolveServiceCommandCheck(cmd *cobra.Command, opts *doctor.Options) {
	if runtime.GOOS != setup.PlatformLinux {
		return
	}

	dir, err := setup.ServiceDirectory(setup.PlatformLinux)
	if err != nil {
		return
	}

	opts.ServiceDir = dir
	opts.ValidateCommand = commandLineValidator(cmd.Root())

	if executable, err := os.Executable(); err == nil {
		opts.Executable = executable
	}
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

	failure := &ChecksError{
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
		width = reportWidth
	}

	nameWidth := 0
	for _, r := range results {
		nameWidth = max(nameWidth, len(r.Check))
	}

	indent := strings.Repeat(" ", 2+nameWidth+labelGap)
	detailWidth := max(width-len(indent), minDetailWidth)

	var b strings.Builder

	for _, r := range results {
		lines := clibase.WrapText(r.Message, detailWidth)
		if r.Fix != "" {
			lines = append(lines, clibase.WrapText("fix: "+r.Fix, detailWidth)...)
		}

		if len(lines) == 0 {
			lines = []string{""}
		}

		fmt.Fprintf(&b, "%s %-*s%s%s\n", statusSymbol(r.Status), nameWidth, r.Check, strings.Repeat(" ", labelGap), lines[0])

		for _, line := range lines[1:] {
			b.WriteString(strings.TrimRight(indent+line, " ") + "\n")
		}
	}

	fmt.Fprintf(&b, "\n%s\n", summary(results))

	_, err := io.WriteString(output, b.String())

	return err
}

func statusSymbol(status string) string {
	switch status {
	case doctor.StatusOK:
		return "✓"
	case doctor.StatusWarning:
		return "!"
	default:
		return "✗"
	}
}

func summary(results []doctor.Result) string {
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

func newDoctorResult(results []doctor.Result) ResultJSON {
	return ResultJSON{
		Checks:      results,
		HasErrors:   doctor.HasErrors(results),
		HasWarnings: doctor.HasWarnings(results),
	}
}
