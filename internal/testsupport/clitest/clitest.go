// Package clitest runs the production command pipeline for command tests.
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

// Envelope is the decoded --json command envelope.
type Envelope struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Command       string                    `json:"command"`
	OK            bool                      `json:"ok"`
	Result        json.RawMessage           `json:"result"`
	Error         *clibase.JSONErrorPayload `json:"error"`
}

// Execution is the observable outcome of one command invocation.
type Execution struct {
	Stdout   string
	Stderr   string
	Err      error
	Envelope Envelope
}

// Execute runs args through cli.ExecuteApplication with empty standard input.
func Execute(t *testing.T, args ...string) Execution {
	t.Helper()

	return ExecuteContext(t.Context(), nil, args...)
}

// ExecuteWithInput runs args through cli.ExecuteApplication reading input.
func ExecuteWithInput(t *testing.T, input io.Reader, args ...string) Execution {
	t.Helper()

	return ExecuteContext(t.Context(), input, args...)
}

// ExecuteContext runs args through cli.ExecuteApplication under ctx.
func ExecuteContext(ctx context.Context, input io.Reader, args ...string) Execution {
	var stdout, stderr bytes.Buffer

	err := cli.ExecuteApplication(ctx, args, input, &stdout, &stderr)

	var exec Execution

	exec.Stdout, exec.Stderr, exec.Err = stdout.String(), stderr.String(), err

	return exec
}

// ExecuteJSON runs args and decodes the JSON envelope from stdout, or from
// stderr when stdout is empty. An output that is not an envelope fails the test.
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

// DecodePayload decodes an envelope result into T.
func DecodePayload[T any](t *testing.T, payload json.RawMessage) T {
	t.Helper()

	var value T
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatalf("failed to decode JSON payload: %v; raw=%s", err, string(payload))
	}

	return value
}
