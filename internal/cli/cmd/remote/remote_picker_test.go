//go:build linux || darwin

package remote

import (
	"errors"
	"testing"

	"github.com/zigai/sesh/internal/remote"
)

var errRemoteActionTransport = errors.New("remote action transport failed")

func TestRemoteActionResultErrorRetainsReceiptAndTransportCause(t *testing.T) {
	tests := []struct {
		name string
		code string
		want error
	}{
		{name: "uncertain", code: remote.ActionCodeUnknownOutcome, want: remote.ErrActionUncertain},
		{name: "partial", code: remote.ActionCodePartial, want: remote.ErrActionPartial},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := remote.ActionResult{
				InvocationID:       "0123456789abcdef0123456789abcdef",
				OK:                 false,
				Code:               test.code,
				DurableOperationID: "restore-operation",
			}

			err := remoteActionResultError(result, errRemoteActionTransport)
			if !errors.Is(err, test.want) || !errors.Is(err, errRemoteActionTransport) {
				t.Fatalf("remoteActionResultError() = %v, want %v and transport causes", err, test.want)
			}

			var outcomeErr *remote.ActionOutcomeError
			if !errors.As(err, &outcomeErr) || outcomeErr.Result != result {
				t.Fatalf("remoteActionResultError() outcome = %#v, want %#v", outcomeErr, result)
			}
		})
	}
}
