package process

import (
	"context"
	"errors"
	"os"

	"github.com/samber/oops"
)

const (
	Domain = "process"

	CodeInvalidPID = "process_invalid_pid"

	CodeBirthRequired = "process_birth_required"

	CodeIdentityMismatch = "process_identity_mismatch"

	CodeProcessVanished = "process_vanished"

	CodeProcessZombie = "process_zombie"

	CodeEmptyCmdline = "process_empty_cmdline"

	CodeMalformedCmdline = "process_malformed_cmdline"

	CodeMalformedEnviron = "process_malformed_environ"

	CodeDuplicateVariable = "process_duplicate_variable"

	CodeMalformedStat = "process_malformed_stat"

	CodeInputTooLarge = "process_input_too_large"

	CodeInvalidVariable = "process_invalid_variable"

	CodeUnsupported = "process_unsupported"

	CodeAmbiguousProcLink = "process_ambiguous_proc_link"

	CodeForegroundUnavailable = "process_foreground_unavailable"

	CodeForegroundAmbiguous = "process_foreground_ambiguous"

	CodeForegroundChanged = "process_foreground_changed"

	CodeCanceled = "operation_canceled"

	CodeTimeout = "operation_timed_out"

	CodeFailed = "process_failed"

	sanitizedPath = "[sanitized]"
)

var (
	ErrInvalidPID = errors.New("a positive process identifier is required")

	ErrBirthRequired = errors.New("expected birth identity precondition is required")

	ErrIdentityMismatch = errors.New("process birth identity does not match expected identity")

	ErrProcessVanished = errors.New("process does not exist or terminated during observation")

	ErrProcessZombie = errors.New("process is in a zombie or terminated state")

	ErrEmptyCmdline = errors.New("process command line is empty")

	ErrMalformedCmdline = errors.New("process command line is malformed or truncated")

	ErrMalformedEnviron = errors.New("process environment is malformed or truncated")

	ErrDuplicateEnvironmentVariable = errors.New("duplicate requested environment variable in process environment")

	ErrMalformedStat = errors.New("process stat is malformed or truncated")

	ErrMalformedIdentity = errors.New("process birth identity format is invalid")

	ErrInputTooLarge = errors.New("process input exceeded bounded read limit")

	ErrInvalidVariableName = errors.New("requested environment variable name is invalid")

	ErrEnvironmentLimitExceeded = errors.New("requested environment keys exceed count or byte bounds")

	ErrUnsupported = errors.New("process observation is not supported on this platform")

	ErrAmbiguousProcLink = errors.New("process link path is ambiguous")

	ErrForegroundUnavailable = errors.New("foreground process is unavailable")

	ErrForegroundAmbiguous = errors.New("foreground process is ambiguous")

	ErrForegroundChanged = errors.New("foreground process changed during observation")
)

func wrapError(err error, op string) error {
	if err == nil {
		return nil
	}

	sanitized := sanitizePathError(err)

	return oops.In(Domain).Code(errorCode(sanitized)).With("operation", op).Wrap(sanitized)
}

func sanitizePathError(err error) error {
	if err == nil {
		return nil
	}

	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		unwrapped := joined.Unwrap()
		sanitized := make([]error, len(unwrapped))

		for i, u := range unwrapped {
			sanitized[i] = sanitizePathError(u)
		}

		return errors.Join(sanitized...)
	}

	if pathErr, ok := errors.AsType[*os.PathError](err); ok {
		return &os.PathError{
			Op:   pathErr.Op,
			Path: sanitizedPath,
			Err:  sanitizePathError(pathErr.Err),
		}
	}

	if linkErr, ok := errors.AsType[*os.LinkError](err); ok {
		return &os.LinkError{
			Op:  linkErr.Op,
			Old: sanitizedPath,
			New: sanitizedPath,
			Err: sanitizePathError(linkErr.Err),
		}
	}

	return err
}

func errorCode(err error) string {
	if code, ok := classifyLifecycleError(err); ok {
		return code
	}

	if code, ok := classifyObservationError(err); ok {
		return code
	}

	return classifyContentError(err)
}

func classifyLifecycleError(err error) (string, bool) {
	switch {
	case errors.Is(err, context.Canceled):
		return CodeCanceled, true
	case errors.Is(err, context.DeadlineExceeded):
		return CodeTimeout, true
	case errors.Is(err, ErrInvalidPID):
		return CodeInvalidPID, true
	case errors.Is(err, ErrBirthRequired):
		return CodeBirthRequired, true
	case errors.Is(err, ErrIdentityMismatch):
		return CodeIdentityMismatch, true
	case errors.Is(err, ErrProcessVanished):
		return CodeProcessVanished, true
	case errors.Is(err, ErrProcessZombie):
		return CodeProcessZombie, true
	default:
		return "", false
	}
}

func classifyContentError(err error) string {
	switch {
	case errors.Is(err, ErrEmptyCmdline):
		return CodeEmptyCmdline
	case errors.Is(err, ErrMalformedCmdline):
		return CodeMalformedCmdline
	case errors.Is(err, ErrMalformedEnviron):
		return CodeMalformedEnviron
	case errors.Is(err, ErrDuplicateEnvironmentVariable):
		return CodeDuplicateVariable
	case errors.Is(err, ErrMalformedStat):
		return CodeMalformedStat
	case errors.Is(err, ErrMalformedIdentity):
		return CodeIdentityMismatch
	case errors.Is(err, ErrInputTooLarge):
		return CodeInputTooLarge
	case errors.Is(err, ErrInvalidVariableName):
		return CodeInvalidVariable
	case errors.Is(err, ErrEnvironmentLimitExceeded):
		return CodeInputTooLarge
	default:
		return CodeFailed
	}
}

func classifyObservationError(err error) (string, bool) {
	switch {
	case errors.Is(err, ErrUnsupported):
		return CodeUnsupported, true
	case errors.Is(err, ErrAmbiguousProcLink):
		return CodeAmbiguousProcLink, true
	case errors.Is(err, ErrForegroundUnavailable):
		return CodeForegroundUnavailable, true
	case errors.Is(err, ErrForegroundAmbiguous):
		return CodeForegroundAmbiguous, true
	case errors.Is(err, ErrForegroundChanged):
		return CodeForegroundChanged, true
	default:
		return "", false
	}
}
