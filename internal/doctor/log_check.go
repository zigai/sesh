//go:build linux || darwin

package doctor

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const chooseLogFile = "Set paths.log_file to a file in a directory you can write"

func (s *Service) checkLogFile() Result {
	path := s.opts.LogPath

	info, err := os.Stat(path)

	switch {
	case err == nil && !info.Mode().IsRegular():
		return newResult(CheckLogFile, StatusFail, DisplayPath(path)+" is not a regular file").
			withReason(ReasonInvalid).
			withManual(chooseLogFile)
	case err == nil && unix.Access(path, unix.W_OK) != nil:
		return newResult(CheckLogFile, StatusFail, DisplayPath(path)+" is not writable").
			withReason(ReasonUnsafe).
			withCommand("chmod u+w " + shellPath(path))
	case err == nil:
		return newResult(CheckLogFile, StatusOK, DisplayPath(path))
	case !errors.Is(err, os.ErrNotExist):
		return newResult(CheckLogFile, StatusFail, "Cannot inspect "+DisplayPath(path)+": "+err.Error()).
			withReason(ReasonInspectionFailed).
			withManual(chooseLogFile)
	}

	dir := existingAncestor(filepath.Dir(path))
	if info, err := os.Stat(dir); err != nil || !info.IsDir() || unix.Access(dir, unix.W_OK|unix.X_OK) != nil {
		return newResult(CheckLogFile, StatusFail, "Cannot create "+DisplayPath(path)+": "+DisplayPath(dir)+" is not a writable directory").
			withReason(ReasonUnsafe).
			withManual(chooseLogFile)
	}

	return newResult(CheckLogFile, StatusOK, DisplayPath(path)+" (created by the next logged command)")
}

func existingAncestor(dir string) string {
	for {
		_, err := os.Stat(dir)

		parent := filepath.Dir(dir)
		if !errors.Is(err, os.ErrNotExist) || parent == dir {
			return dir
		}

		dir = parent
	}
}
