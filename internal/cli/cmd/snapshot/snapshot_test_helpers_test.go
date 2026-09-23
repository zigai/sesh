package cmdsnapshot

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/zigai/sesh/internal/cli/clibase"
)

const (
	testRepositoryFlag = "--repository"
	testSocketFlag     = "--socket"
	testLeaseRootFlag  = "--lease-root"
	testSessionFlag    = "--session"
	testJSONFlag       = "--json"
	testConfigFlag     = "--config"
	testNoConfigFlag   = "--no-config"
	jsonFlagName       = "json"
)

var errSensitiveRaw = errors.New("/private/raw/failure")

type testJSONEnvelope struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Command       string                    `json:"command"`
	OK            bool                      `json:"ok"`
	Result        json.RawMessage           `json:"result"`
	Error         *clibase.JSONErrorPayload `json:"error"`
}

func singleRuntimeEnvelope(t *testing.T, data []byte) testJSONEnvelope {
	t.Helper()

	decoder := json.NewDecoder(bytes.NewReader(data))

	var env testJSONEnvelope
	if err := decoder.Decode(&env); err != nil {
		t.Fatalf("invalid JSON %q: %v", data, err)
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("extra output %q: %v", data, err)
	}

	return env
}

func decodeJSONPayload[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()

	var val T
	if err := json.Unmarshal(raw, &val); err != nil {
		t.Fatalf("failed to unmarshal JSON payload: %v\nraw: %s", err, string(raw))
	}

	return val
}
