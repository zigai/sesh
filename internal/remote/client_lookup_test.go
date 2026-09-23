//go:build linux || darwin

package remote

import (
	"errors"
	"testing"
	"time"
)

func TestInvocationLookupRequestRoundTrip(t *testing.T) {
	request := InvocationLookupRequest{
		Version: ProtocolVersion, PeerID: testPeerID, InvocationID: "0123456789abcdef0123456789abcdef",
	}

	payload, err := EncodeInvocationLookupRequest(request)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeInvocationLookupRequest(payload)
	if err != nil {
		t.Fatal(err)
	}

	if decoded != request {
		t.Fatalf("round trip = %#v, want %#v", decoded, request)
	}

	payload = append(payload[:len(payload)-1], []byte(`,"unexpected":true}`)...)
	if _, err := DecodeInvocationLookupRequest(payload); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("unknown field error = %v, want ErrInvalidFrame", err)
	}
}

func TestExecutePeerLookupReadsReceiptWithoutRedispatch(t *testing.T) {
	dir, _ := openTestReceiptDir(t)
	action := liveActionRequest(t)

	invocation, admitted, err := AdmitInvocation(t.Context(), dir, action, time.Unix(1000, 0).UTC())
	if err != nil || !admitted {
		t.Fatalf("AdmitInvocation() = %#v, %v, want admitted", invocation, err)
	}

	request := InvocationLookupRequest{Version: ProtocolVersion, PeerID: testPeerID, InvocationID: action.InvocationID}

	pending := ExecutePeerLookup(dir, testPeerID, request)
	if pending.OK || pending.Code != ActionCodeUnknownOutcome {
		t.Fatalf("pending lookup = %#v, want unknown outcome", pending)
	}

	operationID := "abcdefabcdefabcdefabcdefabcdefab"
	if err := ResolveInvocation(t.Context(), dir, action.InvocationID, ActionCodePartial, operationID, time.Unix(1001, 0).UTC()); err != nil {
		t.Fatal(err)
	}

	resolved := ExecutePeerLookup(dir, testPeerID, request)
	if resolved.OK || resolved.Code != ActionCodePartial || resolved.DurableOperationID != operationID {
		t.Fatalf("resolved lookup = %#v, want retained partial receipt", resolved)
	}

	foreign := ExecutePeerLookup(dir, "different-peer", request)
	if foreign.Code != ActionCodeTargetChanged {
		t.Fatalf("foreign lookup = %#v, want target changed", foreign)
	}

	request.InvocationID = "fedcbafedcbafedcbafedcbafedcbafe"

	absent := ExecutePeerLookup(dir, testPeerID, request)
	if absent.Code != ActionCodeNotFound {
		t.Fatalf("absent lookup = %#v, want not found", absent)
	}
}
