//go:build darwin

package app

import "context"

// LoginEnvironment returns the current login's environment for a restore: on macOS, the
// environment this process inherited from the login.
func LoginEnvironment(context.Context) map[string]string {
	return processEnvironment()
}
