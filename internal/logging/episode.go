//go:build linux || darwin

package logging

// Episode tracks the outcomes of a repeated operation so a loop logs when it starts failing, fails
// for a different reason, or recovers, instead of on every attempt.
type Episode struct {
	failing bool
	cause   string
}

// Observe records the latest outcome and reports whether it starts a failure, changes the cause of
// the current one, or ends it.
func (e *Episode) Observe(err error) bool {
	if err == nil {
		recovered := e.failing
		e.failing, e.cause = false, ""

		return recovered
	}

	cause := err.Error()
	changed := !e.failing || cause != e.cause
	e.failing, e.cause = true, cause

	return changed
}
