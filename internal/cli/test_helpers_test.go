package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"
)

func newCaptureCLIServer(t *testing.T) *gotmux.Server {
	t.Helper()
	server := tmuxtest.NewServer(t)

	windows, err := server.Windows(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, window := range windows {
		options := window.Handle().Options()
		if err := options.SetAutomaticRename(t.Context(), false); err != nil {
			t.Fatal(err)
		}

		if err := options.SetAllowRename(t.Context(), false); err != nil {
			t.Fatal(err)
		}
	}

	return server
}

const (
	testRepositoryFlag        = "--repository"
	testSocketFlag            = "--socket"
	testLeaseRootFlag         = "--lease-root"
	testSessionFlag           = "--session"
	testProcessEnrichmentFlag = "--process-enrichment"
	testProcessEnvFlag        = "--process-env"
	testGenerationFlag        = "--generation"
	testJSONFlag              = "--json"
	testConfigFlag            = "--config"
	testNoConfigFlag          = "--no-config"
	snapshotCmd               = "snapshot"
)

var camelCaseKeyPattern = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*$`)

type testJSONEnvelope struct {
	SchemaVersion int               `json:"schemaVersion"`
	Command       string            `json:"command"`
	OK            bool              `json:"ok"`
	Result        json.RawMessage   `json:"result"`
	Error         *jsonErrorPayload `json:"error"`
}

type commandExecution struct {
	stdout   string
	stderr   string
	err      error
	envelope testJSONEnvelope
}

func executeCommand(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	err := ExecuteApplication(t.Context(), args, nil, &stdout, &stderr)

	return stdout.String(), stderr.String(), err
}

func executeJSON(t *testing.T, args ...string) commandExecution {
	t.Helper()

	rawOut, rawErr, err := executeCommand(t, args...)

	target := rawOut
	if target == "" {
		target = rawErr
	}

	var env testJSONEnvelope

	if target != "" {
		decoder := json.NewDecoder(strings.NewReader(target))
		if decErr := decoder.Decode(&env); decErr != nil {
			t.Fatalf("failed to decode JSON envelope from %q (err: %v)\nstdout: %q\nstderr: %q", target, decErr, rawOut, rawErr)
		}

		var extra any
		if extraErr := decoder.Decode(&extra); !errors.Is(extraErr, io.EOF) {
			t.Fatalf("JSON output contains more than one document: %q", target)
		}
	}

	return commandExecution{
		stdout:   rawOut,
		stderr:   rawErr,
		err:      err,
		envelope: env,
	}
}

func decodeJSONPayload[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()

	var val T
	if err := json.Unmarshal(raw, &val); err != nil {
		t.Fatalf("failed to unmarshal JSON payload: %v\nraw: %s", err, string(raw))
	}

	return val
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
