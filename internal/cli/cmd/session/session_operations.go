package cmdsession

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/samber/oops"
	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const annotationOwnedValue = "true"

var (
	errNoSessionSpecified = errors.New("no session specified")
	errInvalidSessionName = errors.New("invalid session name")
	errDirectoryNotFound  = errors.New("directory does not exist")
	errDirectoryNotDir    = errors.New("path is not a directory")
)

type sessionRuntime struct {
	workflow *session.Workflow
	repo     *store.Repository
}

type sessionNewJSONResult struct {
	Session   string `json:"session"`
	Directory string `json:"directory"`
	Created   bool   `json:"created"`
}

type sessionRenameJSONResult struct {
	PreviousName string `json:"previousName"`
	Name         string `json:"name"`
	Renamed      bool   `json:"renamed"`
}

type sessionKillJSONResult struct {
	Session string `json:"session"`
	Killed  bool   `json:"killed"`
}

type sessionDeleteJSONResult struct {
	Session      string `json:"session"`
	LiveKilled   bool   `json:"liveKilled"`
	SavedDeleted bool   `json:"savedDeleted"`
}

type sessionSwitchJSONResult struct {
	Session  string `json:"session"`
	Switched bool   `json:"switched"`
}

type sessionJSON struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	NameRaw     model.Bytes `json:"nameRaw,omitempty"`
	WindowCount int         `json:"windowCount"`
	Attached    bool        `json:"attached"`
	ActivityAt  *string     `json:"activityAt"`
	Current     bool        `json:"current"`
}

type sessionHistorySummaryJSON struct {
	Name      string      `json:"name"`
	NameRaw   model.Bytes `json:"nameRaw,omitempty"`
	PaneCount int         `json:"paneCount"`
	LineCount int         `json:"lineCount"`
}

type paneHistoryJSON struct {
	SessionName string      `json:"sessionName"`
	NameRaw     model.Bytes `json:"nameRaw,omitempty"`
	WindowIndex string      `json:"windowIndex"`
	PaneIndex   string      `json:"paneIndex"`
	LineCount   int         `json:"lineCount"`
}

func (rt *sessionRuntime) close() error {
	if rt == nil || rt.repo == nil {
		return nil
	}

	if err := rt.repo.Close(); err != nil {
		return oops.In("cli").With("operation", "close_repository").Wrap(err)
	}

	return nil
}

func NewActionSessionDeleteCommand() *cobra.Command {
	cmd := newSessionDeleteCommand()
	cmd.Use = "session-delete"

	return cmd
}

func NewActionSessionKillCommand() *cobra.Command {
	cmd := newSessionKillCommand()
	cmd.Use = "session-kill"

	return cmd
}

func NewActionSessionNewCommand() *cobra.Command {
	cmd := newSessionNewCommand()
	cmd.Use = "session-new"

	return cmd
}

func NewActionSessionRenameCommand() *cobra.Command {
	cmd := newSessionRenameCommand()
	cmd.Use = "session-rename"

	return cmd
}

func NewActionSessionSwitchCommand() *cobra.Command {
	cmd := newSessionSwitchCommand()
	cmd.Use = "session-switch"

	return cmd
}

func newSessionDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete [session]",
		Short: "Delete a session from live tmux and snapshot repository",
		Long: "Delete a session from live tmux and snapshot repository.\n\n" +
			"When [session] is omitted, deletes the invoking session. If attached, refuses unless --force\n" +
			"is specified. When --json is specified, delete outcomes are finite and machine-readable.\n" +
			"Live state is killed first to prevent concurrent saves from re-creating deleted sessions;\n" +
			"attachments or save races are revalidated before destruction.",
		Args: clibase.UsageArgs(cobra.MaximumNArgs(1)),
		RunE: runSessionDelete,
	}
	cmd.Annotations = map[string]string{clibase.AnnotationSuccessOwned: annotationOwnedValue}

	cmd.Flags().String("socket", "", "Explicit absolute path to tmux server socket (defaults to the ambient tmux server)")
	cmd.Flags().String("repository", "", "Native repository directory holding saved generations (defaults to native data directory)")
	cmd.Flags().String("lease-root", "", "Directory holding shared cooperative endpoint leases")
	cmd.Flags().BoolP("force", "f", false, "Delete even if session has attached clients")
	cmd.Flags().String("history", "", "History state JSON file path")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func newSessionKillCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kill [session]",
		Short: "Kill a live tmux session",
		Long:  "Kill a live tmux session. Refuses if attached unless --force is specified.",
		Args:  clibase.UsageArgs(cobra.MaximumNArgs(1)),
		RunE:  runSessionKill,
	}
	cmd.Annotations = map[string]string{clibase.AnnotationSuccessOwned: annotationOwnedValue}

	cmd.Flags().String("socket", "", "Explicit absolute path to tmux server socket (defaults to the ambient tmux server)")
	cmd.Flags().BoolP("force", "f", false, "Kill even if session has attached clients")
	cmd.Flags().String("history", "", "History state JSON file path")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func newSessionNewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "new <name>",
		Short: "Create or enter a live tmux session",
		Long: "Create or enter a live tmux session in the specified or current directory.\n\n" +
			"In interactive use, an existing session is entered without modification: inside tmux\n" +
			"the invoking client is switched and verified; outside tmux the caller's terminal is\n" +
			"attached. Terminal stream validation prevents creating a session when interactive attach\n" +
			"cannot succeed. When --json is specified, session creation is finite and create-only,\n" +
			"never interleaving terminal bytes into JSON output.",
		Args: clibase.UsageArgs(cobra.ExactArgs(1)),
		RunE: runSessionNew,
	}
	cmd.Annotations = map[string]string{clibase.AnnotationSuccessOwned: annotationOwnedValue}

	cmd.Flags().String("socket", "", "Explicit absolute path to tmux server socket (defaults to the ambient tmux server)")
	cmd.Flags().StringP("dir", "d", "", "Initial working directory for the session")
	cmd.Flags().String("history", "", "History state JSON file path")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func newSessionRenameCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rename <new-name>",
		Short: "Rename a live tmux session",
		Long:  "Rename a live tmux session and update history state.",
		Args:  clibase.UsageArgs(cobra.ExactArgs(1)),
		RunE:  runSessionRename,
	}
	cmd.Annotations = map[string]string{clibase.AnnotationSuccessOwned: annotationOwnedValue}

	cmd.Flags().String("socket", "", "Explicit absolute path to tmux server socket (defaults to the ambient tmux server)")
	cmd.Flags().StringP("session", "s", "", "Session to rename (defaults to current)")
	cmd.Flags().String("history", "", "History state JSON file path")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func newSessionSwitchCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "switch <session>",
		Short: "Switch the invoking tmux client to a live session",
		Long: "Switch the invoking tmux client to a live session and record switch history.\n\n" +
			"The invoking client is selected explicitly: it must be the single attached client\n" +
			"displaying the session that owns the caller's pane. The switch is verified before it is\n" +
			"reported as successful.",
		Args: clibase.UsageArgs(cobra.ExactArgs(1)),
		RunE: runSessionSwitch,
	}
	cmd.Annotations = map[string]string{clibase.AnnotationSuccessOwned: annotationOwnedValue}

	cmd.Flags().String("socket", "", "Explicit absolute path to tmux server socket (defaults to the ambient tmux server)")
	cmd.Flags().String("history", "", "History state JSON file path")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func closeRepository(repo *store.Repository) error {
	if repo == nil {
		return nil
	}

	if err := repo.Close(); err != nil {
		return oops.In("cli").With("operation", "close_repository").Wrap(err)
	}

	return nil
}

func commandTerminalStreams(cmd *cobra.Command) gotmux.TerminalStreams {
	var streams gotmux.TerminalStreams

	if in, ok := cmd.InOrStdin().(*os.File); ok {
		streams.In = in
	}

	if out, ok := cmd.OutOrStdout().(*os.File); ok {
		streams.Out = out
	}

	if errFile, ok := cmd.ErrOrStderr().(*os.File); ok {
		streams.Err = errFile
	}

	return streams
}

func jsonTimestamp(value time.Time) *string {
	if value.IsZero() {
		return nil
	}

	formatted := value.UTC().Format(time.RFC3339Nano)

	return &formatted
}

func openSessionRepository(ctx context.Context, cmd *cobra.Command, cfg *config.Config) (*store.Repository, bool, error) {
	repoFlag, _ := cmd.Flags().GetString("repository")
	if repoFlag != "" {
		absRepo, err := filepath.Abs(repoFlag)
		if err != nil {
			return nil, false, oops.In("cli").With("operation", "resolve_repository_path").Wrap(err)
		}

		repo, err := store.OpenExisting(ctx, absRepo)
		if err != nil {
			return nil, false, oops.In("cli").With("operation", "open_repository").Wrapf(err, "open repository")
		}

		return repo, true, nil
	}

	dataDir, err := config.GetDataDir(cfg)
	if err != nil {
		return nil, false, oops.In("cli").With("operation", "resolve_data_dir").Wrap(err)
	}

	defaultRepoPath := filepath.Join(dataDir, "snapshots")

	repo, err := store.OpenExisting(ctx, defaultRepoPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}

		return nil, false, oops.In("cli").With("operation", "open_repository").Wrapf(err, "open repository")
	}

	return repo, true, nil
}

func openSessionRuntime(cmd *cobra.Command) (*sessionRuntime, error) {
	socketPath := resolveSessionSocket(cmd)
	if socketPath == "" {
		return nil, clibase.NewExitError(clibase.ExitCodeUsage, clibase.ErrSocketRequired)
	}

	server, err := integration.Open(socketPath)
	if err != nil {
		return nil, oops.In("cli").With("operation", "open_tmux").Wrap(err)
	}

	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return nil, err
	}

	ctx := clibase.CommandContext(cmd)

	var (
		repo      *store.Repository
		leaseRoot string
	)

	if cmd.Flags().Lookup("repository") != nil {
		var (
			repoErr  error
			leaseErr error
		)

		repo, _, repoErr = openSessionRepository(ctx, cmd, cfg)
		if repoErr != nil {
			return nil, repoErr
		}

		leaseRoot, leaseErr = resolveSessionLeaseRoot(cmd, cfg)
		if leaseErr != nil {
			return nil, errors.Join(leaseErr, closeRepository(repo))
		}
	}

	wf := session.NewWorkflowWithOptions(session.WorkflowOptions{
		Server:      server,
		Repository:  repo,
		LeaseRoot:   leaseRoot,
		HistoryPath: resolveHistoryPath(cmd),
		Hooks:       cfg.Hooks,
		HookRunner:  nil,
		Getenv:      nil,
		Streams:     commandTerminalStreams(cmd),
		Source:      "cli",
	})

	return &sessionRuntime{workflow: wf, repo: repo}, nil
}

func resolveHistoryPath(cmd *cobra.Command) string {
	historyPath, _ := cmd.Flags().GetString("history")
	if historyPath != "" {
		absHist, err := filepath.Abs(historyPath)
		if err == nil {
			return absHist
		}
	}

	xdg := config.GetXDGPaths()

	return filepath.Join(xdg.StateHome, "sesh", "history.json")
}

func resolveSessionDirectory(cmd *cobra.Command) (string, error) {
	dir, _ := cmd.Flags().GetString("dir")
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", oops.In("cli").With("operation", "get_working_directory").Wrap(err)
		}

		return cwd, nil
	}

	cleanDir := filepath.Clean(dir)

	info, err := os.Stat(cleanDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errDirectoryNotFound
		}

		return "", oops.In("cli").With("operation", "stat_directory").Wrap(err)
	}

	if !info.IsDir() {
		return "", errDirectoryNotDir
	}

	return cleanDir, nil
}

func resolveSessionLeaseRoot(cmd *cobra.Command, cfg *config.Config) (string, error) {
	leaseFlag, _ := cmd.Flags().GetString("lease-root")
	if leaseFlag != "" {
		abs, err := filepath.Abs(leaseFlag)
		if err != nil {
			return "", oops.In("cli").With("operation", "resolve_lease_root").Wrap(err)
		}

		dir, err := privatefs.Ensure(abs)
		if err != nil {
			return "", oops.In("cli").With("operation", "ensure_lease_root").Wrap(err)
		}

		if err := dir.Close(); err != nil {
			return "", oops.In("cli").With("operation", "close_lease_root").Wrap(err)
		}

		return abs, nil
	}

	dataDir, err := config.GetDataDir(cfg)
	if err != nil {
		return "", oops.In("cli").With("operation", "resolve_data_dir").Wrap(err)
	}

	defaultLeaseRoot := filepath.Join(dataDir, "leases")

	dir, err := privatefs.Ensure(defaultLeaseRoot)
	if err != nil {
		return "", oops.In("cli").With("operation", "ensure_lease_root").Wrap(err)
	}

	if err := dir.Close(); err != nil {
		return "", oops.In("cli").With("operation", "close_lease_root").Wrap(err)
	}

	return defaultLeaseRoot, nil
}

func resolveSessionSocket(cmd *cobra.Command) string {
	socketPath, _ := cmd.Flags().GetString("socket")
	if socketPath != "" {
		return socketPath
	}

	tmuxEnv := os.Getenv("TMUX")
	if tmuxEnv == "" {
		return ""
	}

	parts := strings.Split(tmuxEnv, ",")
	if len(parts) == 0 || parts[0] == "" {
		return ""
	}

	return parts[0]
}

func runSessionDelete(cmd *cobra.Command, args []string) error {
	var target string
	if len(args) > 0 {
		target = args[0]
	}

	rt, err := openSessionRuntime(cmd)
	if err != nil {
		return err
	}

	if target == "" {
		return clibase.NewExitError(clibase.ExitCodeUsage, errNoSessionSpecified)
	}

	force, _ := cmd.Flags().GetBool("force")

	outcome, deleteErr := rt.workflow.DeleteSessionEverywhere(clibase.CommandContext(cmd), target, force)
	closeErr := rt.close()

	result := sessionDeleteJSONResult{
		Session:      target,
		LiveKilled:   outcome.LiveKilled,
		SavedDeleted: outcome.SavedGenerations > 0,
	}

	if err := errors.Join(deleteErr, closeErr); err != nil {
		if clibase.JSONOutputEnabled(cmd) {
			return clibase.WriteJSONResultError(cmd, result, err)
		}

		if err := writePartialDeleteSummary(cmd, target, outcome); err != nil {
			return errors.Join(deleteErr, closeErr, err)
		}

		return errors.Join(deleteErr, closeErr)
	}

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResult(cmd, result)
	}

	return clibase.WriteOutput(cmd, "Deleted session %s (live killed: %t, saved generations: %d)\n",
		target, outcome.LiveKilled, outcome.SavedGenerations)
}

func runSessionKill(cmd *cobra.Command, args []string) error {
	var target string
	if len(args) > 0 {
		target = args[0]
	}

	rt, err := openSessionRuntime(cmd)
	if err != nil {
		return err
	}

	if target == "" {
		return clibase.NewExitError(clibase.ExitCodeUsage, errNoSessionSpecified)
	}

	force, _ := cmd.Flags().GetBool("force")

	killErr := rt.workflow.KillSession(clibase.CommandContext(cmd), target, force)
	closeErr := rt.close()

	if err := errors.Join(killErr, closeErr); err != nil {
		if clibase.JSONOutputEnabled(cmd) {
			return clibase.WriteJSONResultError(cmd, sessionKillJSONResult{Session: target, Killed: false}, err)
		}

		return err
	}

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResult(cmd, sessionKillJSONResult{Session: target, Killed: true})
	}

	return clibase.WriteOutput(cmd, "Killed session %s\n", target)
}

func runSessionNew(cmd *cobra.Command, args []string) error {
	name := args[0]
	if name == "" {
		return clibase.NewExitError(clibase.ExitCodeUsage, errInvalidSessionName)
	}

	dir, err := resolveSessionDirectory(cmd)
	if err != nil {
		return err
	}

	rt, err := openSessionRuntime(cmd)
	if err != nil {
		return err
	}

	ctx := clibase.CommandContext(cmd)

	if clibase.JSONOutputEnabled(cmd) {
		sess, createErr := rt.workflow.CreateSession(ctx, name, dir)
		closeErr := rt.close()

		result := sessionNewJSONResult{
			Session:   name,
			Directory: dir,
			Created:   createErr == nil,
		}

		if err := errors.Join(createErr, closeErr); err != nil {
			return clibase.WriteJSONResultError(cmd, result, err)
		}

		_ = sess

		return clibase.WriteJSONResult(cmd, result)
	}

	outcome, enterErr := rt.workflow.EnterOrCreateSession(ctx, name, dir)
	closeErr := rt.close()

	if err := errors.Join(enterErr, closeErr); err != nil {
		return err
	}

	if !outcome.Created {
		return clibase.WriteOutput(cmd, "Entered session %s\n", name)
	}

	return clibase.WriteOutput(cmd, "Created session %s (%s) in %s\n", name, outcome.SessionID, dir)
}

func runSessionRename(cmd *cobra.Command, args []string) error {
	newName := args[0]
	oldName, _ := cmd.Flags().GetString("session")

	if oldName == "" {
		return clibase.NewExitError(clibase.ExitCodeUsage, errNoSessionSpecified)
	}

	rt, err := openSessionRuntime(cmd)
	if err != nil {
		return err
	}

	outcome, renameErr := rt.workflow.RenameSessionOutcome(clibase.CommandContext(cmd), oldName, newName)
	closeErr := rt.close()

	result := sessionRenameJSONResult{
		PreviousName: oldName,
		Name:         newName,
		Renamed:      outcome.Renamed,
	}

	if err := errors.Join(renameErr, closeErr); err != nil {
		if clibase.JSONOutputEnabled(cmd) {
			return clibase.WriteJSONResultError(cmd, result, err)
		}

		return err
	}

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResult(cmd, result)
	}

	return clibase.WriteOutput(cmd, "Renamed session %s to %s\n", oldName, newName)
}

func runSessionSwitch(cmd *cobra.Command, args []string) error {
	target := args[0]
	if target == "" {
		return clibase.NewExitError(clibase.ExitCodeUsage, errNoSessionSpecified)
	}

	rt, err := openSessionRuntime(cmd)
	if err != nil {
		return err
	}

	outcome, switchErr := rt.workflow.SwitchSessionOutcome(clibase.CommandContext(cmd), target)
	closeErr := rt.close()

	result := sessionSwitchJSONResult{
		Session:  target,
		Switched: outcome.Started,
	}

	if err := errors.Join(switchErr, closeErr); err != nil {
		if clibase.JSONOutputEnabled(cmd) {
			return clibase.WriteJSONResultError(cmd, result, err)
		}

		return err
	}

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResult(cmd, result)
	}

	return clibase.WriteOutput(cmd, "Switched to session %s\n", target)
}

func writePartialDeleteSummary(cmd *cobra.Command, target string, outcome session.DeleteOutcome) error {
	if !outcome.LiveKilled && outcome.SavedGenerations == 0 && len(outcome.PreservedGenerations) == 0 {
		return nil
	}

	_, err := fmt.Fprintf(
		cmd.ErrOrStderr(),
		"Partial deletion of session %s: live killed %t, saved generations %d, saved sessions %d, preserved generations %d\n",
		target,
		outcome.LiveKilled,
		outcome.SavedGenerations,
		outcome.SavedSessions,
		len(outcome.PreservedGenerations),
	)
	if err != nil {
		return oops.In("cli").With("operation", "write_partial_delete_summary").Wrap(err)
	}

	return nil
}
