//go:build darwin && integration

package cli_test

import (
	"os"
	"testing"
)

func openTestPTY(tb testing.TB) (*os.File, *os.File) {
	tb.Helper()

	tb.Skip("interactive pty fixtures are unavailable on darwin")

	return nil, nil
}
