//go:build linux || darwin

package snapshot

import (
	"bytes"
	"errors"
	"testing"

	"github.com/zigai/sesh/internal/snapshot/store"
)

func TestSnapshotGCUncertaintyRetainsPhysicalReceiptOnStdout(t *testing.T) {
	cmd := newSnapshotGCCommand()

	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Flags().Set(jsonFlagName, "true"); err != nil {
		t.Fatal(err)
	}

	result := snapshotGCResultJSON{
		Intents:  []string{"0123456789abcdef0123456789abcdef"},
		Resumed:  true,
		Removed:  nil,
		Deferred: []string{"fedcba9876543210fedcba9876543210"},
		Bytes:    42,
	}

	err := renderGCResult(cmd, result, store.ErrUncertain)
	if !errors.Is(err, store.ErrUncertain) || stderr.Len() != 0 {
		t.Fatalf("uncertain collection receipt = %v / %s / %s", err, stdout.String(), stderr.String())
	}

	envelope := singleRuntimeEnvelope(t, stdout.Bytes())
	receipt := decodeJSONPayload[snapshotGCResultJSON](t, envelope.Result)

	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "publicationUncertain" ||
		len(receipt.Intents) != 1 || !receipt.Resumed || receipt.Bytes != 42 || len(receipt.Deferred) != 1 {
		t.Fatalf("uncertain collection receipt lost evidence: %s", stdout.String())
	}
}
