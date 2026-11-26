//go:build linux || darwin

package app

import (
	"os"
	"strings"
)

func processEnvironment() map[string]string {
	environment := map[string]string{}

	for _, assignment := range os.Environ() {
		if name, value, ok := strings.Cut(assignment, "="); ok && name != "" {
			environment[name] = value
		}
	}

	return environment
}
