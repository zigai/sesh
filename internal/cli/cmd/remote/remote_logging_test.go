//go:build linux || darwin

package remote

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/remote"
)

func TestPeerActionLogsReceiptOutcomeWithoutIdentities(t *testing.T) {
	cases := []struct {
		code   string
		level  string
		status string
	}{
		{"", "info", "ok"},
		{remote.ActionCodeForbidden, "error", "failed"},
		{remote.ActionCodeUnavailable, "error", "failed"},
		{remote.ActionCodePartial, "warn", "degraded"},
		{remote.ActionCodeUnknownOutcome, "warn", "degraded"},
	}
	for _, tc := range cases {
		t.Run(tc.status+tc.code, func(t *testing.T) {
			var logs bytes.Buffer

			ctx := zerolog.New(&logs).WithContext(t.Context())
			logPeerActionResult(ctx, remote.ActionResult{
				InvocationID: "PRIVATE_INVOCATION", DurableOperationID: "PRIVATE_OPERATION",
				OK: tc.code == "", Code: tc.code,
			})

			if bytes.Count(logs.Bytes(), []byte("\n")) != 1 || strings.Contains(logs.String(), "PRIVATE_") {
				t.Fatalf("unexpected receipt log: %s", logs.String())
			}

			var record map[string]any
			if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
				t.Fatal(err)
			}

			want := map[string]any{
				"component": "remote", "operation": "remote_peer_action", "level": tc.level,
				"status": tc.status, "phase": "receipt", "message": "remote action receipt resolved",
			}
			if tc.code != "" {
				want["reason"] = tc.code
			}

			if diff := cmp.Diff(want, record); diff != "" {
				t.Fatalf("incorrect receipt schema: %s", diff)
			}
		})
	}

	cmd, _, err := NewCommand().Find([]string{"peer", "action"})
	if err != nil || cmd.Annotations[clibase.AnnotationSuccessOwned] != "true" {
		t.Fatal("peer action receipt must own its outcome")
	}
}
