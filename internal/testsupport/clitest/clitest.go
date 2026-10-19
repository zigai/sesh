package clitest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/zigai/sesh/internal/cli"
	"github.com/zigai/sesh/internal/cli/clibase"
)

type Envelope struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Command       string                    `json:"command"`
	OK            bool                      `json:"ok"`
	Result        json.RawMessage           `json:"result"`
	Error         *clibase.JSONErrorPayload `json:"error"`
}

type Execution struct {
	Stdout   string
	Stderr   string
	Err      error
	Envelope Envelope
}

func Execute(t *testing.T, args ...string) Execution {
	t.Helper()

	return ExecuteContext(t.Context(), nil, args...)
}

func ExecuteWithInput(t *testing.T, input io.Reader, args ...string) Execution {
	t.Helper()

	return ExecuteContext(t.Context(), input, args...)
}

func ExecuteContext(ctx context.Context, input io.Reader, args ...string) Execution {
	var stdout, stderr bytes.Buffer

	err := cli.ExecuteApplication(ctx, args, input, &stdout, &stderr)

	var exec Execution

	exec.Stdout, exec.Stderr, exec.Err = stdout.String(), stderr.String(), err

	return exec
}

func ExecuteJSON(t *testing.T, args ...string) Execution {
	t.Helper()

	exec := Execute(t, args...)

	target := exec.Stdout
	if target == "" {
		target = exec.Stderr
	}

	if target == "" {
		return exec
	}

	if err := json.Unmarshal([]byte(target), &exec.Envelope); err != nil {
		t.Fatalf("failed to parse JSON envelope: %v; stdout=%q; stderr=%q", err, exec.Stdout, exec.Stderr)
	}

	return exec
}

func DecodePayload[T any](t *testing.T, payload json.RawMessage) T {
	t.Helper()

	var value T
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatalf("failed to decode JSON payload: %v; raw=%s", err, string(payload))
	}

	return value
}
