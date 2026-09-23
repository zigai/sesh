//go:build darwin && integration

package session_test

import (
	"os"
	"testing"
)

func openTestPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()

	t.Skip("interactive pty fixtures are unavailable on darwin")

	return nil, nil
}
