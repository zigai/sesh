package session

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	prettytable "github.com/jedib0t/go-pretty/v6/table"
	"github.com/samber/oops"
	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/cli/actioncli"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const (
	colWidthListSession = 16
	hoursPerDay         = 24
)

var errListPositionalArgs = errors.New("list accepts no positional arguments")

type sessionListJSONResult struct {
	Sessions []sessionJSON `json:"sessions"`
}

func LoadHistoryState(cmd *cobra.Command) (*session.State, error) {
	st, err := session.LoadState(actioncli.HistoryPath(cmd))
	if err != nil {
		return nil, oops.In("cli").With("operation", "load_state").Wrap(err)
	}

	return st, nil
}

func newListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "list",
		Short:       "List live tmux sessions",
		Annotations: map[string]string{clibase.AnnotationOperation: "list"},
		Long:        "List live tmux sessions ordered by activity and switch history.\n\nReads metadata only from the explicit tmux server socket and configured history state.",
		Args:        clibase.UsageArgs(cobra.NoArgs),
		RunE:        runList,
	}

	clibase.AddSocketFlag(cmd)
	cmd.Flags().String("current-pane", "", "Explicit current pane ID for active session resolution")
	cmd.Flags().String("history", "", clibase.HistoryFlagUsage)
	cmd.Flags().Bool("full", false, "Show complete values using an adaptive layout")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func ValidateListCommand(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return clibase.NewExitError(clibase.ExitCodeUsage, errListPositionalArgs)
	}

	socketPath := clibase.ResolveSocket(cmd)

	if !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath {
		return clibase.NewExitError(clibase.ExitCodeUsage, oops.In("tmux").Code("endpoint_required").With("operation", "open").Wrap(tmux.ErrEndpoint))
	}

	return nil
}

func formatActivity(t time.Time) string {
	return formatActivityAt(t, time.Now())
}

func formatActivityAt(t time.Time, now time.Time) string {
	if t.IsZero() {
		return ""
	}

	diff := now.Sub(t)
	if diff < time.Minute {
		return "now"
	}

	if diff < time.Hour {
		return fmt.Sprintf("%dm", int(diff.Minutes()))
	}

	if diff < hoursPerDay*time.Hour {
		return fmt.Sprintf("%dh", int(diff.Hours()))
	}

	return fmt.Sprintf("%dd", int(diff.Hours()/hoursPerDay))
}

func newSessionListJSONResult(sessions []session.Session) sessionListJSONResult {
	items := make([]sessionJSON, 0, len(sessions))
	for _, liveSession := range sessions {
		items = append(items, sessionJSON{
			ID:          liveSession.ID,
			Name:        liveSession.Name,
			NameRaw:     liveSession.NameRaw,
			WindowCount: liveSession.WindowCount,
			Attached:    liveSession.Attached,
			ActivityAt:  jsonTimestamp(liveSession.Activity),
			Current:     liveSession.IsCurrent,
		})
	}

	return sessionListJSONResult{Sessions: items}
}

func parseListOptions(cmd *cobra.Command) (*gotmux.Server, string, bool, error) {
	socketPath := clibase.ResolveSocket(cmd)

	server, err := tmux.Open(socketPath)
	if err != nil {
		if errors.Is(err, tmux.ErrEndpoint) {
			return nil, "", false, clibase.NewExitError(clibase.ExitCodeUsage, err)
		}

		return nil, "", false, oops.In("cli").With("operation", "open_tmux").Wrap(err)
	}

	currentPane, _ := cmd.Flags().GetString("current-pane")
	full, _ := cmd.Flags().GetBool("full")

	return server, currentPane, full, nil
}

func queryLiveSessions(ctx context.Context, server *gotmux.Server, currentPane string) ([]session.Session, error) {
	sessions, listErr := session.ListLive(ctx, server, currentPane)
	if listErr != nil {
		if errors.Is(listErr, gotmux.ErrNoServer) || session.ServerAbsent(listErr) {
			return nil, nil
		}

		isPartial := errors.Is(listErr, session.ErrCurrentSessionAmbiguous) || errors.Is(listErr, session.ErrCatalogLimitExceeded)
		if !isPartial && len(sessions) == 0 {
			return nil, oops.In("cli").With("operation", "list_sessions").Wrap(listErr)
		}

		return sessions, oops.In("cli").With("operation", "list_sessions").Wrap(listErr)
	}

	return sessions, nil
}

func renderListOutput(cmd *cobra.Command, server *gotmux.Server, sessions []session.Session, listErr error, full bool) error {
	if clibase.JSONOutputEnabled(cmd) {
		res := newSessionListJSONResult(sessions)
		if listErr != nil {
			return clibase.WriteJSONResultError(cmd, res, listErr)
		}

		return clibase.WriteJSONResult(cmd, res)
	}

	if err := renderSessionListTable(cmd, sessions, full); err != nil {
		return err
	}

	if listErr == nil {
		hintSavedSessions(cmd, server, len(sessions) == 0)
	}

	return listErr
}

// hintSavedSessions points at the newest snapshot on stderr when some of its sessions are not
// running, as after a reboot before anything was restored.
func hintSavedSessions(cmd *cobra.Command, server *gotmux.Server, empty bool) {
	repoPath, err := clibase.ResolveRepository(cmd)
	if err != nil {
		return
	}

	repo, err := store.OpenExisting(cmd.Context(), repoPath)
	if err != nil {
		return
	}

	var (
		count   int
		missing []string
	)

	if empty {
		count = engine.RestorableSessionCount(cmd.Context(), repo)
	} else {
		missing = engine.FindUnrestored(cmd.Context(), server, repo).Sessions
	}

	if err := repo.Close(); err != nil {
		return
	}

	switch {
	case count > 0:
		noun := "sessions"
		if count == 1 {
			noun = "session"
		}

		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%d saved %s available. If tmux stopped, start it on the same socket first.\nRestore with: %s\n", count, noun, restoreCommand(cmd, repoPath))
	case len(missing) > 0:
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Saved sessions not running: %s\ntmux started after the newest snapshot, so they were not restored.\nRestore with: %s\n", strings.Join(missing, ", "), restoreCommand(cmd, repoPath))
	}
}

func restoreCommand(cmd *cobra.Command, repoPath string) string {
	quote := func(value string) string {
		return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
	}

	command := "sesh snapshot restore --socket " + quote(clibase.ResolveSocket(cmd)) + " --repository " + quote(repoPath)
	if noConfig, _ := cmd.Flags().GetBool("no-config"); noConfig {
		command += " --no-config"
	} else if configPath, _ := cmd.Flags().GetString("config"); configPath != "" {
		command += " --config " + quote(configPath)
	}

	return command
}

func renderSessionListTable(cmd *cobra.Command, sessions []session.Session, full bool) error {
	rows := make([]prettytable.Row, 0, len(sessions))
	for _, s := range sessions {
		attached := "no"
		if s.Attached {
			attached = "yes"
		}

		rows = append(rows, prettytable.Row{
			s.Name,
			s.WindowCount,
			attached,
			formatActivity(s.Activity),
		})
	}

	columns := []clibase.TableColumn{
		{Heading: "Name", Fixed: false, MinWidth: colWidthListSession, Wrap: clibase.WrapSession},
		{Heading: "Windows", Fixed: true, MinWidth: 0, Wrap: nil},
		{Heading: "Attached", Fixed: true, MinWidth: 0, Wrap: nil},
		{Heading: "Activity", Fixed: true, MinWidth: 0, Wrap: nil},
	}

	return clibase.WriteResponsiveTable(cmd, columns, rows, full)
}

func runList(cmd *cobra.Command, _ []string) error {
	if err := cmd.Context().Err(); err != nil {
		return oops.In("cli").With("operation", "list_sessions").Wrap(err)
	}

	server, currentPane, full, err := parseListOptions(cmd)
	if err != nil {
		return err
	}

	if _, err := clibase.LoadCommandConfig(cmd.Context(), cmd); err != nil {
		return err
	}

	st, err := LoadHistoryState(cmd)
	if err != nil {
		return err
	}

	sessions, listErr := queryLiveSessions(cmd.Context(), server, currentPane)
	session.SortSessions(sessions, st)

	return renderListOutput(cmd, server, sessions, listErr, full)
}
