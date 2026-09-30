//go:build linux || darwin

package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"
	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/session"
)

const originBindingPrefix = "@sesh_origin_"

func OriginAttachmentOption(terminal *os.File) (string, error) {
	var stat unix.Stat_t

	if terminal == nil {
		return "", errNilAttachStreams
	}

	if err := unix.Fstat(int(terminal.Fd()), &stat); err != nil {
		return "", oops.In("remote").With("operation", "origin_attachment_option").Wrap(err)
	}

	if stat.Mode&unix.S_IFMT != unix.S_IFCHR {
		return "", oops.In("remote").With("operation", "origin_attachment_option").Wrap(session.ErrTerminalRequired)
	}

	return fmt.Sprintf("%s%x", originBindingPrefix, stat.Rdev), nil
}

func (live *GotmuxLiveServer) AttachWithOrigin(ctx context.Context, record StoredCapability, streams AttachStreams) (resultErr error) {
	handle, err := live.session(ctx, record.Target.SessionID)
	if err != nil {
		return err
	}

	identity := handle.ServerIdentity()
	if identity.PID != record.Target.TmuxServerPID || !identity.Started.Equal(record.Target.TmuxServerStartedAt) {
		return oops.In("remote").With("operation", "attach_with_origin").Wrap(ErrTargetChanged)
	}

	name, err := OriginAttachmentOption(streams.In)
	if err != nil {
		return err
	}

	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()

		resultErr = errors.Join(resultErr, live.clearOriginBinding(cleanupCtx, handle, name, record.Grant.AttachmentID))
	}()

	options := live.server.Options()
	if err := options.SetUserWith(ctx, name, gotmux.SetOptionOptions{
		Value: gotmux.PresentValue(record.Grant.AttachmentID), OnlyIfUnset: true, Append: false, ExpandFormat: false,
	}); err != nil {
		return oops.In("remote").With("operation", "attach_with_origin").Wrap(err)
	}

	value, err := options.User(ctx, name)
	if err != nil {
		return oops.In("remote").With("operation", "attach_with_origin").Wrap(err)
	}

	if id, present := value.Local.Get(); !present || id != record.Grant.AttachmentID {
		return oops.In("remote").With("operation", "attach_with_origin").Wrap(ErrCapabilityMismatch)
	}

	if err := selectAttachmentPane(ctx, handle, record.Target.PaneID); err != nil {
		return err
	}

	if err := handle.Attach(ctx, gotmux.Streams{In: streams.In, Out: streams.Out, Err: streams.Err}, gotmux.AttachOptions{
		ReadOnly: false, PreserveEnvironment: false, Detach: gotmux.DetachModeNone, Dir: "", Flags: nil,
	}); err != nil {
		return oops.In("remote").With("operation", "attach_with_origin").Wrap(err)
	}

	return nil
}

func selectAttachmentPane(ctx context.Context, handle gotmux.Session, paneID string) error {
	if paneID == "" {
		return nil
	}

	panes, err := handle.Panes(ctx)
	if err != nil {
		return oops.In("remote").With("operation", "select_attachment_pane").Wrap(err)
	}

	for _, pane := range panes {
		if string(pane.ID) == paneID {
			if err := pane.Handle().Select(ctx); err != nil {
				return oops.In("remote").With("operation", "select_attachment_pane").Wrap(err)
			}

			return nil
		}
	}

	return oops.In("remote").With("operation", "select_attachment_pane").Wrap(ErrTargetChanged)
}

func (live *GotmuxLiveServer) clearOriginBinding(ctx context.Context, handle gotmux.Session, name, attachmentID string) error {
	probe, err := live.server.Probe(ctx)
	if err != nil {
		if errors.Is(err, gotmux.ErrNoServer) {
			return nil
		}

		return oops.In("remote").With("operation", "clear_origin_binding").Wrap(err)
	}

	identity := handle.ServerIdentity()
	if !identity.Equal(probe.Identity) {
		return nil
	}

	condition := fmt.Sprintf("#{&&:#{==:#{pid},%d},#{&&:#{==:#{start_time},%d},#{==:#{%s},%s}}}", identity.PID, identity.Started.Unix(), name, attachmentID)

	command, err := gotmux.NewCommand("if-shell", "-F", condition, "set-option -su "+name)
	if err != nil {
		return oops.In("remote").With("operation", "clear_origin_binding").Wrap(err)
	}

	if _, err := live.server.Run(ctx, command); err != nil {
		return oops.In("remote").With("operation", "clear_origin_binding").Wrap(err)
	}

	return nil
}
