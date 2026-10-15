//go:build linux || darwin

package doctor

import (
	"fmt"
	"strings"
)

const programName = "sesh"

func (s *Service) checkInstallPath() Result {
	copies := pathCopies(s.opts.PathEnv, programName)

	switch len(copies) {
	case 0:
		return newResult(CheckInstallPath, StatusInfo, "sesh is not on PATH").withReason(ReasonNotFound)
	case 1:
		return newResult(CheckInstallPath, StatusOK, DisplayPath(copies[0])+", no other copies on PATH")
	}

	details := make([]string, 0, len(copies))
	for _, candidate := range copies {
		details = append(details, DisplayPath(candidate))
	}

	return newResult(CheckInstallPath, StatusWarn, fmt.Sprintf("%d copies on PATH; the shell runs the first", len(copies))).
		withReason(ReasonDuplicate).
		withDetails(details...).
		withManual("Remove the copies you do not use")
}

func (s *Service) checkConfig() Result {
	if s.opts.ConfigError != "" {
		return newResult(CheckConfigFile, StatusFail, s.opts.ConfigError).
			withReason(ReasonInvalid).
			withManual(s.configFixStep())
	}

	var loaded []string

	skipped := len(s.opts.ConfigFiles) > 0

	for _, file := range s.opts.ConfigFiles {
		if file.Status == ConfigLoaded {
			loaded = append(loaded, DisplayPath(file.Path))
		}

		if file.Status != ConfigSkipped {
			skipped = false
		}
	}

	switch {
	case skipped:
		return newResult(CheckConfigFile, StatusSkip, "--no-config")
	case len(loaded) == 0:
		return newResult(CheckConfigFile, StatusInfo, "No config file; using built-in settings")
	default:
		return newResult(CheckConfigFile, StatusOK, "Loaded "+strings.Join(loaded, ", "))
	}
}

func (s *Service) configFixStep() string {
	var present []string

	for _, file := range s.opts.ConfigFiles {
		if file.Status != ConfigNotFound && file.Status != ConfigSkipped {
			present = append(present, DisplayPath(file.Path))
		}
	}

	if len(present) == 0 {
		return "Point --config or SESH_CONFIG at an existing file, or unset it"
	}

	return "Correct the error in " + strings.Join(present, " or ")
}
