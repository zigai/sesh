//go:build linux || darwin

package remote

import (
	"encoding/json/jsontext"
	"errors"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/action"
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
	admittedRequest := liveActionRequest(t)

	invocation, admitted, err := AdmitInvocation(t.Context(), dir, admittedRequest, time.Unix(1000, 0).UTC())
	if err != nil || !admitted {
		t.Fatalf("AdmitInvocation() = %#v, %v, want admitted", invocation, err)
	}

	request := InvocationLookupRequest{Version: ProtocolVersion, PeerID: testPeerID, InvocationID: admittedRequest.InvocationID}

	pending := ExecutePeerLookup(dir, testPeerID, request)
	if pending.OK || pending.Code != action.CodeUncertain || pending.Action != action.SessionKill.ID {
		t.Fatalf("pending lookup = %#v, want unknown outcome", pending)
	}

	output := jsontext.Value(`{"session":{"id":"$1"}}`)
	if err := ResolveInvocation(t.Context(), dir, admittedRequest.InvocationID, string(action.CodePartial), output, time.Unix(1001, 0).UTC()); err != nil {
		t.Fatal(err)
	}

	resolved := ExecutePeerLookup(dir, testPeerID, request)
	if resolved.OK || resolved.Code != action.CodePartial || string(resolved.Output) != string(output) {
		t.Fatalf("resolved lookup = %#v, want retained partial receipt", resolved)
	}

	foreign := ExecutePeerLookup(dir, "different-peer", request)
	if foreign.Code != action.CodeTargetChanged {
		t.Fatalf("foreign lookup = %#v, want target changed", foreign)
	}

	request.InvocationID = "fedcbafedcbafedcbafedcbafedcbafe"

	absent := ExecutePeerLookup(dir, testPeerID, request)
	if absent.Code != action.CodeNotFound {
		t.Fatalf("absent lookup = %#v, want not found", absent)
	}
}
