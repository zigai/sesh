//go:build linux || darwin

package session

import (
	"context"
	"errors"
	"os/exec"

	"github.com/charmbracelet/x/term"
	"github.com/rs/zerolog"
	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"
)

func initialAttachOutcome(name string) EnterOutcome {
	return EnterOutcome{
		SessionName: name,
		SessionID:   "",
		Action:      EnterAttach,
		Created:     false,
		Started:     false,
		Client:      "",
		Hooks:       HookSummary{Attempted: 0, Completed: 0, Failed: 0, TimedOut: 0},
	}
}

func (w *Workflow) AttachSession(ctx context.Context, name string) (EnterOutcome, error) {
	out := initialAttachOutcome(name)

	sess, streams, sessionID, prepErr := w.prepareAttachTarget(ctx, name)
	if prepErr != nil {
		return out, w.fail(ctx, "attach", prepErr)
	}

	out.SessionID = sessionID

	preHooks, preErr := w.runAttachPreHook(ctx, name, sessionID)
	if preErr != nil {
		return out, w.fail(ctx, "attach", preErr)
	}

	out.Hooks = preHooks

	cmd, cmdErr := sess.PrepareAttach(ctx, streams, gotmux.AttachOptions{
		ReadOnly:            false,
		PreserveEnvironment: false,
		Detach:              gotmux.DetachNone,
		Dir:                 "",
		Flags:               nil,
	})
	if cmdErr != nil {
		return out, w.fail(ctx, "attach", oops.In("session").Code("attach_failed").With("operation", "prepare_attach").Wrap(cmdErr))
	}

	state, stateErr := term.GetState(streams.In.Fd())
	if stateErr != nil {
		return out, w.fail(ctx, "attach", oops.In("session").Code("attach_failed").With("operation", "read_terminal_state").Wrap(stateErr))
	}

	if startErr := cmd.Start(); startErr != nil {
		restoreErr := wrapTerminalRestore(term.Restore(streams.In.Fd(), state))

		return out, w.fail(ctx, "attach", errors.Join(
			oops.In("session").Code("attach_failed").With("operation", "start_attach").Wrap(startErr),
			restoreErr,
		))
	}

	out.Started = true

	postHooks, postErr := w.runAttachPostHook(ctx, name, sessionID, cmd)
	out.Hooks = mergeHookSummaries(out.Hooks, postHooks)

	waitErr := attachWaitError(ctx, cmd.Wait())
	restoreErr := wrapTerminalRestore(term.Restore(streams.In.Fd(), state))

	if attachErr := errors.Join(waitErr, postErr, restoreErr); attachErr != nil {
		return out, w.fail(ctx, "attach", attachErr)
	}

	zerolog.Ctx(ctx).Info().
		Str("component", "session").
		Str("operation", "attach").
		Str("status", "ok").
		Msg("session attached")

	return out, nil
}

func (w *Workflow) prepareAttachTarget(ctx context.Context, name string) (gotmux.Session, gotmux.TerminalStreams, string, error) {
	if w.server == nil {
		return gotmux.Session{}, gotmux.TerminalStreams{}, "", oops.In("session").Code("endpoint_required").Wrap(errServerRequired)
	}

	sess, findErr := w.server.FindSession(ctx, name)
	if findErr != nil {
		return gotmux.Session{}, gotmux.TerminalStreams{}, "", lookupSessionError(findErr)
	}

	streams := w.terminalStreams()
	if streamErr := validateTerminalStreams(streams); streamErr != nil {
		return gotmux.Session{}, gotmux.TerminalStreams{}, "", oops.In("session").Code("terminal_required").With("operation", "validate_terminals").Wrap(streamErr)
	}

	if valErr := ValidateHookConfig(w.hooks, EventSessionAttachPre); valErr != nil {
		return gotmux.Session{}, gotmux.TerminalStreams{}, "", oops.In("session").Code("hook_failed").With("operation", "validate_hooks").Wrap(valErr)
	}

	if valErr := ValidateHookConfig(w.hooks, EventSessionAttachPost); valErr != nil {
		return gotmux.Session{}, gotmux.TerminalStreams{}, "", oops.In("session").Code("hook_failed").With("operation", "validate_hooks").Wrap(valErr)
	}

	return sess, streams, string(sess.ID()), nil
}

func (w *Workflow) runAttachPreHook(ctx context.Context, name, sessionID string) (HookSummary, error) {
	return w.runHookBoundary(ctx, HookEvent{
		Name:          EventSessionAttachPre,
		Phase:         "pre",
		Action:        "attach",
		PrevSession:   "",
		Session:       name,
		PrevSessionID: "",
		SessionID:     sessionID,
		Source:        w.source,
	})
}

func (w *Workflow) runAttachPostHook(ctx context.Context, name, sessionID string, cmd *exec.Cmd) (HookSummary, error) {
	summary, postErr := w.runHookBoundary(ctx, HookEvent{
		Name:          EventSessionAttachPost,
		Phase:         "post",
		Action:        "attach",
		PrevSession:   "",
		Session:       name,
		PrevSessionID: "",
		SessionID:     sessionID,
		Source:        w.source,
	})

	if postErr != nil && ctx.Err() == nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}

	return summary, postErr
}

func validateTerminalStreams(streams gotmux.TerminalStreams) error {
	if streams.In == nil || streams.Out == nil || streams.Err == nil {
		return ErrTerminalRequired
	}

	if !term.IsTerminal(streams.In.Fd()) || !term.IsTerminal(streams.Out.Fd()) {
		return ErrTerminalRequired
	}

	if _, _, err := term.GetSize(streams.Out.Fd()); err != nil {
		return errors.Join(ErrTerminalRequired, err)
	}

	return nil
}

func attachWaitError(ctx context.Context, waitErr error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return oops.In("session").Code("operation_canceled").With("operation", "attach_session").Wrap(ctxErr)
	}

	if waitErr == nil {
		return nil
	}

	exitErr, ok := errors.AsType[*exec.ExitError](waitErr)
	if ok {
		return oops.In("session").Code("attach_failed").With("operation", "attach_session").With("exit_code", exitErr.ExitCode()).Wrap(waitErr)
	}

	return oops.In("session").Code("attach_failed").With("operation", "attach_session").Wrap(waitErr)
}

func wrapTerminalRestore(err error) error {
	if err == nil {
		return nil
	}

	return oops.In("session").Code("attach_failed").With("operation", "restore_terminal").Wrap(err)
}

func mergeHookSummaries(base, next HookSummary) HookSummary {
	return HookSummary{
		Attempted: base.Attempted + next.Attempted,
		Completed: base.Completed + next.Completed,
		Failed:    base.Failed + next.Failed,
		TimedOut:  base.TimedOut + next.TimedOut,
	}
}
