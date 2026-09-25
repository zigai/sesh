//go:build linux || darwin

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"
)

const (
	windowStatusStyleOption     = "window-status-style"
	windowStatusBellStyleOption = "window-status-bell-style"
	styleOwnerOption            = "@sesh-agent-style"
	stylePriorOption            = "@sesh-agent-style-prior"
	styleLockChannel            = "sesh_style_lock"
)

var emptyOwnedWindowStyle ownedWindowStyle

var zeroStyleRecord styleRecord

type ActivePane struct {
	PaneID       string
	WindowID     string
	PaneActive   bool
	WindowActive bool
}

type StyleController interface {
	ActivePanes(ctx context.Context) (map[string]ActivePane, error)
	SetWindowStyle(ctx context.Context, windowID string, style string) error
	ClearWindowStyle(ctx context.Context, windowID string) error
	ClearOwned(ctx context.Context) error
}

type TmuxStyleController struct {
	server         *gotmux.Server
	setStatusStyle bool
	setBellStyle   bool
	mu             sync.Mutex
	owned          map[string]ownedWindowStyle
}

type ownedWindowStyle struct {
	applied string
	status  optionPrior
	bell    optionPrior
}

type optionPrior struct {
	Present bool   `json:"present"`
	Value   string `json:"value"`
}

type styleRecord struct {
	Status optionPrior `json:"status"`
	Bell   optionPrior `json:"bell"`
}

func NewTmuxStyleController(server *gotmux.Server, setStatusStyle bool, setBellStyle bool) *TmuxStyleController {
	return &TmuxStyleController{
		server:         server,
		setStatusStyle: setStatusStyle,
		setBellStyle:   setBellStyle,
		mu:             sync.Mutex{},
		owned:          make(map[string]ownedWindowStyle),
	}
}

func (c *TmuxStyleController) ActivePanes(ctx context.Context) (map[string]ActivePane, error) {
	panes, err := ObservePanes(ctx, c.server)
	if err != nil {
		return nil, err
	}

	active := make(map[string]ActivePane, len(panes))

	for _, pane := range panes {
		active[pane.PaneID] = ActivePane{
			PaneID:       pane.PaneID,
			WindowID:     pane.WindowID,
			PaneActive:   pane.PaneActive,
			WindowActive: pane.WindowActive,
		}
	}

	return active, nil
}

func (c *TmuxStyleController) SetWindowStyle(ctx context.Context, windowID string, style string) (retErr error) {
	unlock, err := c.lock(ctx)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, unlock()) }()

	window, err := c.window(windowID)
	if err != nil {
		return err
	}

	owned, known, err := c.styleRecordForUpdate(ctx, window)
	if err != nil {
		return err
	}

	statusEdited, bellEdited := c.checkManualEdits(ctx, window, owned, known)

	allManagedEdited := (statusEdited || !c.setStatusStyle) && (bellEdited || !c.setBellStyle)
	if allManagedEdited && (c.setStatusStyle || c.setBellStyle) {
		c.mu.Lock()
		delete(c.owned, windowID)
		c.mu.Unlock()

		return c.releaseOwnershipMarkers(ctx, window)
	}

	if err := c.applyStyleSelective(ctx, window, style, !statusEdited, !bellEdited); err != nil {
		return errors.Join(err, c.releaseWindow(ctx, window))
	}

	owned.applied = style

	c.mu.Lock()
	c.owned[windowID] = owned
	c.mu.Unlock()

	return nil
}

func (c *TmuxStyleController) ClearWindowStyle(ctx context.Context, windowID string) (retErr error) {
	unlock, err := c.lock(ctx)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, unlock()) }()

	window, err := c.window(windowID)
	if err != nil {
		return err
	}

	c.mu.Lock()
	delete(c.owned, windowID)
	c.mu.Unlock()

	return c.releaseWindow(ctx, window)
}

func (c *TmuxStyleController) ClearOwned(ctx context.Context) (retErr error) {
	if c.server == nil {
		return oops.In("agents").Code("endpoint_required").With("operation", "clear_window_styles").Wrap(ErrServerRequired)
	}

	unlock, err := c.lock(ctx)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, unlock()) }()

	windows, err := c.server.Windows(ctx)
	if err != nil {
		return oops.In("agents").Code("tmux_failed").With("operation", "list_windows").Wrap(err)
	}

	var failures []error

	for _, info := range windows {
		window := info.Handle()

		owned, ownedErr := c.isOwned(ctx, window)
		if ownedErr != nil {
			failures = append(failures, ownedErr)

			continue
		}

		if !owned {
			continue
		}

		c.mu.Lock()
		delete(c.owned, string(info.ID))
		c.mu.Unlock()

		if err := c.releaseWindow(ctx, window); err != nil {
			failures = append(failures, err)
		}
	}

	return errors.Join(failures...)
}

func (c *TmuxStyleController) styleRecordForUpdate(
	ctx context.Context,
	window gotmux.Window,
) (ownedWindowStyle, bool, error) {
	owned, known, err := c.readRecord(ctx, window)
	if err != nil || known {
		return owned, known, err
	}

	owned, err = c.adoptWindow(ctx, window)

	return owned, false, err
}

func (c *TmuxStyleController) adoptWindow(ctx context.Context, window gotmux.Window) (ownedWindowStyle, error) {
	if record, ok, err := c.readRecord(ctx, window); err != nil {
		return ownedWindowStyle{}, err
	} else if ok {
		return record, nil
	}

	status, err := c.readOption(ctx, window, windowStatusStyleOption)
	if err != nil {
		return ownedWindowStyle{}, err
	}

	bell, err := c.readOption(ctx, window, windowStatusBellStyleOption)
	if err != nil {
		return ownedWindowStyle{}, err
	}

	record := ownedWindowStyle{applied: "", status: status, bell: bell}

	encoded, err := json.Marshal(styleRecord{Status: status, Bell: bell})
	if err != nil {
		return ownedWindowStyle{}, oops.In("agents").Code("encode_style_record").With("operation", "record_window_style").Wrap(err)
	}

	options := window.Options()

	if err := options.SetUser(ctx, stylePriorOption, string(encoded)); err != nil {
		return ownedWindowStyle{}, oops.In("agents").Code("tmux_failed").With("operation", "record_window_style").Wrap(err)
	}

	if err := options.SetUser(ctx, styleOwnerOption, ""); err != nil {
		ownerErr := oops.In("agents").Code("tmux_failed").With("operation", "record_window_style").Wrap(err)
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultLockTimeout)
		cleanupErr := options.UnsetUser(cleanupCtx, stylePriorOption)

		cancel()

		if cleanupErr != nil {
			cleanupErr = oops.In("agents").Code("tmux_failed").With("operation", "rollback_window_style_prior").Wrap(cleanupErr)
		}

		return ownedWindowStyle{}, errors.Join(ownerErr, cleanupErr)
	}

	return record, nil
}

func (c *TmuxStyleController) checkManualEdits(
	ctx context.Context,
	window gotmux.Window,
	owned ownedWindowStyle,
	known bool,
) (bool, bool) {
	statusEdited := false
	if known && c.setStatusStyle && owned.applied != "" {
		statusEdited = c.checkOptionEdited(ctx, window, windowStatusStyleOption, owned.applied, "set_window_status_style")
	}

	bellEdited := false
	if known && c.setBellStyle && owned.applied != "" {
		bellEdited = c.checkOptionEdited(ctx, window, windowStatusBellStyleOption, owned.applied, "set_window_bell_style")
	}

	return statusEdited, bellEdited
}

func (c *TmuxStyleController) checkOptionEdited(
	ctx context.Context,
	window gotmux.Window,
	name string,
	applied string,
	operation string,
) bool {
	current, err := c.readOption(ctx, window, name)
	if err != nil {
		return false
	}

	if (current.Present && current.Value != applied) || (!current.Present && applied != "") {
		zerolog.Ctx(ctx).Warn().
			Str("component", "agents").
			Str("operation", operation).
			Str("status", "skipped").
			Str("reason", "user_option_changed").
			Msg("agent window style update skipped to preserve manual edit")

		return true
	}

	return false
}

func (c *TmuxStyleController) applyStyleSelective(ctx context.Context, window gotmux.Window, style string, applyStatus, applyBell bool) error {
	options := window.Options()

	if err := options.SetUser(ctx, styleOwnerOption, style); err != nil {
		return oops.In("agents").Code("tmux_failed").With("operation", "record_window_style").Wrap(err)
	}

	if c.setStatusStyle && applyStatus {
		if err := options.Set(ctx, windowStatusStyleOption, style); err != nil {
			return oops.In("agents").Code("tmux_failed").With("operation", "set_window_status_style").Wrap(err)
		}
	}

	if c.setBellStyle && applyBell {
		if err := options.Set(ctx, windowStatusBellStyleOption, style); err != nil {
			return oops.In("agents").Code("tmux_failed").With("operation", "set_window_status_bell_style").Wrap(err)
		}
	}

	return nil
}

func (c *TmuxStyleController) releaseWindow(ctx context.Context, window gotmux.Window) error {
	record, ok, err := c.readRecord(ctx, window)
	if err != nil {
		return err
	}

	if !ok {
		return nil
	}

	var failures []error

	if c.setStatusStyle {
		if err := c.restoreOption(ctx, window, windowStatusStyleOption, record.status, record.applied); err != nil {
			failures = append(failures, err)
		}
	}

	if c.setBellStyle {
		if err := c.restoreOption(ctx, window, windowStatusBellStyleOption, record.bell, record.applied); err != nil {
			failures = append(failures, err)
		}
	}

	if err := errors.Join(failures...); err != nil {
		return err
	}

	return c.releaseOwnershipMarkers(ctx, window)
}

func (c *TmuxStyleController) releaseOwnershipMarkers(ctx context.Context, window gotmux.Window) error {
	options := window.Options()

	if err := options.UnsetUser(ctx, stylePriorOption); err != nil {
		return oops.In("agents").Code("tmux_failed").With("operation", "release_window_style_prior").Wrap(err)
	}

	if err := options.UnsetUser(ctx, styleOwnerOption); err != nil {
		return oops.In("agents").Code("tmux_failed").With("operation", "release_window_style_owner").Wrap(err)
	}

	return nil
}

func (c *TmuxStyleController) isOwned(ctx context.Context, window gotmux.Window) (bool, error) {
	value, err := window.Options().User(ctx, styleOwnerOption)
	if err != nil {
		return false, oops.In("agents").Code("tmux_failed").With("operation", "read_window_ownership").Wrap(err)
	}

	_, ok := value.Effective.Get()

	return ok, nil
}

func (c *TmuxStyleController) readRecord(ctx context.Context, window gotmux.Window) (ownedWindowStyle, bool, error) {
	options := window.Options()

	owner, err := options.User(ctx, styleOwnerOption)
	if err != nil {
		return ownedWindowStyle{}, false, oops.In("agents").Code("tmux_failed").With("operation", "read_window_ownership").Wrap(err)
	}

	applied, owned := owner.Effective.Get()
	if !owned {
		return emptyOwnedWindowStyle, false, nil
	}

	record := ownedWindowStyle{applied: applied, status: optionPrior{Present: false, Value: ""}, bell: optionPrior{Present: false, Value: ""}}

	prior, err := options.User(ctx, stylePriorOption)
	if err != nil {
		return ownedWindowStyle{}, false, oops.In("agents").Code("tmux_failed").With("operation", "read_window_ownership").Wrap(err)
	}

	raw, ok := prior.Effective.Get()
	if !ok {
		return ownedWindowStyle{}, false, oops.In("agents").Code("style_ownership_corrupt").With("operation", "read_window_ownership").Wrap(ErrStyleOwnershipCorrupt)
	}

	stored, decoded := decodeStyleRecord(raw)
	if !decoded {
		return ownedWindowStyle{}, false, oops.In("agents").Code("style_ownership_corrupt").With("operation", "read_window_ownership").Wrap(ErrStyleOwnershipCorrupt)
	}

	record.status = stored.Status
	record.bell = stored.Bell

	return record, true, nil
}

func decodeStyleRecord(raw string) (styleRecord, bool) {
	var stored styleRecord

	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return zeroStyleRecord, false
	}

	return stored, true
}

func (c *TmuxStyleController) readOption(ctx context.Context, window gotmux.Window, name string) (optionPrior, error) {
	value, err := window.Options().Get(ctx, name)
	if err != nil {
		return optionPrior{}, oops.In("agents").Code("tmux_failed").With("operation", "read_window_option").Wrap(err)
	}

	local, ok := value.Local.Get()
	if !ok {
		return optionPrior{Present: false, Value: ""}, nil
	}

	return optionPrior{Present: true, Value: local}, nil
}

func (c *TmuxStyleController) restoreOption(
	ctx context.Context,
	window gotmux.Window,
	name string,
	prior optionPrior,
	applied string,
) error {
	current, err := c.readOption(ctx, window, name)
	if err != nil {
		return err
	}

	if !current.Present || current.Value != applied {
		if current.Present {
			zerolog.Ctx(ctx).Warn().
				Str("component", "agents").
				Str("operation", "clear_window_style").
				Str("status", "skipped").
				Str("reason", "user_option_changed").
				Msg("agent window style release skipped")
		}

		return nil
	}

	options := window.Options()

	if prior.Present {
		if err := options.Set(ctx, name, prior.Value); err != nil {
			return oops.In("agents").Code("tmux_failed").With("operation", "restore_window_option").Wrap(err)
		}

		return nil
	}

	if err := options.Unset(ctx, name); err != nil {
		return oops.In("agents").Code("tmux_failed").With("operation", "restore_window_option").Wrap(err)
	}

	return nil
}

func (c *TmuxStyleController) window(windowID string) (gotmux.Window, error) {
	if c.server == nil {
		return gotmux.Window{}, oops.In("agents").Code("endpoint_required").With("operation", "window_style").Wrap(ErrServerRequired)
	}

	window, err := c.server.WindowHandle(gotmux.WindowID(windowID))
	if err != nil {
		return gotmux.Window{}, oops.In("agents").Code("invalid_target").With("operation", "window_style").Wrap(err)
	}

	return window, nil
}

func (c *TmuxStyleController) lock(ctx context.Context) (func() error, error) {
	if c.server == nil {
		return func() error { return nil }, nil
	}

	lockCtx := ctx

	var cancel context.CancelFunc
	if _, ok := ctx.Deadline(); !ok {
		lockCtx, cancel = context.WithTimeout(ctx, defaultLockTimeout)
	}

	if err := c.server.Lock(lockCtx, styleLockChannel); err != nil {
		if cancel != nil {
			cancel()
		}

		return nil, oops.In("agents").Code("fencing_failed").With("operation", "style_lock").Wrap(err)
	}

	return func() error {
		if cancel != nil {
			cancel()
		}

		unlockCtx, unlockCancel := context.WithTimeout(context.WithoutCancel(ctx), defaultLockTimeout)
		defer unlockCancel()

		if err := c.server.Unlock(unlockCtx, styleLockChannel); err != nil {
			return oops.In("agents").Code("fencing_failed").With("operation", "style_unlock").Wrap(err)
		}

		return nil
	}, nil
}
