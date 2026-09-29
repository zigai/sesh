package cmdsession_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/zigai/sesh/internal/testsupport/clitest"
)

const (
	testRepositoryFlag = "--repository"
	testSocketFlag     = "--socket"
	testConfigFlag     = "--config"
	testJSONFlag       = "--json"
	testNoConfigFlag   = "--no-config"
)

var camelCaseKeyPattern = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*$`)

type commandExecution = clitest.Execution

func executeJSON(t *testing.T, args ...string) commandExecution {
	t.Helper()

	return clitest.ExecuteJSON(t, args...)
}

func executeCommand(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	exec := clitest.Execute(t, args...)

	return exec.Stdout, exec.Stderr, exec.Err
}

func assertAllKeysCamelCase(t *testing.T, val any, path string) {
	t.Helper()

	switch v := val.(type) {
	case map[string]any:
		for k, child := range v {
			if !camelCaseKeyPattern.MatchString(k) {
				t.Fatalf("key %q at path %q is not lowerCamelCase", k, path)
			}

			assertAllKeysCamelCase(t, child, path+"."+k)
		}
	case []any:
		for i, child := range v {
			assertAllKeysCamelCase(t, child, fmt.Sprintf("%s[%d]", path, i))
		}
	}
}
