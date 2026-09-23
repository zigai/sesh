//go:build linux || darwin

package remote

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"io"
	"time"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/session"
)

const (
	ActionCodeInvalidRequest = "invalid_request"

	ActionCodeForbidden = "forbidden"

	ActionCodeUnsupported = "unsupported"

	ActionCodeConflict = "conflict"

	ActionCodeInvocationConflict = "invocation_conflict"

	ActionCodeTargetChanged = "target_changed"

	ActionCodeUnavailable = "unavailable"

	ActionCodePartial = "partial"

	ActionCodeUnknownOutcome = "unknown_outcome"

	ActionCodeNotFound = "not_found"
)

const (
	invocationIDBytes = 16
	intentDigestBytes = 32
)

const (
	ActionCreate Action = "create"

	ActionRename Action = "rename"

	ActionKill Action = "kill"

	ActionDelete Action = "delete"

	ActionSnapshot Action = "snapshot"

	ActionRestore Action = "restore"
)

var (
	ErrInvocationNotFound = errors.New("remote invocation record not found")

	ErrInvocationConflict = errors.New("remote invocation intent does not match")
	ErrActionFailed       = errors.New("remote action failed")
	ErrActionUncertain    = errors.New("remote action outcome is uncertain")
	ErrActionPartial      = errors.New("remote action is incomplete")

	errNilActionOutput = errors.New("remote action output is not configured")
)

type Action string

type ActionRequest struct {
	InvocationID        string    `json:"invocationId"`
	IntentDigest        string    `json:"intentDigest"`
	Operation           Action    `json:"operation"`
	ConnectionID        string    `json:"connectionId"`
	ConfigDigest        string    `json:"configDigest"`
	PeerID              string    `json:"peerId"`
	Kind                string    `json:"kind,omitempty"`
	TmuxServerSocket    string    `json:"tmuxServerSocket,omitempty"`
	TmuxServerPID       int       `json:"tmuxServerPid,omitempty"`
	TmuxServerStartedAt time.Time `json:"tmuxServerStartedAt"`
	SessionID           string    `json:"sessionId,omitempty"`
	SessionName         string    `json:"sessionName"`
	Generation          string    `json:"generation,omitempty"`
	SessionCreatedAt    time.Time `json:"sessionCreatedAt"`
	NewName             string    `json:"newName,omitempty"`
	Directory           string    `json:"directory,omitempty"`
}

type ActionResult struct {
	InvocationID       string `json:"invocationId"`
	OK                 bool   `json:"ok"`
	Code               string `json:"code"`
	DurableOperationID string `json:"durableOperationId,omitempty"`
}

type ActionOutcomeError struct{ Result ActionResult }

type CanonicalIntent struct {
	Operation           Action    `json:"operation"`
	PeerID              string    `json:"peerId"`
	Kind                string    `json:"kind"`
	TmuxServerSocket    string    `json:"tmuxServerSocket"`
	TmuxServerPID       int       `json:"tmuxServerPid"`
	TmuxServerStartedAt time.Time `json:"tmuxServerStartedAt"`
	SessionID           string    `json:"sessionId"`
	SessionName         string    `json:"sessionName"`
	Generation          string    `json:"generation"`
	SessionCreatedAt    time.Time `json:"sessionCreatedAt"`
	NewName             string    `json:"newName"`
	Directory           string    `json:"directory"`
}

func (err *ActionOutcomeError) Error() string {
	return ErrActionFailed.Error()
}

func (err *ActionOutcomeError) Unwrap() error {
	if err == nil {
		return ErrActionFailed
	}

	switch err.Result.Code {
	case ActionCodeUnknownOutcome:
		return ErrActionUncertain
	case ActionCodePartial:
		return ErrActionPartial
	default:
		return ErrActionFailed
	}
}

func NewInvocationID() (string, error) {
	value := make([]byte, invocationIDBytes)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", oops.In("remote").With("operation", "new_invocation_id").Wrap(err)
	}

	return hex.EncodeToString(value), nil
}

func IntentDigest(request ActionRequest) (string, error) {
	encoded, err := json.Marshal(CanonicalIntent{
		Operation:           request.Operation,
		PeerID:              request.PeerID,
		Kind:                request.Kind,
		TmuxServerSocket:    request.TmuxServerSocket,
		TmuxServerPID:       request.TmuxServerPID,
		TmuxServerStartedAt: request.TmuxServerStartedAt,
		SessionID:           request.SessionID,
		SessionName:         request.SessionName,
		Generation:          request.Generation,
		SessionCreatedAt:    request.SessionCreatedAt,
		NewName:             request.NewName,
		Directory:           request.Directory,
	})
	if err != nil {
		return "", oops.In("remote").With("operation", "intent_digest").Wrap(err)
	}

	digest := sha256.Sum256(encoded)

	return hex.EncodeToString(digest[:]), nil
}

func EncodeActionRequest(request ActionRequest) ([]byte, error) {
	if err := ValidateActionRequest(request, false); err != nil {
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

	if err := ValidateActionRequest(request, true); err != nil {
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
		return oops.In("remote").With("operation", "verify_intent_digest").With("action_code", ActionCodeInvocationConflict).Wrap(ErrInvocationConflict)
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
	if len(output) == 0 || len(output) > MaxActionBytes {
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
	if !ValidInvocationID(result.InvocationID) {
		return ErrInvalidFrame
	}

	if result.OK {
		if result.Code != "" {
			return ErrInvalidFrame
		}

		return nil
	}

	if !validActionCode(result.Code) {
		return ErrInvalidFrame
	}

	return nil
}

func ValidateActionRequest(request ActionRequest, remoteSide bool) error {
	if err := validateActionRequestEnvelope(request); err != nil {
		return err
	}

	if request.Operation == ActionCreate {
		return validateCreateActionRequest(request)
	}

	if request.Kind == string(session.KindSaved) {
		return validateSavedActionRequest(request)
	}

	if remoteSide {
		return validatePeerLiveActionRequest(request)
	}

	return validateLiveActionRequest(request)
}

func ValidInvocationID(value string) bool {
	return len(value) == invocationIDBytes*2 && validHex(value)
}

func validateActionRequestEnvelope(request ActionRequest) error {
	if !validAction(request.Operation) || !ValidIdentifier(request.PeerID) ||
		!ValidIdentifier(request.SessionName) || !ValidIdentifier(request.ConnectionID) ||
		!ValidIdentifier(request.ConfigDigest) {
		return ErrInvalidFrame
	}

	if !ValidInvocationID(request.InvocationID) || !validIntentDigest(request.IntentDigest) {
		return ErrInvalidFrame
	}

	if !ValidText(request.TmuxServerSocket) || !ValidText(request.NewName) || !ValidText(request.Directory) {
		return ErrInvalidFrame
	}

	if request.TmuxServerPID < 0 {
		return ErrInvalidFrame
	}

	return nil
}

func validateCreateActionRequest(request ActionRequest) error {
	if request.Kind != "" || request.NewName != "" || request.SessionID != "" || request.Generation != "" ||
		request.TmuxServerPID != 0 || !request.TmuxServerStartedAt.IsZero() ||
		!request.SessionCreatedAt.IsZero() {
		return ErrInvalidFrame
	}

	if request.Directory == "" {
		return ErrInvalidFrame
	}

	return nil
}

func validateSavedActionRequest(request ActionRequest) error {
	if request.Operation != ActionDelete && request.Operation != ActionRestore {
		return ErrInvalidFrame
	}

	if !ValidIdentifier(request.SessionID) || !ValidIdentifier(request.Generation) || request.TmuxServerPID != 0 ||
		!request.TmuxServerStartedAt.IsZero() || !request.SessionCreatedAt.IsZero() ||
		request.NewName != "" || request.Directory != "" {
		return ErrInvalidFrame
	}

	return nil
}

func validateLiveActionRequest(request ActionRequest) error {
	if request.Kind != string(session.KindLive) || request.Directory != "" ||
		request.Generation != "" ||
		!ValidIdentifier(request.SessionID) || request.TmuxServerPID <= 0 ||
		request.TmuxServerStartedAt.IsZero() || request.SessionCreatedAt.IsZero() {
		return ErrInvalidFrame
	}

	if request.Operation == ActionRename {
		if !ValidIdentifier(request.NewName) {
			return ErrInvalidFrame
		}
	} else if request.NewName != "" {
		return ErrInvalidFrame
	}

	return nil
}

func validatePeerLiveActionRequest(request ActionRequest) error {
	if request.Kind != string(session.KindLive) || request.Directory != "" ||
		!ValidIdentifier(request.SessionID) || request.TmuxServerSocket == "" ||
		request.TmuxServerPID <= 0 || request.TmuxServerStartedAt.IsZero() ||
		request.SessionCreatedAt.IsZero() {
		return ErrInvalidFrame
	}

	if request.Operation == ActionRename {
		if !ValidIdentifier(request.NewName) {
			return ErrInvalidFrame
		}
	} else if request.NewName != "" {
		return ErrInvalidFrame
	}

	return nil
}

func validIntentDigest(value string) bool {
	return len(value) == intentDigestBytes*2 && validHex(value)
}

func validAction(action Action) bool {
	switch action {
	case ActionCreate, ActionRename, ActionKill, ActionDelete, ActionSnapshot, ActionRestore:
		return true
	default:
		return false
	}
}

func validActionCode(code string) bool {
	switch code {
	case ActionCodeInvalidRequest, ActionCodeForbidden, ActionCodeUnsupported,
		ActionCodeConflict, ActionCodeInvocationConflict, ActionCodeTargetChanged,
		ActionCodeUnavailable, ActionCodePartial, ActionCodeUnknownOutcome, ActionCodeNotFound:
		return true
	default:
		return false
	}
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

	if len(encoded) > MaxActionBytes {
		return nil, oops.In("remote").With("operation", "encode_action_result").Wrap(ErrFrameTooLarge)
	}

	return encoded, nil
}
