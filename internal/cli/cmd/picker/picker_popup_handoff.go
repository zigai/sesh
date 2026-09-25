//go:build linux || darwin

package cmdpicker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/session"
)

const pickerHandoffMaxBytes = 4096

var errPickerHandoffInvalid = errors.New("invalid popup switch handoff")

type pickerSwitchHandoff struct {
	SessionID        string                  `json:"sessionId"`
	SessionName      string                  `json:"sessionName"`
	TmuxServerSocket string                  `json:"tmuxServerSocket"`
	RuntimeIdentity  session.RuntimeIdentity `json:"runtimeIdentity"`
	ActivePaneID     string                  `json:"activePaneId,omitempty"`
	PaneID           string                  `json:"paneId,omitempty"`
}

func createPickerSwitchHandoff() (string, func(), error) {
	file, err := os.CreateTemp("", "sesh-picker-*.handoff")
	if err != nil {
		return "", nil, oops.In("cli").With("operation", "create_picker_handoff").Wrap(err)
	}

	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", nil, oops.In("cli").With("operation", "create_picker_handoff").Wrap(err)
	}

	return path, func() { _ = os.Remove(path) }, nil
}

func writePickerSwitchHandoff(path string, target session.Session, paneID string) error {
	if target.ID == "" || target.Name == "" || target.Kind != session.KindLive || target.Origin != session.OriginLocal {
		return oops.In("cli").With("operation", "write_picker_handoff").Wrap(errPickerHandoffInvalid)
	}

	encoded, err := json.Marshal(pickerSwitchHandoff{
		SessionID:        target.ID,
		SessionName:      target.Name,
		TmuxServerSocket: target.TmuxServerSocket,
		RuntimeIdentity:  target.RuntimeIdentity,
		ActivePaneID:     target.ActivePaneID,
		PaneID:           paneID,
	})
	if err != nil {
		return oops.In("cli").With("operation", "encode_picker_handoff").Wrap(err)
	}

	if len(encoded) > pickerHandoffMaxBytes {
		return oops.In("cli").With("operation", "write_picker_handoff").Wrap(errPickerHandoffInvalid)
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return oops.In("cli").With("operation", "open_picker_handoff").Wrap(err)
	}

	_, writeErr := file.Write(encoded)

	return errors.Join(writeErr, file.Close())
}

func readPickerSwitchHandoff(path string) (pickerSwitchHandoff, bool, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return pickerSwitchHandoff{}, false, oops.In("cli").With("operation", "open_picker_handoff").Wrap(err)
	}

	encoded, readErr := io.ReadAll(io.LimitReader(file, pickerHandoffMaxBytes+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return pickerSwitchHandoff{}, false, oops.In("cli").With("operation", "read_picker_handoff").Wrap(err)
	}

	if len(encoded) == 0 {
		var empty pickerSwitchHandoff

		return empty, false, nil
	}

	if len(encoded) > pickerHandoffMaxBytes {
		return pickerSwitchHandoff{}, false, oops.In("cli").With("operation", "read_picker_handoff").Wrap(errPickerHandoffInvalid)
	}

	var handoff pickerSwitchHandoff

	if json.Unmarshal(encoded, &handoff) != nil || handoff.SessionID == "" || handoff.SessionName == "" {
		return pickerSwitchHandoff{}, false, oops.In("cli").With("operation", "read_picker_handoff").Wrap(errPickerHandoffInvalid)
	}

	return handoff, true, nil
}

func completePickerSwitchHandoff(
	ctx context.Context,
	services *pickerServices,
	path, clientName, originSessionID string,
) error {
	handoff, selected, err := readPickerSwitchHandoff(path)
	if err != nil || !selected {
		return err
	}

	if clientName == "" || originSessionID == "" {
		return oops.In("cli").With("operation", "complete_picker_handoff").Wrap(errPickerHandoffInvalid)
	}

	target := newPickerSwitchTarget(handoff)

	return services.performSwitchValidated(ctx, target, handoff.PaneID, clientName, originSessionID)
}

func newPickerSwitchTarget(handoff pickerSwitchHandoff) session.Session {
	var target session.Session

	target.ID = handoff.SessionID
	target.Name = handoff.SessionName
	target.TmuxServerSocket = handoff.TmuxServerSocket
	target.Kind = session.KindLive
	target.Origin = session.OriginLocal
	target.RuntimeIdentity = handoff.RuntimeIdentity
	target.ActivePaneID = handoff.ActivePaneID

	return target
}
