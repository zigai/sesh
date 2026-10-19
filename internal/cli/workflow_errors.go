package cli

import (
	"errors"
)

func commandOutcomeLogged(err error) bool {
	if err == nil {
		return false
	}

	if owner, ok := err.(interface{ OutcomeLogged() bool }); ok {
		return owner.OutcomeLogged()
	}

	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}

		for _, cause := range causes {
			if !commandOutcomeLogged(cause) {
				return false
			}
		}

		return true
	}

	return commandOutcomeLogged(errors.Unwrap(err))
}
