//go:build linux || darwin

package remote

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/privatefs"
)

const (
	replayFenceName         = "fence.json"
	maxFenceRecords         = 16384
	maxFenceBytes           = 2 << 20
	invocationDirName       = "receipts"
	invocationLockName      = "receipts.lock"
	invocationFileSuffix    = ".json"
	maxInvocationBytes      = 64 << 10
	maxInvocationRecords    = 4096
	invocationPruneFraction = 3
	outcomeOK               = "ok"
)

var (
	ErrInvocationCapacity = errors.New("remote invocation receipts are full")
	ErrInvocationResolved = errors.New("remote invocation already has a durable outcome")
)

type ReplayFence struct {
	Version    int                  `json:"version"`
	Tombstones map[string]Tombstone `json:"tombstones"`
}

// Tombstone keeps the outcome of a retired invocation so its ID cannot run again. The output is
// not kept.
type Tombstone struct {
	IntentDigest string    `json:"intentDigest"`
	Action       action.ID `json:"action"`
	Outcome      string    `json:"outcome"`
	RetiredAt    time.Time `json:"retiredAt"`
}

// Invocation is the durable receipt of one admitted action. Outcome is empty until the action
// finishes, then "ok" or the failure code. Output is kept when it fits the receipt.
type Invocation struct {
	Version      int            `json:"version"`
	ID           string         `json:"id"`
	IntentDigest string         `json:"intentDigest"`
	Action       action.ID      `json:"action"`
	AdmittedAt   time.Time      `json:"admittedAt"`
	Outcome      string         `json:"outcome"`
	ResolvedAt   time.Time      `json:"resolvedAt"`
	Output       jsontext.Value `json:"output,omitzero"`
}

type recordVersion struct {
	Version int `json:"version"`
}

func (invocation Invocation) Unresolved() bool {
	return invocation.Outcome == ""
}

func (invocation Invocation) IsUncertain() bool {
	return invocation.Outcome == "" || invocation.Outcome == string(action.CodeUncertain)
}

func (invocation Invocation) Result() ActionResult {
	if invocation.Unresolved() {
		return ActionResult{InvocationID: invocation.ID, Action: invocation.Action, OK: false, Code: action.CodeUncertain, Output: nil}
	}

	if invocation.Outcome == outcomeOK {
		return ActionResult{InvocationID: invocation.ID, Action: invocation.Action, OK: true, Code: "", Output: invocation.Output}
	}

	return ActionResult{InvocationID: invocation.ID, Action: invocation.Action, OK: false, Code: action.Code(invocation.Outcome), Output: invocation.Output}
}

func (invocation Invocation) Validate() error {
	if invocation.Version != ProtocolVersion || !ValidInvocationID(invocation.ID) ||
		!validIntentDigest(invocation.IntentDigest) || !invocation.Action.Valid() ||
		invocation.AdmittedAt.IsZero() || !validOutcome(invocation.Outcome) {
		return ErrInvalidFrame
	}

	if len(invocation.Output) > 0 && !invocation.Output.IsValid() {
		return ErrInvalidFrame
	}

	return invocation.validateResolution()
}

// validateResolution requires a resolved invocation to record when it resolved, and an
// unresolved one to carry no resolution.
func (invocation Invocation) validateResolution() error {
	if invocation.Outcome == "" {
		if !invocation.ResolvedAt.IsZero() || len(invocation.Output) > 0 {
			return ErrInvalidFrame
		}

		return nil
	}

	if invocation.ResolvedAt.IsZero() {
		return ErrInvalidFrame
	}

	return nil
}

// OutcomeOf is the receipt outcome of an action result.
func OutcomeOf(result ActionResult) string {
	if result.OK {
		return outcomeOK
	}

	return string(result.Code)
}

func InvocationDir(stateHome string) (string, error) {
	dir, err := PeerStateDir(stateHome)
	if err != nil {
		return "", err
	}

	return joinComponents(dir, invocationDirName), nil
}

func InvocationPath(stateHome string) (string, error) {
	dir, err := PeerStatePath(stateHome)
	if err != nil {
		return "", err
	}

	return joinComponents(dir, invocationDirName), nil
}

func AdmitInvocation(ctx context.Context, dir *privatefs.Dir, request ActionRequest, now time.Time) (Invocation, bool, error) {
	if !ValidInvocationID(request.InvocationID) || !validIntentDigest(request.IntentDigest) ||
		!request.Action.Valid() {
		return Invocation{}, false, oops.In("remote").With("operation", "admit_invocation").Wrap(ErrInvalidFrame)
	}

	lock, err := dir.Lock(ctx, invocationLockName)
	if err != nil {
		return Invocation{}, false, oops.In("remote").With("operation", "admit_invocation").Wrap(err)
	}

	invocation, admitted, admitErr := admitInvocationLocked(dir, request, now)

	closeErr := lock.Close()
	if closeErr != nil {
		closeErr = oops.In("remote").With("operation", "admit_invocation").Wrap(closeErr)
	}

	if admitErr != nil {
		return invocation, false, errors.Join(admitErr, closeErr)
	}

	return invocation, admitted, closeErr
}

// ResolveInvocation records an invocation's outcome. An output too large for the receipt is
// dropped; the outcome is always kept.
func ResolveInvocation(ctx context.Context, dir *privatefs.Dir, id, outcome string, output jsontext.Value, now time.Time) error {
	if !ValidInvocationID(id) || !validOutcome(outcome) || outcome == "" {
		return oops.In("remote").With("operation", "resolve_invocation").Wrap(ErrInvalidFrame)
	}

	lock, err := dir.Lock(ctx, invocationLockName)
	if err != nil {
		return oops.In("remote").With("operation", "resolve_invocation").Wrap(err)
	}

	resolveErr := resolveInvocationLocked(dir, id, outcome, output, now)

	closeErr := lock.Close()
	if closeErr != nil {
		closeErr = oops.In("remote").With("operation", "resolve_invocation").Wrap(closeErr)
	}

	return errors.Join(resolveErr, closeErr)
}

func LookupInvocation(dir *privatefs.Dir, id string) (Invocation, error) {
	if !ValidInvocationID(id) {
		return Invocation{}, oops.In("remote").With("operation", "lookup_invocation").Wrap(ErrInvalidFrame)
	}

	inv, err := readInvocation(dir, id)
	if err == nil {
		return inv, nil
	}

	if !errors.Is(err, fs.ErrNotExist) {
		return Invocation{}, err
	}

	fence, fenceErr := readReplayFence(dir)
	if fenceErr != nil {
		return Invocation{}, err
	}

	tombstone, ok := fence.Tombstones[id]
	if !ok {
		return Invocation{}, fs.ErrNotExist
	}

	return tombstone.invocation(id), nil
}

func (tombstone Tombstone) invocation(id string) Invocation {
	return Invocation{
		Version:      ProtocolVersion,
		ID:           id,
		IntentDigest: tombstone.IntentDigest,
		Action:       tombstone.Action,
		AdmittedAt:   tombstone.RetiredAt,
		Outcome:      tombstone.Outcome,
		ResolvedAt:   tombstone.RetiredAt,
		Output:       nil,
	}
}

func admitInvocationLocked(dir *privatefs.Dir, request ActionRequest, now time.Time) (Invocation, bool, error) {
	existing, err := readInvocation(dir, request.InvocationID)

	switch {
	case err == nil:
		if existing.IntentDigest != request.IntentDigest || existing.Action != request.Action {
			return existing, false, ErrInvocationConflict
		}

		return existing, false, nil
	case !errors.Is(err, fs.ErrNotExist):
		return Invocation{}, false, err
	}

	fence, fenceErr := readReplayFence(dir)
	if fenceErr != nil && !errors.Is(fenceErr, fs.ErrNotExist) {
		return Invocation{}, false, fenceErr
	}

	if tombstone, ok := fence.Tombstones[request.InvocationID]; ok {
		if tombstone.IntentDigest != request.IntentDigest || tombstone.Action != request.Action {
			return tombstone.invocation(request.InvocationID), false, ErrInvocationConflict
		}

		return tombstone.invocation(request.InvocationID), false, nil
	}

	if err := pruneInvocations(dir, maxInvocationRecords, now); err != nil {
		return Invocation{}, false, err
	}

	invocation := Invocation{
		Version:      ProtocolVersion,
		ID:           request.InvocationID,
		IntentDigest: request.IntentDigest,
		Action:       request.Action,
		AdmittedAt:   now.UTC(),
		Outcome:      "",
		ResolvedAt:   time.Time{},
		Output:       nil,
	}

	if err := writeInvocation(dir, invocation); err != nil {
		return Invocation{}, false, err
	}

	return invocation, true, nil
}

func resolveInvocationLocked(dir *privatefs.Dir, id, outcome string, output jsontext.Value, now time.Time) error {
	invocation, err := readInvocation(dir, id)
	if err != nil {
		return err
	}

	if !invocation.Unresolved() {
		if invocation.Outcome == outcome {
			return nil
		}

		return oops.In("remote").With("operation", "resolve_invocation").With("outcome", invocation.Outcome).Wrap(ErrInvocationResolved)
	}

	invocation.Outcome = outcome
	invocation.ResolvedAt = now.UTC()
	invocation.Output = output

	err = writeInvocation(dir, invocation)
	if errors.Is(err, ErrFrameTooLarge) {
		invocation.Output = nil

		return writeInvocation(dir, invocation)
	}

	return err
}

func readInvocation(dir *privatefs.Dir, id string) (Invocation, error) {
	name, err := invocationName(id)
	if err != nil {
		return Invocation{}, oops.In("remote").With("operation", "read_invocation").Wrap(err)
	}

	content, err := dir.Read(name, int64(maxInvocationBytes))
	if err != nil {
		return Invocation{}, oops.In("remote").With("operation", "read_invocation").Wrap(err)
	}

	invocation, err := decodeStrict[Invocation](bytes.TrimSpace(content))
	if err != nil {
		return Invocation{}, oops.In("remote").With("operation", "read_invocation").Wrap(err)
	}

	if err := invocation.Validate(); err != nil {
		return Invocation{}, oops.In("remote").With("operation", "read_invocation").Wrap(err)
	}

	if invocation.ID != id {
		return Invocation{}, oops.In("remote").With("operation", "read_invocation").Wrap(ErrInvalidFrame)
	}

	return invocation, nil
}

func writeInvocation(dir *privatefs.Dir, invocation Invocation) error {
	if err := invocation.Validate(); err != nil {
		return oops.In("remote").With("operation", "write_invocation").Wrap(err)
	}

	name, err := invocationName(invocation.ID)
	if err != nil {
		return oops.In("remote").With("operation", "write_invocation").Wrap(err)
	}

	encoded, err := json.Marshal(invocation)
	if err != nil {
		return oops.In("remote").With("operation", "write_invocation").Wrap(err)
	}

	if len(encoded) > maxInvocationBytes {
		return oops.In("remote").With("operation", "write_invocation").Wrap(ErrFrameTooLarge)
	}

	if err := writeFileAtomic(dir, name, encoded); err != nil {
		return oops.In("remote").With("operation", "write_invocation").Wrap(err)
	}

	return nil
}

func pruneInvocations(dir *privatefs.Dir, limit int, now time.Time) error {
	names, err := dir.Names()
	if err != nil {
		return oops.In("remote").With("operation", "prune_invocations").Wrap(err)
	}

	recordCount := 0
	resolved := make([]Invocation, 0, len(names))

	for _, name := range names {
		id, ok := invocationIDFromName(name)
		if !ok {
			continue
		}

		invocation, readErr := readInvocation(dir, id)
		if readErr != nil && staleRecord(dir, name) {
			if err := removeFile(dir, name); err != nil {
				return err
			}

			continue
		}

		recordCount++

		if readErr != nil {
			continue
		}

		if !invocation.IsUncertain() {
			resolved = append(resolved, invocation)
		}
	}

	if recordCount < limit {
		return nil
	}

	uncertainCount := recordCount - len(resolved)
	target := limit - limit/invocationPruneFraction

	if uncertainCount >= target {
		return oops.In("remote").With("operation", "prune_invocations").Wrap(ErrInvocationCapacity)
	}

	return retireOldestResolvedInvocations(dir, resolved, recordCount, target, now)
}

func retireOldestResolvedInvocations(dir *privatefs.Dir, resolved []Invocation, totalCount, target int, now time.Time) error {
	slices.SortFunc(resolved, func(left, right Invocation) int {
		if order := left.AdmittedAt.Compare(right.AdmittedAt); order != 0 {
			return order
		}

		return strings.Compare(left.ID, right.ID)
	})

	fence, err := readReplayFence(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	toRemove := make([]Invocation, 0, len(resolved))
	for _, invocation := range resolved {
		if totalCount-len(toRemove) <= target {
			break
		}

		toRemove = append(toRemove, invocation)
	}

	if len(fence.Tombstones)+len(toRemove) > maxFenceRecords {
		return oops.In("remote").With("operation", "retire_invocations").Wrap(ErrInvocationCapacity)
	}

	for _, invocation := range toRemove {
		fence.Tombstones[invocation.ID] = Tombstone{
			IntentDigest: invocation.IntentDigest,
			Action:       invocation.Action,
			Outcome:      invocation.Outcome,
			RetiredAt:    now.UTC(),
		}
	}

	if err := writeReplayFence(dir, fence); err != nil {
		return err
	}

	for _, invocation := range toRemove {
		name, nameErr := invocationName(invocation.ID)
		if nameErr != nil {
			return nameErr
		}

		if err := removeFile(dir, name); err != nil {
			return err
		}
	}

	return nil
}

func readReplayFence(dir *privatefs.Dir) (ReplayFence, error) {
	content, err := dir.Read(replayFenceName, int64(maxFenceBytes))
	if errors.Is(err, fs.ErrNotExist) {
		return ReplayFence{Version: ProtocolVersion, Tombstones: make(map[string]Tombstone)}, nil
	}

	if err != nil {
		return ReplayFence{Version: 0, Tombstones: nil}, oops.In("remote").With("operation", "read_replay_fence").Wrap(err)
	}

	if version, ok := contentVersion(content); ok && version != ProtocolVersion {
		return ReplayFence{Version: ProtocolVersion, Tombstones: make(map[string]Tombstone)}, nil
	}

	fence, err := decodeStrict[ReplayFence](bytes.TrimSpace(content))
	if err != nil {
		return ReplayFence{Version: 0, Tombstones: nil}, oops.In("remote").With("operation", "read_replay_fence").Wrap(err)
	}

	if fence.Tombstones == nil {
		fence.Tombstones = make(map[string]Tombstone)
	}

	return fence, nil
}

func writeReplayFence(dir *privatefs.Dir, fence ReplayFence) error {
	fence.Version = ProtocolVersion
	if fence.Tombstones == nil {
		fence.Tombstones = make(map[string]Tombstone)
	}

	encoded, err := json.Marshal(fence)
	if err != nil {
		return oops.In("remote").With("operation", "write_replay_fence").Wrap(err)
	}

	if len(encoded) > maxFenceBytes {
		return oops.In("remote").With("operation", "write_replay_fence").Wrap(ErrFrameTooLarge)
	}

	if err := writeFileAtomic(dir, replayFenceName, encoded); err != nil {
		return oops.In("remote").With("operation", "write_replay_fence").Wrap(err)
	}

	return nil
}

func invocationName(id string) (string, error) {
	if !ValidInvocationID(id) {
		return "", oops.In("remote").With("operation", "invocation_name").Wrap(ErrUnsafePath)
	}

	return id + invocationFileSuffix, nil
}

func invocationIDFromName(name string) (string, bool) {
	id, found := strings.CutSuffix(name, invocationFileSuffix)
	if !found || !ValidInvocationID(id) {
		return "", false
	}

	return id, true
}

func validOutcome(outcome string) bool {
	if outcome == "" || outcome == outcomeOK {
		return true
	}

	return action.Code(outcome).Valid()
}

// staleRecord reports a receipt written by another protocol version. Its invocation cannot be
// retried with this protocol, so it only takes space.
func staleRecord(dir *privatefs.Dir, name string) bool {
	content, err := dir.Read(name, int64(maxInvocationBytes))
	if err != nil {
		return false
	}

	version, ok := contentVersion(content)

	return ok && version != ProtocolVersion
}

func contentVersion(content []byte) (int, bool) {
	var probe recordVersion
	if err := json.Unmarshal(bytes.TrimSpace(content), &probe); err != nil {
		return 0, false
	}

	return probe.Version, true
}
