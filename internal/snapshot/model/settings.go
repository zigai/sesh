package model

import (
	"bytes"
	"slices"
)

func ValidNonrestartableReason(reason string) bool {
	return reason == ReasonPaneDead ||
		reason == ReasonExecutableDeleted ||
		reason == ReasonWorkingDirectoryDeleted
}

func validateSettings(options []Option, environment []Variable) error {
	seen := make(map[string]bool)

	for _, option := range options {
		key := option.Scope + "\x00" + string(option.Name)
		if !validOption(option) || seen[key] {
			return ErrInvalid
		}

		seen[key] = true
	}

	return validateEnvironment(environment)
}

func validOption(option Option) bool {
	if len(option.Name) == 0 || bytes.ContainsRune(option.Name, 0) || bytes.ContainsRune(option.Value, 0) {
		return false
	}

	if !option.Present && len(option.Value) != 0 {
		return false
	}

	return slices.Contains([]string{"server", "global-session", "global-window", "session", "window", "pane"}, option.Scope)
}

func validateEnvironment(environment []Variable) error {
	seen := make(map[string]bool)
	for _, variable := range environment {
		if !validVariable(variable) || seen[string(variable.Name)] {
			return ErrInvalid
		}

		seen[string(variable.Name)] = true
	}

	return nil
}

func validVariable(variable Variable) bool {
	if len(variable.Name) == 0 || bytes.ContainsAny(variable.Name, "=\x00") || bytes.ContainsRune(variable.Value, 0) {
		return false
	}

	if variable.State == "set" {
		return true
	}

	return (variable.State == "removed" || variable.State == "absent") && len(variable.Value) == 0
}

func validateLaunch(launch Launch) error {
	if err := validateLaunchRecovery(launch); err != nil {
		return err
	}

	if bytes.ContainsRune(launch.Executable, 0) || bytes.ContainsRune(launch.Directory, 0) {
		return ErrInvalid
	}

	for _, argument := range launch.Arguments {
		if bytes.ContainsRune(argument, 0) {
			return ErrInvalid
		}
	}

	return validateEnvironment(launch.Environment)
}

func validateLaunchRecovery(launch Launch) error {
	switch launch.Recovery {
	case RecoveryUnverified:
		if launch.Reason != "" {
			return ErrInvalid
		}

		return nil
	case RecoveryRestart, RecoveryResume:
		return validateRunnableLaunch(launch)
	case RecoveryNonrestartable:
		return validateNonrestartableLaunch(launch)
	default:
		return ErrInvalid
	}
}

func validateRunnableLaunch(launch Launch) error {
	if launch.Reason != "" || len(launch.Executable) == 0 || len(launch.Arguments) == 0 || !validDirectory(launch.Directory) {
		return ErrInvalid
	}

	return nil
}

func validateNonrestartableLaunch(launch Launch) error {
	if !ValidNonrestartableReason(launch.Reason) {
		return ErrInvalid
	}

	if len(launch.Directory) > 0 && !validDirectory(launch.Directory) {
		return ErrInvalid
	}

	return nil
}
