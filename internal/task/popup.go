package task

import (
	"context"
	"errors"
	"fmt"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/config"
)

const (
	popupSessionPrefix = "task-"
	popupDetachKey     = "C-p"
	popupNoPrefix      = "None"
	rootKeyTable       = gotmux.KeyTable("root")
)

// PopupSessionName is the popup server session that holds a task's terminal.
func PopupSessionName(taskID string) string {
	return popupSessionPrefix + taskID
}

// EnsurePopupSession returns the task's popup session, starting it in the task directory on
// first use. Without a popup config, the server gets the few settings a popup terminal needs:
// no status line, no prefix, and the popup key closes it again.
func EnsurePopupSession(ctx context.Context, popup *gotmux.Server, cfg config.TasksConfig, record Record, size gotmux.Size) (gotmux.Session, error) {
	existing, err := popup.FindSession(ctx, PopupSessionName(record.ID))
	if err == nil {
		return existing, nil
	}

	if !errors.Is(err, gotmux.ErrNotFound) && !errors.Is(err, gotmux.ErrNoServer) {
		return gotmux.Session{}, taskError("open_task_popup", err)
	}

	created, err := popup.NewSession(ctx, gotmux.NewSessionOptions{
		Name: PopupSessionName(record.ID), Dir: record.Directory, Window: "", Program: gotmux.Program{}, Env: nil,
		TmuxEnv: Env(record), Size: size,
		Start: gotmux.StartPolicyAllowStart, Group: "",
	})
	if err != nil {
		return gotmux.Session{}, taskError("open_task_popup", err)
	}

	if err := created.Options().SetUser(ctx, OptionTask, record.ID); err != nil {
		return created, taskError("open_task_popup", err)
	}

	if cfg.PopupConfig == "" {
		if err := applyPopupDefaults(ctx, popup); err != nil {
			return created, taskError("configure_task_popup", err)
		}
	}

	return created, nil
}

// OpenRunWindow opens a window named after a task action in the task's popup session, running
// argv and then the popup's shell so the output stays readable. A previous run's window of the
// same action is replaced. The window is selected, so the popup opens on it.
func OpenRunWindow(ctx context.Context, popup *gotmux.Server, popupSession gotmux.Session, record Record, name string, argv []string) error {
	windows, err := popupSession.Windows(ctx)
	if err != nil {
		return taskError("open_task_action", fmt.Errorf("list popup windows: %w", err))
	}

	for _, link := range windows {
		info, err := link.Window().Info(ctx)
		if err != nil {
			return taskError("open_task_action", fmt.Errorf("inspect popup window: %w", err))
		}

		if info.Name == name {
			if err := link.Window().Kill(ctx); err != nil && !errors.Is(err, gotmux.ErrNotFound) {
				return taskError("open_task_action", fmt.Errorf("close previous run: %w", err))
			}
		}
	}

	shell, err := defaultShell(ctx, popup)
	if err != nil {
		return taskError("open_task_action", err)
	}

	if _, err := popupSession.NewWindow(ctx, gotmux.NewWindowOptions{
		Name: name, Dir: record.Directory, Program: shellProgram(shell, argv), Env: nil, TmuxEnv: Env(record),
		Index: nil, Before: false, After: false, Select: true,
	}); err != nil {
		return taskError("open_task_action", fmt.Errorf("open action window: %w", err))
	}

	return nil
}

func applyPopupDefaults(ctx context.Context, popup *gotmux.Server) error {
	options := popup.GlobalSessionOptions()

	if err := options.SetStatus(ctx, gotmux.StatusModeOff); err != nil {
		return fmt.Errorf("hide popup status: %w", err)
	}

	if err := options.SetPrefix(ctx, gotmux.Key(popupNoPrefix)); err != nil {
		return fmt.Errorf("clear popup prefix: %w", err)
	}

	detach, err := gotmux.NewCommand("detach-client")
	if err != nil {
		return fmt.Errorf("build popup detach: %w", err)
	}

	sequence, err := gotmux.Sequence(detach)
	if err != nil {
		return fmt.Errorf("build popup detach: %w", err)
	}

	if err := popup.Bind(ctx, rootKeyTable, gotmux.Key(popupDetachKey), sequence, gotmux.BindOptions{Repeat: false, Note: "", ClearNote: false}); err != nil {
		return fmt.Errorf("bind popup detach: %w", err)
	}

	return nil
}
