//go:build linux || darwin

package remote

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/action"
)

const (
	invocationIDBytes = 16
	intentDigestBytes = 32
)

var (
	ErrInvocationNotFound = errors.New("remote invocation record not found")
	ErrInvocationConflict = errors.New("remote invocation intent does not match")
	ErrActionFailed       = errors.New("remote action failed")

	errNilActionOutput = errors.New("remote action output is not configured")
)

// ActionRequest asks a peer to run one action. Input is the action's typed input as JSON; the
// intent digest binds the invocation ID to the action, peer, tmux server and input, so a retry
// of the same ID with different intent is refused instead of replayed.
type ActionRequest struct {
	InvocationID string         `json:"invocationId"`
	IntentDigest string         `json:"intentDigest"`
	Action       action.ID      `json:"action"`
	ConnectionID string         `json:"connectionId"`
	ConfigDigest string         `json:"configDigest"`
	PeerID       string         `json:"peerId"`
	Socket       string         `json:"socket"`
	Input        jsontext.Value `json:"input"`
}

// ActionResult is a peer's answer to an invocation: success with the action's output, or a
// failure code. A failure may carry output that describes what is known. Action is empty only
// when the peer could not tell which action was meant.
type ActionResult struct {
	InvocationID string         `json:"invocationId"`
	Action       action.ID      `json:"action"`
	OK           bool           `json:"ok"`
	Code         action.Code    `json:"code"`
	Output       jsontext.Value `json:"output,omitzero"`
}

// PeerError is a remote action that failed or whose outcome is unknown. The invocation ID
// finds the peer's receipt with `sesh remote lookup`.
type PeerError struct {
	Code         action.Code
	InvocationID string
	Err          error
}

type canonicalIntent struct {
	Action action.ID      `json:"action"`
	PeerID string         `json:"peerId"`
	Socket string         `json:"socket"`
	Input  jsontext.Value `json:"input"`
}

func (err *PeerError) Error() string {
	message := fmt.Sprintf("%s: %s (invocation %s)", ErrActionFailed, err.Code, err.InvocationID)
	if err.Err != nil {
		message += ": " + err.Err.Error()
	}

	return message
}

func (err *PeerError) Unwrap() []error {
	if err.Err == nil {
		return []error{ErrActionFailed}
	}

	return []error{ErrActionFailed, err.Err}
}

func NewInvocationID() (string, error) {
	value := make([]byte, invocationIDBytes)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", oops.In("remote").With("operation", "new_invocation_id").Wrap(err)
	}

	return hex.EncodeToString(value), nil
}

func IntentDigest(request ActionRequest) (string, error) {
	encoded, err := json.Marshal(canonicalIntent{
		Action: request.Action,
		PeerID: request.PeerID,
		Socket: request.Socket,
		Input:  request.Input,
	})
	if err != nil {
		return "", oops.In("remote").With("operation", "intent_digest").Wrap(err)
	}

	digest := sha256.Sum256(encoded)

	return hex.EncodeToString(digest[:]), nil
}

func EncodeActionRequest(request ActionRequest) ([]byte, error) {
	if err := ValidateActionRequest(request); err != nil {
		return nil, err
	}

	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, oops.In("remote").With("operation", "encode_action_request").Wrap(err)
	}

	if len(encoded) > MaxActionBytes {
		return nil, oops.In("remote").With("operation", "encode_action_request").Wrap(ErrFrameTooLarge)
	}

	return encoded, nil
}

func DecodeActionRequest(payload []byte) (ActionRequest, error) {
	if len(payload) == 0 || len(payload) > MaxActionBytes {
		return ActionRequest{}, ErrInvalidFrame
	}

	request, err := decodeStrict[ActionRequest](bytes.TrimSpace(payload))
	if err != nil {
		return ActionRequest{}, err
	}

	if err := ValidateActionRequest(request); err != nil {
		return ActionRequest{}, err
	}

	return request, nil
}

func VerifyIntentDigest(request ActionRequest) error {
	digest, err := IntentDigest(request)
	if err != nil {
		return err
	}

	if digest != request.IntentDigest {
		return oops.In("remote").With("operation", "verify_intent_digest").Wrap(ErrInvocationConflict)
	}

	return nil
}

func WriteActionResult(output io.Writer, result ActionResult) error {
	if output == nil {
		return errNilActionOutput
	}

	encoded, err := encodeActionResult(result)
	if err != nil {
		return err
	}

	if _, err := output.Write(append(encoded, '\n')); err != nil {
		return oops.In("remote").With("operation", "write_action_result").Wrap(err)
	}

	return nil
}

func DecodeActionResult(output []byte) (ActionResult, error) {
	if len(output) == 0 || len(output) > MaxResultBytes {
		return ActionResult{}, ErrInvalidFrame
	}

	result, err := decodeStrict[ActionResult](bytes.TrimSpace(output))
	if err != nil {
		return ActionResult{}, err
	}

	if err := ValidateActionResult(result); err != nil {
		return ActionResult{}, err
	}

	return result, nil
}

func ValidateActionResult(result ActionResult) error {
	if !ValidInvocationID(result.InvocationID) || len(result.Output) > MaxResultBytes {
		return ErrInvalidFrame
	}

	if (result.Action != "" || result.OK) && !result.Action.Valid() {
		return ErrInvalidFrame
	}

	if len(result.Output) > 0 && !result.Output.IsValid() {
		return ErrInvalidFrame
	}

	if result.OK {
		if result.Code != "" {
			return ErrInvalidFrame
		}

		return nil
	}

	if !result.Code.Valid() {
		return ErrInvalidFrame
	}

	return nil
}

func ValidateActionRequest(request ActionRequest) error {
	if !request.Action.Valid() || !ValidIdentifier(request.PeerID) || !ValidIdentifier(request.ConnectionID) ||
		!ValidIdentifier(request.ConfigDigest) || !ValidText(request.Socket) {
		return ErrInvalidFrame
	}

	if !ValidInvocationID(request.InvocationID) || !validIntentDigest(request.IntentDigest) {
		return ErrInvalidFrame
	}

	if len(request.Input) == 0 || !request.Input.IsValid() || request.Input.Kind() != '{' {
		return ErrInvalidFrame
	}

	return nil
}

func ValidInvocationID(value string) bool {
	return len(value) == invocationIDBytes*2 && validHex(value)
}

func validIntentDigest(value string) bool {
	return len(value) == intentDigestBytes*2 && validHex(value)
}

func validHex(value string) bool {
	for _, character := range value {
		switch {
		case character >= '0' && character <= '9':
		case character >= 'a' && character <= 'f':
		default:
			return false
		}
	}

	return value != ""
}

func encodeActionResult(result ActionResult) ([]byte, error) {
	if err := ValidateActionResult(result); err != nil {
		return nil, err
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, oops.In("remote").With("operation", "encode_action_result").Wrap(err)
	}

	if len(encoded) > MaxResultBytes {
		return nil, oops.In("remote").With("operation", "encode_action_result").Wrap(ErrFrameTooLarge)
	}

	return encoded, nil
}
