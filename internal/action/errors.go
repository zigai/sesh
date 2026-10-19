package action

import (
	"context"
	"errors"
)

const (
	CodeInvalidArgument Code = "invalidArgument"
	CodeNotFound        Code = "notFound"
	CodeAlreadyExists   Code = "alreadyExists"
	CodeAttached        Code = "attached"
	CodeTargetChanged   Code = "targetChanged"
	CodeForbidden       Code = "forbidden"
	CodeUnsupported     Code = "unsupported"
	CodeUnavailable     Code = "unavailable"
	CodeConflict        Code = "conflict"
	CodePartial         Code = "partial"
	CodeUncertain       Code = "uncertain"
	CodeCanceled        Code = "canceled"
	CodeFailed          Code = "failed"
)

const ExitCodeCanceled = 130

var errUnspecified = errors.New("action failed")

type Code string

type Error struct {
	Code Code
	Err  error
}

func (code Code) Valid() bool {
	switch code {
	case CodeInvalidArgument, CodeNotFound, CodeAlreadyExists, CodeAttached, CodeTargetChanged,
		CodeForbidden, CodeUnsupported, CodeUnavailable, CodeConflict, CodePartial, CodeUncertain,
		CodeCanceled, CodeFailed:
		return true
	default:
		return false
	}
}

func (e *Error) Error() string {
	if e.Err == nil {
		return string(e.Code)
	}

	return e.Err.Error()
}

func (e *Error) Unwrap() error {
	return e.Err
}

func Wrap(code Code, err error) error {
	if err == nil {
		err = errUnspecified
	}

	if _, ok := errors.AsType[*Error](err); ok {
		return err
	}

	return &Error{Code: code, Err: err}
}

func CodeOf(err error) Code {
	if err == nil {
		return ""
	}

	if classified, ok := errors.AsType[*Error](err); ok {
		return classified.Code
	}

	if errors.Is(err, context.Canceled) {
		return CodeCanceled
	}

	return CodeFailed
}
