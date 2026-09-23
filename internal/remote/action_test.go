//go:build linux || darwin

package remote

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/session"
)

func liveActionRequest(t *testing.T) ActionRequest {
	t.Helper()

	request := ActionRequest{
		Operation:           ActionRename,
		ConnectionID:        testConnectionID,
		ConfigDigest:        testConfigDigest,
		PeerID:              testPeerID,
		Kind:                string(session.KindLive),
		TmuxServerSocket:    "/tmp/tmux.sock",
		TmuxServerPID:       42,
		TmuxServerStartedAt: time.Unix(100, 0).UTC(),
		SessionID:           "$1",
		SessionName:         testSessionName,
		SessionCreatedAt:    time.Unix(200, 0).UTC(),
		NewName:             "api",
	}

	id, err := NewInvocationID()
	if err != nil {
		t.Fatal(err)
	}

	digest, err := IntentDigest(request)
	if err != nil {
		t.Fatal(err)
	}

	request.InvocationID = id
	request.IntentDigest = digest

	return request
}

func TestActionRequestRoundTripPreservesLiveIncarnation(t *testing.T) {
	request := liveActionRequest(t)

	payload, err := EncodeActionRequest(request)
	if err != nil {
		t.Fatalf("EncodeActionRequest() error = %v", err)
	}

	decoded, err := DecodeActionRequest(payload)
	if err != nil {
		t.Fatalf("DecodeActionRequest() error = %v", err)
	}

	if !reflect.DeepEqual(decoded, request) {
		t.Fatalf("action request round trip = %#v, want %#v", decoded, request)
	}
}

func TestSavedActionRequestCarriesOnlyStableSavedIdentity(t *testing.T) {
	request := ActionRequest{
		Operation:        ActionRestore,
		ConnectionID:     testConnectionID,
		ConfigDigest:     testConfigDigest,
		PeerID:           testPeerID,
		Kind:             string(session.KindSaved),
		TmuxServerSocket: "/tmp/tmux.sock",
		SessionID:        "saved-session",
		SessionName:      testSessionName,
		Generation:       "saved-generation",
	}

	digest, err := IntentDigest(request)
	if err != nil {
		t.Fatal(err)
	}

	request.InvocationID = strings.Repeat("a", 32)
	request.IntentDigest = digest

	payload, err := EncodeActionRequest(request)
	if err != nil {
		t.Fatalf("EncodeActionRequest() error = %v", err)
	}

	decoded, err := DecodeActionRequest(payload)
	if err != nil {
		t.Fatalf("DecodeActionRequest() error = %v", err)
	}

	if !reflect.DeepEqual(decoded, request) {
		t.Fatalf("saved action request = %#v, want %#v", decoded, request)
	}
}

func TestActionRequestRejectsInvalidOperationShape(t *testing.T) {
	base := liveActionRequest(t)

	tests := map[string]func(*ActionRequest){
		"unsupported operation": func(r *ActionRequest) { r.Operation = Action("push") },
		"missing invocation":    func(r *ActionRequest) { r.InvocationID = "" },
		"short invocation":      func(r *ActionRequest) { r.InvocationID = "abc" },
		"saved with session id": func(r *ActionRequest) {
			r.Operation = ActionRestore
			r.Kind = string(session.KindSaved)
		},
		"live without incarnation": func(r *ActionRequest) { r.TmuxServerStartedAt = time.Time{} },
		"live without session id":  func(r *ActionRequest) { r.SessionID = "" },
		"rename without new name":  func(r *ActionRequest) { r.NewName = "" },
		"kill with new name":       func(r *ActionRequest) { r.Operation = ActionKill },
		"negative pid":             func(r *ActionRequest) { r.TmuxServerPID = -1 },
		"directory on live target": func(r *ActionRequest) { r.Directory = "/work" },
		"control character":        func(r *ActionRequest) { r.SessionName = "dev\x1b[31m" },
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			request := base
			mutate(&request)

			if _, err := EncodeActionRequest(request); !errors.Is(err, ErrInvalidFrame) {
				t.Fatalf("EncodeActionRequest() error = %v, want invalid frame", err)
			}
		})
	}
}

func TestCreateActionRequiresDirectoryWithoutLiveIdentity(t *testing.T) {
	valid := ActionRequest{
		Operation:    ActionCreate,
		ConnectionID: testConnectionID,
		ConfigDigest: testConfigDigest,
		PeerID:       testPeerID,
		SessionName:  "new",
		Directory:    "/work",
	}

	digest, err := IntentDigest(valid)
	if err != nil {
		t.Fatal(err)
	}

	valid.InvocationID = strings.Repeat("f", 32)
	valid.IntentDigest = digest

	if _, err := EncodeActionRequest(valid); err != nil {
		t.Fatalf("EncodeActionRequest(valid) error = %v", err)
	}

	withoutDirectory := valid
	withoutDirectory.Directory = ""

	if _, err := EncodeActionRequest(withoutDirectory); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("EncodeActionRequest(no directory) error = %v, want invalid frame", err)
	}

	withLiveIdentity := valid
	withLiveIdentity.SessionID = "$1"
	withLiveIdentity.TmuxServerPID = 42
	withLiveIdentity.TmuxServerStartedAt = time.Unix(100, 0).UTC()
	withLiveIdentity.SessionCreatedAt = time.Unix(200, 0).UTC()

	if _, err := EncodeActionRequest(withLiveIdentity); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("EncodeActionRequest(live identity) error = %v, want invalid frame", err)
	}
}

func TestDecodeActionRequestRejectsTamperedIntent(t *testing.T) {
	request := liveActionRequest(t)

	encoded, err := EncodeActionRequest(request)
	if err != nil {
		t.Fatal(err)
	}

	tampered := bytes.Replace(encoded, []byte(`"newName":"api"`), []byte(`"newName":"other"`), 1)
	if bytes.Equal(tampered, encoded) {
		t.Fatal("fixture did not tamper the request")
	}

	decoded, err := DecodeActionRequest(tampered)
	if err != nil {
		t.Fatalf("DecodeActionRequest() error = %v", err)
	}

	if err := VerifyIntentDigest(decoded); !errors.Is(err, ErrInvocationConflict) {
		t.Fatalf("VerifyIntentDigest() error = %v, want conflict", err)
	}
}

func TestIntentDigestExcludesTransportIdentity(t *testing.T) {
	request := liveActionRequest(t)

	digest, err := IntentDigest(request)
	if err != nil {
		t.Fatal(err)
	}

	retried := request
	retried.InvocationID = strings.Repeat("b", 32)
	retried.ConnectionID = "other-connection"
	retried.ConfigDigest = "other-digest"

	retriedDigest, err := IntentDigest(retried)
	if err != nil {
		t.Fatal(err)
	}

	if digest != retriedDigest {
		t.Fatalf("intent digest changed across transport identity: %q != %q", digest, retriedDigest)
	}

	mutated := request
	mutated.SessionID = "$2"

	mutatedDigest, err := IntentDigest(mutated)
	if err != nil {
		t.Fatal(err)
	}

	if digest == mutatedDigest {
		t.Fatal("intent digest ignored a target change")
	}
}

func TestDecodeActionResultRejectsUnboundedOrUnknownResponses(t *testing.T) {
	valid := `{"invocationId":"` + strings.Repeat("a", 32) + `","ok":true,"code":""}`

	for _, output := range [][]byte{
		nil,
		[]byte(`{"invocationId":"` + strings.Repeat("a", 32) + `","ok":false,"code":"raw remote failure"}`),
		[]byte(`{"invocationId":"` + strings.Repeat("a", 32) + `","ok":true,"code":"unavailable"}`),
		[]byte(`{"invocationId":"` + strings.Repeat("a", 32) + `","ok":false}`),
		[]byte(`{"ok":true,"code":""}`),
		[]byte(strings.Replace(valid, `"ok":true`, `"ok":true,"ok":true`, 1)),
		[]byte(strings.Replace(valid, `"code":""`, `"code":"","extra":1`, 1)),
		bytes.Repeat([]byte{'x'}, MaxActionBytes+1),
	} {
		if _, err := DecodeActionResult(output); !errors.Is(err, ErrInvalidFrame) {
			t.Fatalf("DecodeActionResult(%q) error = %v, want invalid frame", output, err)
		}
	}
}

func TestActionResultAcceptsClosedOutcomeVocabulary(t *testing.T) {
	id := strings.Repeat("c", 32)

	for _, code := range []string{
		ActionCodeInvalidRequest, ActionCodeForbidden, ActionCodeUnsupported,
		ActionCodeConflict, ActionCodeInvocationConflict, ActionCodeTargetChanged,
		ActionCodeUnavailable, ActionCodePartial, ActionCodeUnknownOutcome, ActionCodeNotFound,
	} {
		result := ActionResult{InvocationID: id, OK: false, Code: code}
		if err := ValidateActionResult(result); err != nil {
			t.Fatalf("ValidateActionResult(%q) error = %v", code, err)
		}
	}

	if err := ValidateActionResult(ActionResult{InvocationID: id, OK: true}); err != nil {
		t.Fatalf("ValidateActionResult(success) error = %v", err)
	}
}

func TestActionResultRoundTripPreservesDurableOperation(t *testing.T) {
	result := ActionResult{
		InvocationID:       strings.Repeat("d", 32),
		OK:                 true,
		DurableOperationID: strings.Repeat("e", 32),
	}

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
