//go:build linux || darwin

package remote

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/action"
)

const testKillInput = `{"session":{"kind":"live","id":"$1","name":"work","socket":"/tmp/tmux.sock","serverPid":42,` +
	`"serverStartedAt":"1970-01-01T00:01:40Z","createdAt":"1970-01-01T00:03:20Z","generation":""},"force":false}`

func liveActionRequest(t *testing.T) ActionRequest {
	t.Helper()

	request := ActionRequest{
		InvocationID: "",
		IntentDigest: "",
		Action:       action.SessionKill.ID,
		ConnectionID: testConnectionID,
		ConfigDigest: testConfigDigest,
		PeerID:       testPeerID,
		Socket:       "/tmp/tmux.sock",
		Input:        jsontext.Value(testKillInput),
	}

	id, err := NewInvocationID()
	if err != nil {
		t.Fatal(err)
	}

	request.InvocationID = id

	digest, err := IntentDigest(request)
	if err != nil {
		t.Fatal(err)
	}

	request.IntentDigest = digest

	return request
}

func TestActionRequestRoundTripPreservesInput(t *testing.T) {
	request := liveActionRequest(t)

	encoded, err := EncodeActionRequest(request)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeActionRequest(encoded)
	if err != nil {
		t.Fatalf("DecodeActionRequest() error = %v", err)
	}

	if !reflect.DeepEqual(decoded, request) {
		t.Fatalf("round trip = %#v, want %#v", decoded, request)
	}

	if err := VerifyIntentDigest(decoded); err != nil {
		t.Fatalf("decoded request lost its intent: %v", err)
	}
}

func TestDecodeActionRequestRejectsMalformedRequests(t *testing.T) {
	tests := map[string]func(*ActionRequest){
		"unregistered action shape": func(request *ActionRequest) { request.Action = "kill" },
		"short invocation":          func(request *ActionRequest) { request.InvocationID = "short" },
		"short digest":              func(request *ActionRequest) { request.IntentDigest = "short" },
		"missing peer":              func(request *ActionRequest) { request.PeerID = "" },
		"input is not an object":    func(request *ActionRequest) { request.Input = jsontext.Value(`["$1"]`) },
		"missing input":             func(request *ActionRequest) { request.Input = nil },
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			request := liveActionRequest(t)
			mutate(&request)

			if err := ValidateActionRequest(request); !errors.Is(err, ErrInvalidFrame) {
				t.Fatalf("ValidateActionRequest() error = %v, want %v", err, ErrInvalidFrame)
			}
		})
	}

	encoded, err := EncodeActionRequest(liveActionRequest(t))
	if err != nil {
		t.Fatal(err)
	}

	for name, payload := range map[string][]byte{
		"unknown member": bytes.Replace(encoded, []byte(`"socket"`), []byte(`"extra":1,"socket"`), 1),
		"oversized":      bytes.Repeat([]byte{' '}, MaxActionBytes+1),
		"empty":          nil,
	} {
		if _, err := DecodeActionRequest(payload); !errors.Is(err, ErrInvalidFrame) {
			t.Fatalf("DecodeActionRequest(%s) error = %v, want %v", name, err, ErrInvalidFrame)
		}
	}
}

func TestIntentDigestBindsActionPeerServerAndInput(t *testing.T) {
	request := liveActionRequest(t)

	for name, mutate := range map[string]func(*ActionRequest){
		"action": func(request *ActionRequest) { request.Action = action.SessionDelete.ID },
		"peer":   func(request *ActionRequest) { request.PeerID = "other-peer" },
		"socket": func(request *ActionRequest) { request.Socket = "/tmp/other.sock" },
		"input": func(request *ActionRequest) {
			request.Input = jsontext.Value(strings.Replace(testKillInput, `"$1"`, `"$2"`, 1))
		},
	} {
		changed := request
		mutate(&changed)

		if err := VerifyIntentDigest(changed); !errors.Is(err, ErrInvocationConflict) {
			t.Fatalf("changing the %s kept the intent digest: %v", name, err)
		}
	}

	retried := request
	retried.ConnectionID = "renamed-connection"

	if err := VerifyIntentDigest(retried); err != nil {
		t.Fatalf("the local connection name changed the intent: %v", err)
	}
}

func TestDecodeActionResultRejectsUnboundedOrUnknownResponses(t *testing.T) {
	id := strings.Repeat("a", 32)
	valid := `{"invocationId":"` + id + `","action":"session.kill","ok":true,"code":""}`

	for _, output := range [][]byte{
		nil,
		[]byte(`{"invocationId":"` + id + `","action":"session.kill","ok":false,"code":"raw remote failure"}`),
		[]byte(`{"invocationId":"` + id + `","action":"session.kill","ok":true,"code":"unavailable"}`),
		[]byte(`{"invocationId":"` + id + `","action":"","ok":true,"code":""}`),
		[]byte(`{"invocationId":"` + id + `","action":"session.kill","ok":false}`),
		[]byte(`{"action":"session.kill","ok":true,"code":""}`),
		[]byte(strings.Replace(valid, `"code":""`, `"code":"","output":{"name":`, 1)),
		[]byte(strings.Replace(valid, `"code":""`, `"code":"","extra":1`, 1)),
		bytes.Repeat([]byte{'x'}, MaxResultBytes+1),
	} {
		if _, err := DecodeActionResult(output); !errors.Is(err, ErrInvalidFrame) {
			t.Fatalf("DecodeActionResult(%.80q) error = %v, want invalid frame", output, err)
		}
	}
}

func TestActionResultRoundTripPreservesOutput(t *testing.T) {
	for _, result := range []ActionResult{
		{InvocationID: strings.Repeat("d", 32), Action: action.SessionKill.ID, OK: true, Code: "", Output: jsontext.Value(`{"session":{"id":"$1"}}`)},
		{InvocationID: strings.Repeat("d", 32), Action: action.SnapshotRestore.ID, OK: false, Code: action.CodePartial, Output: jsontext.Value(`{"status":"partial"}`)},
		{InvocationID: strings.Repeat("d", 32), Action: "", OK: false, Code: action.CodeInvalidArgument, Output: nil},
	} {
		var output bytes.Buffer
		if err := WriteActionResult(&output, result); err != nil {
			t.Fatal(err)
		}

		decoded, err := DecodeActionResult(output.Bytes())
		if err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(decoded, result) {
			t.Fatalf("action result round trip = %#v, want %#v", decoded, result)
		}
	}
}

func TestInvocationIdentifiersAreUnpredictable(t *testing.T) {
	seen := make(map[string]bool)

	for range 64 {
		id, err := NewInvocationID()
		if err != nil {
			t.Fatal(err)
		}

		if !ValidInvocationID(id) {
			t.Fatalf("NewInvocationID() = %q, want a valid identifier", id)
		}

		if seen[id] {
			t.Fatalf("NewInvocationID() repeated %q", id)
		}

		seen[id] = true
	}
}
