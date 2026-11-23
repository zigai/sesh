package apps

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/zigai/sesh/internal/snapshot/model"
)

const envProgram = "env"

// SplitResumeCommand separates the leading `env NAME=value` assignments a
// harness resume command may carry from the harness argv that follows them.
func SplitResumeCommand(command []string) ([]string, []string) {
	if len(command) == 0 || filepath.Base(command[0]) != envProgram {
		return nil, command
	}

	end := 1
	for end < len(command) && environmentAssignment(command[end]) {
		end++
	}

	return command[1:end], command[end:]
}

func environmentAssignment(argument string) bool {
	name, _, ok := strings.Cut(argument, "=")
	if !ok || name == "" {
		return false
	}

	for index, char := range name {
		letter := char == '_' || (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z')
		if !letter && (index == 0 || char < '0' || char > '9') {
			return false
		}
	}

	return true
}

func withAssignments(environment []model.Variable, assignments []string) []model.Variable {
	result := cloneVariables(environment)

	for _, assignment := range assignments {
		name, value, _ := strings.Cut(assignment, "=")
		variable := model.Variable{
			Name:   model.Bytes(name),
			Value:  model.Bytes(value),
			State:  "set",
			Hidden: false,
		}

		index := slices.IndexFunc(result, func(existing model.Variable) bool {
			return string(existing.Name) == name
		})
		if index >= 0 {
			result[index] = variable
		} else {
			result = append(result, variable)
		}
	}

	return result
}
