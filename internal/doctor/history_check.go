//go:build linux || darwin

package doctor

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/strictjson"
)

func (s *Service) checkHistory() Result {
	path := s.opts.HistoryPath
	moveAside := "mv " + shellPath(path) + " " + shellPath(path+".bak")

	info, err := os.Lstat(path)

	switch {
	case errors.Is(err, os.ErrNotExist):
		return newResult(CheckHistoryFile, StatusInfo, "Not created yet; switching sessions creates it").withReason(ReasonNotFound)
	case errors.Is(err, os.ErrPermission):
		return newResult(CheckHistoryFile, StatusFail, "Permission denied reading "+DisplayPath(path)).
			withReason(ReasonUnsafe).
			withManual("Make the directory of " + DisplayPath(path) + " readable by you")
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		return s.unsafeHistory()
	}

	state, loadErr := session.LoadState(path)
	if loadErr == nil {
		return newResult(CheckHistoryFile, StatusOK, fmt.Sprintf("%s, %s, %s",
			plural(len(state.SwitchHistory), "switch entry"), plural(len(state.RemoteSwitchHistory), "remote entry"),
			plural(len(state.AgentViewHistory), "agent view entry")))
	}

	switch {
	case errors.Is(loadErr, session.ErrHistoryOversized):
		return newResult(CheckHistoryFile, StatusFail, "Larger than the 1 MiB limit").withReason(ReasonCorrupt).withCommand(moveAside)
	case errors.Is(loadErr, privatefs.ErrUnsafe), errors.Is(loadErr, syscall.ELOOP):
		return s.unsafeHistory()
	case errors.Is(loadErr, strictjson.ErrInvalid):
		return newResult(CheckHistoryFile, StatusFail, "Contains invalid JSON").withReason(ReasonCorrupt).withCommand(moveAside)
	case errors.Is(loadErr, session.ErrInvalidHistoryVersion):
		return incompatibleHistory(path, loadErr, moveAside)
	default:
		return newResult(CheckHistoryFile, StatusFail, "Cannot load: "+loadErr.Error()).
			withReason(ReasonInspectionFailed).
			withCommand("ls -l " + shellPath(path))
	}
}

func incompatibleHistory(path string, loadErr error, moveAside string) Result {
	versionErr, ok := errors.AsType[*session.HistoryVersionError](loadErr)
	if !ok {
		return newResult(CheckHistoryFile, StatusFail, "Has an unsupported schema version").withReason(ReasonIncompatible).withCommand(moveAside)
	}

	filter, ok := session.HistoryUpgradeFilter(versionErr.Version)
	if !ok {
		return newResult(CheckHistoryFile, StatusFail, fmt.Sprintf("Has unsupported schema version %d", versionErr.Version)).
			withReason(ReasonIncompatible).
			withCommand(moveAside)
	}

	file, temp := shellPath(path), shellPath(path+".tmp")

	return newResult(CheckHistoryFile, StatusFail, fmt.Sprintf("Has schema version %d; convert it to version %d", versionErr.Version, session.CurrentHistoryVersion)).
		withReason(ReasonIncompatible).
		withCommand(fmt.Sprintf("jq %s %s > %s && chmod 600 %s && mv %s %s", shellArg(filter), file, temp, temp, temp, file))
}

func (s *Service) unsafeHistory() Result {
	path := DisplayPath(s.opts.HistoryPath)

	return newResult(CheckHistoryFile, StatusFail, path+" is a symlink or has unsafe permissions").
		withReason(ReasonUnsafe).
		withManual(fmt.Sprintf("Make %s a regular file owned by you with mode 0600 in a directory only you can write", path))
}
