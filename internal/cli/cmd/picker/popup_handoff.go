//go:build linux || darwin

package picker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/tui"
)

const (
	handoffMaxBytes      = 256 * 1024
	handoffMaxAgentViews = 256
)

var errPickerHandoffInvalid = errors.New("invalid popup switch handoff")

// switchHandoff carries a popup's result to the invoking process: the selected session, if
// any, and the agent views whose recording the popup deferred.
type switchHandoff struct {
	SessionID        string                  `json:"sessionId,omitempty"`
	SessionName      string                  `json:"sessionName,omitempty"`
	TmuxServerSocket string                  `json:"tmuxServerSocket,omitempty"`
	RuntimeIdentity  session.RuntimeIdentity `json:"runtimeIdentity"`
	ActivePaneID     string                  `json:"activePaneId,omitempty"`
	PaneID           string                  `json:"paneId,omitempty"`
	AgentViews       []handoffView           `json:"agentViews,omitempty"`
	ReplayKey        string                  `json:"replayKey,omitempty"`
	TaskProject      string                  `json:"taskProject,omitempty"`
}

type handoffView struct {
	Session session.Identity `json:"session"`
	PaneID  string           `json:"paneId"`
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

func writePickerSwitchHandoff(path string, target *session.Session, paneID, replayKey, taskProject string, views []tui.AgentView) error {
	var handoff switchHandoff

	handoff.ReplayKey = replayKey
	handoff.TaskProject = taskProject

	if target != nil {
		if !applyPickerHandoffTarget(&handoff, target, paneID) {
			return oops.In("cli").With("operation", "write_picker_handoff").Wrap(errPickerHandoffInvalid)
		}
	}

	for _, view := range views[max(0, len(views)-handoffMaxAgentViews):] {
		handoff.AgentViews = append(handoff.AgentViews, handoffView{Session: view.Session, PaneID: view.PaneID})
	}

	if target == nil && len(handoff.AgentViews) == 0 && handoff.ReplayKey == "" && handoff.TaskProject == "" {
		return nil
	}

	encoded, err := json.Marshal(handoff)
	if err != nil {
		return oops.In("cli").With("operation", "encode_picker_handoff").Wrap(err)
	}

	if len(encoded) > handoffMaxBytes {
		return oops.In("cli").With("operation", "write_picker_handoff").Wrap(errPickerHandoffInvalid)
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return oops.In("cli").With("operation", "open_picker_handoff").Wrap(err)
	}

	_, writeErr := file.Write(encoded)

	return errors.Join(writeErr, file.Close())
}

func applyPickerHandoffTarget(handoff *switchHandoff, target *session.Session, paneID string) bool {
	if target.ID == "" || target.Name == "" || target.Kind != session.KindLive || target.Origin != session.OriginLocal {
		return false
	}

	handoff.SessionID = target.ID
	handoff.SessionName = target.Name
	handoff.TmuxServerSocket = target.TmuxServerSocket
	handoff.RuntimeIdentity = target.RuntimeIdentity
	handoff.ActivePaneID = target.ActivePaneID
	handoff.PaneID = paneID

	return true
}

func readPickerSwitchHandoff(path string) (switchHandoff, bool, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return switchHandoff{}, false, oops.In("cli").With("operation", "open_picker_handoff").Wrap(err)
	}

	encoded, readErr := io.ReadAll(io.LimitReader(file, handoffMaxBytes+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return switchHandoff{}, false, oops.In("cli").With("operation", "read_picker_handoff").Wrap(err)
	}

	if len(encoded) == 0 {
		var empty switchHandoff

		return empty, false, nil
	}

	if len(encoded) > handoffMaxBytes {
		return switchHandoff{}, false, oops.In("cli").With("operation", "read_picker_handoff").Wrap(errPickerHandoffInvalid)
	}

	var handoff switchHandoff

	if json.Unmarshal(encoded, &handoff) != nil || (handoff.SessionID == "") != (handoff.SessionName == "") ||
		len(handoff.AgentViews) > handoffMaxAgentViews || (handoff.TaskProject != "" && !filepath.IsAbs(handoff.TaskProject)) {
		return switchHandoff{}, false, oops.In("cli").With("operation", "read_picker_handoff").Wrap(errPickerHandoffInvalid)
	}

	return handoff, handoff.SessionID != "", nil
}

func completePickerSwitchHandoff(
	ctx context.Context,
	services *services,
	path, clientName, originSessionID string,
) error {
	handoff, selected, err := readPickerSwitchHandoff(path)
	if err != nil {
		return err
	}

	views := make([]tui.AgentView, 0, len(handoff.AgentViews))
	for _, view := range handoff.AgentViews {
		views = append(views, tui.AgentView{Session: view.Session, PaneID: view.PaneID})
	}

	if !selected {
		if len(views) == 0 {
			return nil
		}

		return services.recordPickerHistory(ctx, "", views)
	}

	if clientName == "" || originSessionID == "" {
		return oops.In("cli").With("operation", "complete_picker_handoff").Wrap(errPickerHandoffInvalid)
	}

	target := newPickerSwitchTarget(handoff)

	return services.performSwitchValidated(ctx, target, handoff.PaneID, clientName, originSessionID, views)
}

func newPickerSwitchTarget(handoff switchHandoff) session.Session {
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

// replayPickerKey replays the key the popup asked for on the client that opened it, once the
// popup has closed. Only keys the picker is known to request are accepted.
func replayPickerKey(ctx context.Context, path, socketPath, clientName string) error {
	handoff, _, err := readPickerSwitchHandoff(path)
	if err != nil || handoff.ReplayKey == "" {
		return err
	}

	if handoff.ReplayKey != tui.ReplayKeyFloatingTerminal || clientName == "" {
		return oops.In("cli").With("operation", "replay_picker_key").Wrap(errPickerHandoffInvalid)
	}

	args := []string{"-S", socketPath, "send-keys", "-K", "-c", clientName, handoff.ReplayKey}
	if output, err := exec.CommandContext(ctx, "tmux", args...).CombinedOutput(); err != nil {
		return oops.In("cli").With("operation", "replay_picker_key").With("output", string(output)).Wrap(err)
	}

	return nil
}
