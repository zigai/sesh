//go:build linux || darwin

package doctor

import (
	"fmt"
	"os/exec"
)

type CustomField struct {
	Name    string
	Command string
}

func checkCustomFields(customFields []CustomField) []Result {
	results := make([]Result, 0, len(customFields))

	for _, field := range customFields {
		results = append(results, checkCustomField(field))
	}

	return results
}

func checkCustomField(field CustomField) Result {
	if field.Command == "" {
		return Result{
			Check:   CheckCustomField,
			Status:  StatusOK,
			Reason:  "",
			Message: fmt.Sprintf("custom.%s runs a shell command", field.Name),
			Fix:     "",
		}
	}

	path, err := exec.LookPath(field.Command)
	if err != nil {
		return Result{
			Check:   CheckCustomField,
			Status:  StatusError,
			Reason:  ReasonNotFound,
			Message: fmt.Sprintf("custom.%s command %q was not found", field.Name, field.Command),
			Fix:     fmt.Sprintf("install %s or change display.fields.%s.command", field.Command, field.Name),
		}
	}

	return Result{
		Check:   CheckCustomField,
		Status:  StatusOK,
		Reason:  "",
		Message: fmt.Sprintf("custom.%s runs %s", field.Name, path),
		Fix:     "",
	}
}
