//go:build linux && integration

package application_test

import (
	"os"
	"testing"

	"github.com/zigai/sesh/internal/testsupport/testenv"
)

func TestMain(m *testing.M) {
	os.Exit(testenv.RunIsolated(m))
}
