//go:build linux || darwin

package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/cli/actioncli"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/session"
)

const annotationOwnedValue = "true"

var (
	errInvalidSessionName = errors.New("invalid session name")
	errDirectoryNotFound  = errors.New("directory does not exist")
	errDirectoryNotDir    = errors.New("path is not a directory")
)

type sessionJSON struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	NameRaw     session.RawBytes `json:"nameRaw,omitempty"`
	WindowCount int              `json:"windowCount"`
	Attached    bool             `json:"attached"`
	ActivityAt  *string          `json:"activityAt"`
	Current     bool             `json:"current"`
}

type historySummaryJSON struct {
	Name      string           `json:"name"`
	NameRaw   session.RawBytes `json:"nameRaw,omitempty"`
	PaneCount int              `json:"paneCount"`
	LineCount int              `json:"lineCount"`
}

type paneHistoryJSON struct {
	SessionName string           `json:"sessionName"`
	NameRaw     session.RawBytes `json:"nameRaw,omitempty"`
	WindowIndex string           `json:"windowIndex"`
	PaneIndex   string           `json:"paneIndex"`
	LineCount   int              `json:"lineCount"`
}

func newSessionCreateCommand() *cobra.Command {
	return actioncli.Command(action.SessionCreate, actioncli.Options[action.CreateSession, action.CreatedSession]{
		Operation: "create", SuccessOwned: true, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
	})
}

func newSessionRenameCommand() *cobra.Command {
	return actioncli.Command(action.SessionRename, actioncli.Options[action.RenameSession, action.RenamedSession]{
		Operation: "rename", SuccessOwned: true, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
	})
}

func newSessionKillCommand() *cobra.Command {
	return actioncli.Command(action.SessionKill, actioncli.Options[action.KillSession, action.KilledSession]{
		Operation: "kill", SuccessOwned: true, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
	})
}

func newSessionDeleteCommand() *cobra.Command {
	return actioncli.Command(action.SessionDelete, actioncli.Options[action.DeleteSession, action.DeletedSession]{
		Operation: "delete", SuccessOwned: true, Configure: nil, Prepare: nil, Frontend: nil, Render: renderSessionDelete,
	})
}

func newSessionSwitchCommand() *cobra.Command {
	return actioncli.Command(action.SessionSwitch, actioncli.Options[action.SwitchSession, action.SwitchedSession]{
		Operation: "switch", SuccessOwned: true, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
	})
}

func newSessionNewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "new <name>",
		Short: "Create or enter a live tmux session",
		Long: "Create or enter a live tmux session in the specified or current directory.\n\n" +
			"An existing session is entered without modification: inside tmux the invoking client is\n" +
			"switched and verified; outside tmux the caller's terminal is attached. Terminal stream\n" +
			"validation prevents creating a session when interactive attach cannot succeed. Use\n" +
			"'sesh session create' to create a session without entering it.",
		Args: clibase.UsageArgs(cobra.ExactArgs(1)),
		RunE: runSessionNew,
	}
	cmd.Annotations = map[string]string{clibase.AnnotationOperation: "new", clibase.AnnotationSuccessOwned: annotationOwnedValue}

	actioncli.AddEnvironmentFlags(cmd)
	cmd.Flags().StringP("dir", "d", "", "Initial working directory for the session")

	return cmd
}

func renderSessionDelete(cmd *cobra.Command, output action.DeletedSession, err error) error {
	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResultError(cmd, output, err)
	}

	if err == nil {
		return clibase.WriteOutput(cmd, "%s\n", action.SessionDelete.Report(output))
	}

	if !output.LiveKilled && output.SavedGenerations == 0 && len(output.PreservedGenerations) == 0 {
		return err
	}

	_, writeErr := fmt.Fprintf(
		cmd.ErrOrStderr(),
		"Partial deletion of session %s: live killed %t, saved generations %d, saved sessions %d, preserved generations %d\n",
		output.Name,
		output.LiveKilled,
		output.SavedGenerations,
		output.SavedSessions,
		len(output.PreservedGenerations),
	)
	if writeErr != nil {
		return errors.Join(err, oops.In("cli").With("operation", "write_partial_delete_summary").Wrap(writeErr))
	}

	return err
}

func jsonTimestamp(value time.Time) *string {
	if value.IsZero() {
		return nil
	}

	formatted := value.UTC().Format(time.RFC3339Nano)

	return &formatted
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
			return "", oops.In("cli").With("operation", "stat_directory").Wrap(errDirectoryNotFound)
		}

		return "", oops.In("cli").With("operation", "stat_directory").Wrap(err)
	}

	if !info.IsDir() {
		return "", oops.In("cli").With("operation", "stat_directory").Wrap(errDirectoryNotDir)
	}

	return cleanDir, nil
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

	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return err
	}

	runtime, err := actioncli.OpenRuntime(cmd, cfg)
	if err != nil {
		return err
	}

	outcome, enterErr := runtime.Local.Workflow().EnterOrCreateSession(clibase.CommandContext(cmd), name, dir)
	if err := errors.Join(enterErr, runtime.Close()); err != nil {
		return err
	}

	if !outcome.Created {
		return clibase.WriteOutput(cmd, "Entered session %s\n", name)
	}

	return clibase.WriteOutput(cmd, "Created session %s (%s) in %s\n", name, outcome.SessionID, dir)
}
