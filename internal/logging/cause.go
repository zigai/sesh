//go:build linux || darwin

package logging

import (
	"github.com/rs/zerolog"
	"github.com/samber/oops"
)

// Cause returns a field function for [zerolog.Event.Func] that records err as "error", and the
// deepest oops domain, code and operation recorded on it as "error_domain", "error_code" and
// "error_step". A nil err adds nothing.
func Cause(err error) func(*zerolog.Event) {
	return func(event *zerolog.Event) {
		if err == nil {
			return
		}

		event.Err(err)

		diagnostic, ok := oops.AsOops(err)
		if !ok {
			return
		}

		if domain := diagnostic.Domain(); domain != "" {
			event.Str("error_domain", domain)
		}

		if code, ok := diagnostic.Code().(string); ok && code != "" {
			event.Str("error_code", code)
		}

		if step := deepestStep(diagnostic); step != "" {
			event.Str("error_step", step)
		}
	}
}

func deepestStep(diagnostic oops.OopsError) string {
	step := ""

	for _, layer := range diagnostic.Layers() {
		if operation, ok := layer.Context["operation"].(string); ok && operation != "" {
			step = operation
		}
	}

	return step
}
