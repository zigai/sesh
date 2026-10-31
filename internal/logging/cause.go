//go:build linux || darwin

package logging

import (
	"github.com/rs/zerolog"
	"github.com/samber/oops"
)

var loggableContextKeys = map[string]bool{
	"action": true, "artifact": true, "barrier": true, "binding": true, "blocked": true, "bridge_socket": true,
	"column": true, "command": true, "connection_id": true, "dir": true, "directories": true, "directory": true,
	"domain": true, "event": true, "exit_code": true, "field": true, "file": true,
	"generation": true, "group_name": true, "hook_count": true, "hook_event": true, "hook_index": true,
	"host": true, "invocation": true, "key": true, "kind": true, "line": true, "location": true, "name": true,
	"observed_peer_id": true, "outcome": true, "pane_id": true, "path": true, "phase": true, "reason": true,
	"repository": true, "rewrite_id": true, "service": true, "skipped": true,
	"socket_name": true, "step_id": true, "version": true, "window_id": true,
}

// Cause returns a field function for [zerolog.Event.Func] that records err as "error", the deepest
// oops domain, code and operation recorded on it as "error_domain", "error_code" and "error_step",
// and the oops context values under keys known to hold identifiers, paths, positions or fixed reasons
// as the "error_context" object. Context such as command output or peer-supplied text is left out. A
// nil err adds nothing.
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

		if context := loggableContext(diagnostic.Context()); len(context) > 0 {
			event.Dict("error_context", zerolog.Dict().Fields(context))
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

func loggableContext(context map[string]any) map[string]any {
	loggable := make(map[string]any, len(context))

	for key, value := range context {
		if loggableContextKeys[key] {
			loggable[key] = value
		}
	}

	return loggable
}
