package cmdsession

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"

	prettytable "github.com/jedib0t/go-pretty/v6/table"
	"github.com/samber/oops"
	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/session"
)

const colWidthHistorySession = 20

var errHistoryPositionalArgs = errors.New("history accepts no positional arguments")

type sessionHistoryJSONResult struct {
	Mode     string                      `json:"mode"`
	Sessions []sessionHistorySummaryJSON `json:"sessions"`
}

type paneHistoryJSONResult struct {
	Mode  string            `json:"mode"`
	Panes []paneHistoryJSON `json:"panes"`
}

func NewHistoryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Show tmux history lines by session",
		Long:  "Show tmux history lines by session or per pane.\n\nReads metadata only from the explicit tmux server socket.",
		Args:  clibase.UsageArgs(cobra.NoArgs),
		RunE:  runHistory,
	}

	cmd.Flags().String("socket", "", "Explicit absolute path to tmux server socket (required)")
	cmd.Flags().Bool("panes", false, "Show per-pane history lines")
	cmd.Flags().Bool("full", false, "Show complete values using an adaptive layout")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func ValidateHistoryCommand(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return clibase.NewExitError(clibase.ExitCodeUsage, errHistoryPositionalArgs)
	}

	socketPath, _ := cmd.Flags().GetString("socket")
	if socketPath == "" {
		return clibase.NewExitError(clibase.ExitCodeUsage, clibase.ErrSocketRequired)
	}

	if !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath {
		return clibase.NewExitError(clibase.ExitCodeUsage, oops.In("tmux").Code("endpoint_required").With("operation", "open").Wrap(tmux.ErrEndpoint))
	}

	return nil
}

func newPaneHistoryJSONResult(panes []session.PaneHistory) paneHistoryJSONResult {
	items := make([]paneHistoryJSON, 0, len(panes))
	for _, pane := range panes {
		items = append(items, paneHistoryJSON{
			SessionName: pane.SessionName,
			NameRaw:     pane.NameRaw,
			WindowIndex: strconv.Itoa(pane.WindowIndex),
			PaneIndex:   strconv.Itoa(pane.PaneIndex),
			LineCount:   pane.Lines,
		})
	}

	return paneHistoryJSONResult{Mode: "panes", Panes: items}
}

func newSessionHistoryJSONResult(summaries []session.SessionHistorySummary) sessionHistoryJSONResult {
	items := make([]sessionHistorySummaryJSON, 0, len(summaries))
	for _, summary := range summaries {
		items = append(items, sessionHistorySummaryJSON{
			Name:      summary.SessionName,
			NameRaw:   summary.NameRaw,
			PaneCount: summary.PaneCount,
			LineCount: summary.Lines,
		})
	}

	return sessionHistoryJSONResult{Mode: "sessions", Sessions: items}
}

func wrapHistoryListError(err error) error {
	if err == nil {
		return nil
	}

	if _, ok := oops.AsOops(err); ok {
		return err
	}

	return oops.In("cli").With("operation", "history").Wrap(err)
}

func renderHistoryPanes(cmd *cobra.Command, panes []session.PaneHistory, listErr error, full bool) error {
	session.SortPaneHistory(panes)

	if listErr != nil {
		listErr = wrapHistoryListError(listErr)
	}

	if clibase.JSONOutputEnabled(cmd) {
		res := newPaneHistoryJSONResult(panes)
		if listErr != nil {
			return clibase.WriteJSONResultError(cmd, res, listErr)
		}

		return clibase.WriteJSONResult(cmd, res)
	}

	if err := renderPaneHistoryTable(cmd, panes, full); err != nil {
		return err
	}

	return listErr
}

func renderHistorySessions(cmd *cobra.Command, panes []session.PaneHistory, listErr error, full bool) error {
	summaries := session.SummarizePaneHistory(panes)

	if listErr != nil {
		listErr = wrapHistoryListError(listErr)
	}

	if clibase.JSONOutputEnabled(cmd) {
		res := newSessionHistoryJSONResult(summaries)
		if listErr != nil {
			return clibase.WriteJSONResultError(cmd, res, listErr)
		}

		return clibase.WriteJSONResult(cmd, res)
	}

	if err := renderSessionHistoryTable(cmd, summaries, full); err != nil {
		return err
	}

	return listErr
}

func renderPaneHistoryTable(cmd *cobra.Command, panes []session.PaneHistory, full bool) error {
	rows := make([]prettytable.Row, 0, len(panes))
	for _, pane := range panes {
		rows = append(rows, prettytable.Row{
			pane.SessionName,
			fmt.Sprintf("%d.%d", pane.WindowIndex, pane.PaneIndex),
			pane.Lines,
		})
	}

	columns := []clibase.TableColumn{
		{Heading: "Session", Fixed: false, MinWidth: colWidthHistorySession, Wrap: clibase.WrapSession},
		{Heading: "Pane", Fixed: true, MinWidth: 0, Wrap: clibase.WrapIdentifier},
		{Heading: "Lines", Fixed: true, MinWidth: 0, Wrap: nil},
	}

	return clibase.WriteResponsiveTable(cmd, columns, rows, full)
}

func renderSessionHistoryTable(cmd *cobra.Command, summaries []session.SessionHistorySummary, full bool) error {
	rows := make([]prettytable.Row, 0, len(summaries))
	for _, summary := range summaries {
		rows = append(rows, prettytable.Row{
			summary.SessionName,
			summary.PaneCount,
			summary.Lines,
		})
	}

	columns := []clibase.TableColumn{
		{Heading: "Session", Fixed: false, MinWidth: colWidthHistorySession, Wrap: clibase.WrapSession},
		{Heading: "Panes", Fixed: true, MinWidth: 0, Wrap: nil},
		{Heading: "Lines", Fixed: true, MinWidth: 0, Wrap: nil},
	}

	return clibase.WriteResponsiveTable(cmd, columns, rows, full)
}

func runHistory(cmd *cobra.Command, _ []string) error {
	if err := cmd.Context().Err(); err != nil {
		return oops.In("cli").With("operation", "history").Wrap(err)
	}

	socketPath, _ := cmd.Flags().GetString("socket")
	if socketPath == "" {
		return clibase.NewExitError(clibase.ExitCodeUsage, clibase.ErrSocketRequired)
	}

	server, err := tmux.Open(socketPath)
	if err != nil {
		if errors.Is(err, tmux.ErrEndpoint) {
			return clibase.NewExitError(clibase.ExitCodeUsage, err)
		}

		return oops.In("cli").With("operation", "open_tmux").Wrap(err)
	}

	showPanes, _ := cmd.Flags().GetBool("panes")
	full, _ := cmd.Flags().GetBool("full")

	panes, listErr := session.ListPaneHistory(cmd.Context(), server)
	if listErr != nil && !errors.Is(listErr, session.ErrPaneHistoryLimitExceeded) {
		if errors.Is(listErr, gotmux.ErrNoServer) || session.ServerAbsent(listErr) {
			panes = nil
			listErr = nil
		} else {
			return oops.In("cli").With("operation", "history").Wrap(listErr)
		}
	}

	if showPanes {
		return renderHistoryPanes(cmd, panes, listErr, full)
	}

	return renderHistorySessions(cmd, panes, listErr, full)
}
