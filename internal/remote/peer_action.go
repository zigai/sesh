//go:build linux || darwin

package remote

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"time"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/session"
)

var (
	ErrPeerForbidden   = errors.New("remote operation is not permitted by peer policy")
	ErrPeerUnsupported = errors.New("remote operation has no native peer handler")
)

type LiveServer interface {
	Sessions(ctx context.Context) ([]session.Session, error)
	Create(ctx context.Context, name, directory string) error
	Rename(ctx context.Context, sessionID, name string) error
	Kill(ctx context.Context, sessionID string) error
	Attach(ctx context.Context, sessionID string, streams AttachStreams) error
}

type CompoundDeleter interface {
	DeleteLiveAndSaved(ctx context.Context, target session.Session, live LiveServer) (durableOperationID string, err error)
}

type SavedSessions interface {
	Delete(ctx context.Context, generation, sessionID string) (string, error)
	Snapshot(ctx context.Context, socket string) (string, error)
	Restore(ctx context.Context, socket, generation, sessionID string) (string, error)
}

type PeerPolicy struct {
	Create   bool
	Rename   bool
	Kill     bool
	Delete   bool
	Snapshot bool
	Restore  bool
	Sockets  []string
}

type PeerActionOptions struct {
	PeerID   string
	Receipts *privatefs.Dir
	Policy   PeerPolicy
	Live     LiveServer
	Saved    SavedSessions
	Now      func() time.Time
}

func (policy PeerPolicy) Check(action Action, socket string) error {
	var allowed bool

	switch action {
	case ActionCreate:
		allowed = policy.Create
	case ActionRename:
		allowed = policy.Rename
	case ActionKill:
		allowed = policy.Kill
	case ActionDelete:
		allowed = policy.Delete
	case ActionSnapshot:
		allowed = policy.Snapshot
	case ActionRestore:
		allowed = policy.Restore
	default:
		return oops.In("remote").With("operation", "check_peer_policy").With("action", string(action)).Wrap(ErrPeerUnsupported)
	}

	if !allowed {
		return oops.In("remote").With("operation", "check_peer_policy").With("action", string(action)).Wrap(ErrPeerForbidden)
	}

	if socket != "" && len(policy.Sockets) > 0 && !slices.Contains(policy.Sockets, socket) {
		return oops.In("remote").With("operation", "check_peer_policy").With("action", string(action)).Wrap(ErrPeerForbidden)
	}

	return nil
}

func ExecutePeerAction(ctx context.Context, options PeerActionOptions, request ActionRequest) ActionResult {
	if err := ValidateActionRequest(request, true); err != nil {
		return ActionResult{InvocationID: request.InvocationID, OK: false, Code: ActionCodeInvalidRequest, DurableOperationID: ""}
	}

	if err := VerifyIntentDigest(request); err != nil {
		return ActionResult{InvocationID: request.InvocationID, OK: false, Code: ActionCodeInvalidRequest, DurableOperationID: ""}
	}

	if request.PeerID != options.PeerID {
		return ActionResult{InvocationID: request.InvocationID, OK: false, Code: ActionCodeTargetChanged, DurableOperationID: ""}
	}

	if err := options.Policy.Check(request.Operation, request.TmuxServerSocket); err != nil {
		return ActionResult{InvocationID: request.InvocationID, OK: false, Code: policyCode(err), DurableOperationID: ""}
	}

	if options.Receipts == nil {
		return ActionResult{InvocationID: request.InvocationID, OK: false, Code: ActionCodeUnavailable, DurableOperationID: ""}
	}

	now := options.Now
	if now == nil {
		now = time.Now
	}

	invocation, admitted, err := AdmitInvocation(ctx, options.Receipts, request, now())
	if err != nil {
		return ActionResult{InvocationID: request.InvocationID, OK: false, Code: admissionCode(err), DurableOperationID: ""}
	}

	if !admitted {
		return invocation.Result()
	}

	outcome, durableOperationID := executePeerMutation(ctx, options, request)

	if err := ResolveInvocation(ctx, options.Receipts, invocation.ID, outcome, durableOperationID, now()); err != nil {
		return ActionResult{
			InvocationID:       invocation.ID,
			OK:                 false,
			Code:               ActionCodeUnknownOutcome,
			DurableOperationID: durableOperationID,
		}
	}

	return invocationResult(invocation.ID, outcome, durableOperationID)
}

func LookupPeerInvocation(dir *privatefs.Dir, invocationID string) (ActionResult, error) {
	invocation, err := LookupInvocation(dir, invocationID)
	if errors.Is(err, fs.ErrNotExist) {
		return ActionResult{}, ErrInvocationNotFound
	}

	if err != nil {
		return ActionResult{}, err
	}

	return invocation.Result(), nil
}

func ValidateLiveTarget(request ActionRequest, sessions []session.Session) (session.Session, error) {
	if request.PeerID == "" {
		return session.Session{}, ErrInvalidFrame
	}

	for _, candidate := range sessions {
		if candidate.ID != request.SessionID || candidate.Name != request.SessionName {
			continue
		}

		if candidate.TmuxServerSocket != request.TmuxServerSocket {
			return session.Session{}, ErrTargetChanged
		}

		if candidate.RuntimeIdentity.TmuxServerPID != request.TmuxServerPID ||
			!candidate.RuntimeIdentity.TmuxServerStartedAt.Equal(request.TmuxServerStartedAt) ||
			!candidate.RuntimeIdentity.TmuxSessionCreatedAt.Equal(request.SessionCreatedAt) {
			return session.Session{}, ErrTargetChanged
		}

		return candidate, nil
	}

	return session.Session{}, oops.In("remote").With("operation", "find_target_session").Wrap(ErrTargetChanged)
}

func PermissionsAllow(permissions config.RemotePermissionConfig, action Action) bool {
	switch action {
	case ActionCreate:
		return permissions.Create
	case ActionRename:
		return permissions.Rename
	case ActionKill:
		return permissions.Kill
	case ActionDelete:
		return permissions.Delete
	case ActionSnapshot:
		return permissions.Snapshot
	case ActionRestore:
		return permissions.Restore
	default:
		return false
	}
}

func executePeerMutation(ctx context.Context, options PeerActionOptions, request ActionRequest) (string, string) {
	switch request.Operation {
	case ActionCreate:
		return executePeerCreate(ctx, options, request)
	case ActionRename, ActionKill:
		return executePeerLiveMutation(ctx, options, request)
	case ActionDelete:
		return executePeerDelete(ctx, options, request)
	case ActionSnapshot:
		return executePeerSavedOperation(ctx, options, request, ActionSnapshot)
	case ActionRestore:
		return executePeerSavedOperation(ctx, options, request, ActionRestore)
	default:
		return ActionCodeUnsupported, ""
	}
}

func executePeerCreate(ctx context.Context, options PeerActionOptions, request ActionRequest) (string, string) {
	if options.Live == nil {
		return ActionCodeUnsupported, ""
	}

	sessions, err := options.Live.Sessions(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ActionCodeUnknownOutcome, ""
		}

		return ActionCodeUnavailable, ""
	}

	for _, existing := range sessions {
		if existing.Name == request.SessionName {
			return ActionCodeConflict, ""
		}
	}

	createErr := options.Live.Create(ctx, request.SessionName, request.Directory)
	if createErr == nil && ctx.Err() == nil {
		return outcomeOK, ""
	}

	return ActionCodeUnknownOutcome, ""
}

func executePeerLiveMutation(ctx context.Context, options PeerActionOptions, request ActionRequest) (string, string) {
	if options.Live == nil {
		return ActionCodeUnsupported, ""
	}

	sessions, err := options.Live.Sessions(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ActionCodeUnknownOutcome, ""
		}

		return ActionCodeUnavailable, ""
	}

	validated, err := ValidateLiveTarget(request, sessions)
	if err != nil {
		return ActionCodeTargetChanged, ""
	}

	switch request.Operation {
	case ActionRename:
		return executePeerRename(ctx, options.Live, validated, request.NewName)
	case ActionKill:
		return executePeerKill(ctx, options.Live, validated)
	case ActionCreate, ActionDelete, ActionSnapshot, ActionRestore:
		return ActionCodeUnsupported, ""
	default:
		return ActionCodeUnsupported, ""
	}
}

func executePeerRename(ctx context.Context, live LiveServer, validated session.Session, newName string) (string, string) {
	renameErr := live.Rename(ctx, validated.ID, newName)
	if renameErr == nil && ctx.Err() == nil {
		return outcomeOK, ""
	}

	return ActionCodeUnknownOutcome, ""
}

func executePeerKill(ctx context.Context, live LiveServer, validated session.Session) (string, string) {
	killErr := live.Kill(ctx, validated.ID)
	if killErr == nil && ctx.Err() == nil {
		return outcomeOK, ""
	}

	return ActionCodeUnknownOutcome, ""
}

func executePeerDelete(ctx context.Context, options PeerActionOptions, request ActionRequest) (string, string) {
	if options.Saved == nil {
		return ActionCodeUnsupported, ""
	}

	if request.Kind == string(session.KindSaved) {
		return executePeerSavedDelete(ctx, options.Saved, request)
	}

	return executePeerCompoundLiveDelete(ctx, options, request)
}

func executePeerSavedDelete(ctx context.Context, saved SavedSessions, request ActionRequest) (string, string) {
	durableOperationID, err := saved.Delete(ctx, request.Generation, request.SessionID)
	if err == nil && ctx.Err() == nil {
		return outcomeOK, durableOperationID
	}

	if ctx.Err() != nil || durableOperationID != "" {
		return ActionCodeUnknownOutcome, durableOperationID
	}

	if errors.Is(err, ErrPeerUnsupported) {
		return ActionCodeUnsupported, durableOperationID
	}

	return ActionCodeUnavailable, durableOperationID
}

func executePeerCompoundLiveDelete(ctx context.Context, options PeerActionOptions, request ActionRequest) (string, string) {
	compound, ok := options.Saved.(CompoundDeleter)
	if !ok || compound == nil || options.Live == nil {
		return ActionCodeUnsupported, ""
	}

	sessions, err := options.Live.Sessions(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ActionCodeUnknownOutcome, ""
		}

		return ActionCodeUnavailable, ""
	}

	validated, err := ValidateLiveTarget(request, sessions)
	if err != nil {
		return ActionCodeTargetChanged, ""
	}

	durableOperationID, err := compound.DeleteLiveAndSaved(ctx, validated, options.Live)
	if err == nil && ctx.Err() == nil {
		return outcomeOK, durableOperationID
	}

	return ActionCodeUnknownOutcome, durableOperationID
}

func executePeerSavedOperation(ctx context.Context, options PeerActionOptions, request ActionRequest, action Action) (string, string) {
	if options.Saved == nil {
		return ActionCodeUnsupported, ""
	}

	durableOperationID, err := runPeerSavedOperation(ctx, options.Saved, request, action)
	if errors.Is(err, ErrPeerUnsupported) && durableOperationID == "" {
		return ActionCodeUnsupported, ""
	}

	if err == nil && ctx.Err() == nil {
		return outcomeOK, durableOperationID
	}

	if ctx.Err() != nil || errors.Is(err, errNativeOperationUncertain) {
		return ActionCodeUnknownOutcome, durableOperationID
	}

	if errors.Is(err, errNativeOperationPartial) {
		return ActionCodePartial, durableOperationID
	}

	if durableOperationID != "" {
		return ActionCodeUnknownOutcome, durableOperationID
	}

	if errors.Is(err, ErrPeerUnsupported) {
		return ActionCodeUnsupported, durableOperationID
	}

	return ActionCodeUnavailable, durableOperationID
}

func runPeerSavedOperation(ctx context.Context, saved SavedSessions, request ActionRequest, action Action) (string, error) {
	switch action {
	case ActionSnapshot:
		operationID, err := saved.Snapshot(ctx, request.TmuxServerSocket)

		return operationID, wrapSavedOperationError("snapshot", err)
	case ActionRestore:
		operationID, err := saved.Restore(ctx, request.TmuxServerSocket, request.Generation, request.SessionID)

		return operationID, wrapSavedOperationError("restore", err)
	case ActionCreate, ActionRename, ActionKill, ActionDelete:
		return "", ErrPeerUnsupported
	default:
		return "", ErrPeerUnsupported
	}
}

func wrapSavedOperationError(operation string, err error) error {
	if err == nil {
		return nil
	}

	return oops.In("remote").Code("remote_native_failed").With("operation", operation).Wrap(err)
}

func policyCode(err error) string {
	switch {
	case errors.Is(err, ErrPeerForbidden):
		return ActionCodeForbidden
	case errors.Is(err, ErrPeerUnsupported):
		return ActionCodeUnsupported
	default:
		return ActionCodeUnavailable
	}
}

func admissionCode(err error) string {
	switch {
	case errors.Is(err, ErrInvocationConflict):
		return ActionCodeInvocationConflict
	case errors.Is(err, ErrInvocationCapacity):
		return ActionCodeUnavailable
	case errors.Is(err, ErrInvalidFrame):
		return ActionCodeInvalidRequest
	default:
		return ActionCodeUnavailable
	}
}

func invocationResult(invocationID, outcome, durableOperationID string) ActionResult {
	if outcome == outcomeOK {
		return ActionResult{
			InvocationID:       invocationID,
			OK:                 true,
			Code:               "",
			DurableOperationID: durableOperationID,
		}
	}

	return ActionResult{
		InvocationID:       invocationID,
		OK:                 false,
		Code:               outcome,
		DurableOperationID: durableOperationID,
	}
}
