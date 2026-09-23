//go:build linux || darwin

package logging

import (
	"errors"
	"strings"

	"github.com/samber/oops"
)

var (
	ErrLogPathEmpty           = errors.New("log path is empty")
	ErrLogPathNotRegular      = errors.New("log path is not a regular file")
	ErrRotationPathNotRegular = errors.New("log rotation path is not regular")
	ErrLogPathChanged         = errors.New("log path changed while opening")
	ErrLogForeign             = errors.New("log file contains foreign or corrupt data")
	ErrRotatingSinkNoFile     = errors.New("rotating log sink has no file descriptor")
	ErrLogInvalidRecord       = errors.New("invalid or partial JSON log record")
	ErrLogRecordTooLong       = errors.New("log record exceeds maximum line length")
	ErrLogReadLimit           = errors.New("retained log read exceeds resource bounds")
	ErrLockTimeout            = errors.New("timeout acquiring log file lock")
)

func wrapErr(err error, code, msg string) error {
	if err == nil {
		return nil
	}

	op := strings.TrimPrefix(code, "log_")
	op = strings.TrimSuffix(op, "_failed")

	return oops.In("logging").Code(code).With("operation", op).Wrapf(err, "%s", msg)
}
