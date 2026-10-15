//go:build linux || darwin

package doctor

import "fmt"

func (s *Service) checkCustomFields() []Result {
	if s.opts.ConfigError != "" {
		return []Result{skipped(CheckFields, CheckConfigFile, "configuration did not load")}
	}

	results := make([]Result, 0, len(s.opts.CustomFields))

	for _, field := range s.opts.CustomFields {
		results = append(results, s.checkCustomField(field))
	}

	return results
}

func (s *Service) checkCustomField(field CustomField) Result {
	id := FieldCheck(field.Name)

	if field.Command == "" {
		return newResult(id, StatusOK, "Runs a shell command")
	}

	path, found := lookPath(s.opts.PathEnv, field.Command)
	if !found {
		return newResult(id, StatusFail, fmt.Sprintf("Command %q is not on PATH", field.Command)).
			withReason(ReasonNotFound).
			withManual(fmt.Sprintf("Install %s or change display.fields.%s.command", field.Command, field.Name))
	}

	return newResult(id, StatusOK, "Runs "+DisplayPath(path))
}
