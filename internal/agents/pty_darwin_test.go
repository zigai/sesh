//go:build darwin && integration

package agents_test

import (
	"os"
	"testing"
)

func openTestPTY(t *testing.T) (*os.File, *os.File, error) {
	t.Helper()

	t.Skip("interactive pty fixtures are unavailable on darwin")

	return nil, nil, nil
}
