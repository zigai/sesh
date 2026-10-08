//go:build linux || darwin

package remote

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/privatefs"
)

func openTestReceiptDir(t *testing.T) (*privatefs.Dir, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "receipts")

	dir, err := openPrivateDir(path, true)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = dir.Close() })

	return dir, path
}

func admittedRequest(t *testing.T) ActionRequest {
	t.Helper()

	return liveActionRequest(t)
}

func TestAdmitInvocationIsDurableAndIdempotent(t *testing.T) {
	dir, receipts := openTestReceiptDir(t)
	request := admittedRequest(t)

	now := time.Unix(1000, 0).UTC()

	first, admitted, err := AdmitInvocation(t.Context(), dir, request, now)
	if err != nil {
		t.Fatalf("AdmitInvocation() error = %v", err)
	}

	if !admitted || !first.Unresolved() {
		t.Fatalf("first admission = (%#v, %t), want a new unresolved record", first, admitted)
	}

	second, admitted, err := AdmitInvocation(t.Context(), dir, request, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("second AdmitInvocation() error = %v", err)
	}

	if admitted {
		t.Fatal("second admission created another record for the same invocation ID")
	}

	if second.AdmittedAt != first.AdmittedAt {
		t.Fatalf("second admission changed the durable admission time: %#v", second)
	}

	info, err := os.Stat(filepath.Join(receipts, first.ID+".json"))
	if err != nil {
		t.Fatalf("stat receipt: %v", err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestAdmitInvocationRejectsConflictingIntent(t *testing.T) {
	dir, _ := openTestReceiptDir(t)
	request := admittedRequest(t)

	now := time.Unix(1000, 0).UTC()

	if _, _, err := AdmitInvocation(t.Context(), dir, request, now); err != nil {
		t.Fatal(err)
	}

	conflicting := request
	conflicting.Input = jsontext.Value(`{"session":{"id":"$9","name":"other"},"force":false}`)

	digest, err := IntentDigest(conflicting)
	if err != nil {
		t.Fatal(err)
	}

	conflicting.IntentDigest = digest

	if _, _, err := AdmitInvocation(t.Context(), dir, conflicting, now); !errors.Is(err, ErrInvocationConflict) {
		t.Fatalf("AdmitInvocation(conflicting) error = %v, want %v", err, ErrInvocationConflict)
	}
}

func TestAdmitInvocationRejectsInvalidIdentity(t *testing.T) {
	dir, _ := openTestReceiptDir(t)

	request := admittedRequest(t)
	request.InvocationID = "short"

	if _, _, err := AdmitInvocation(t.Context(), dir, request, time.Now()); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("AdmitInvocation(short ID) error = %v, want %v", err, ErrInvalidFrame)
	}

	request = admittedRequest(t)
	request.IntentDigest = "not-a-digest"

	if _, _, err := AdmitInvocation(t.Context(), dir, request, time.Now()); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("AdmitInvocation(bad digest) error = %v, want %v", err, ErrInvalidFrame)
	}
}

func TestResolveAndLookupInvocation(t *testing.T) {
	dir, _ := openTestReceiptDir(t)
	request := admittedRequest(t)

	now := time.Unix(1000, 0).UTC()

	invocation, _, err := AdmitInvocation(t.Context(), dir, request, now)
	if err != nil {
		t.Fatal(err)
	}

	pending, err := LookupInvocation(dir, invocation.ID)
	if err != nil {
		t.Fatalf("LookupInvocation() error = %v", err)
	}

	if !pending.Unresolved() || pending.Result().Code != action.CodeUncertain {
		t.Fatalf("pending result = %#v, want an explicit unknown outcome", pending.Result())
	}

	if err := ResolveInvocation(t.Context(), dir, invocation.ID, outcomeOK, nil, now.Add(time.Second)); err != nil {
		t.Fatalf("ResolveInvocation() error = %v", err)
	}

	resolved, err := LookupInvocation(dir, invocation.ID)
	if err != nil {
		t.Fatal(err)
	}

	if resolved.Unresolved() || !resolved.Result().OK {
		t.Fatalf("resolved record = %#v, want a durable success", resolved)
	}

	if err := ResolveInvocation(t.Context(), dir, invocation.ID, outcomeOK, nil, now.Add(2*time.Second)); err != nil {
		t.Fatalf("repeated ResolveInvocation() error = %v", err)
	}

	if err := ResolveInvocation(t.Context(), dir, invocation.ID, string(action.CodeUnavailable), nil, now.Add(2*time.Second)); !errors.Is(err, ErrInvocationResolved) {
		t.Fatalf("conflicting ResolveInvocation() error = %v, want %v", err, ErrInvocationResolved)
	}

	if _, err := LookupInvocation(dir, "00000000000000000000000000000000"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LookupInvocation(missing) error = %v, want not-exist", err)
	}
}

func TestLookupInvocationRejectsInvalidIdentity(t *testing.T) {
	dir, _ := openTestReceiptDir(t)

	if _, err := LookupInvocation(dir, "not-an-identifier"); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("LookupInvocation() error = %v, want %v", err, ErrInvalidFrame)
	}
}

func TestResolveInvocationRequiresDurableOutcome(t *testing.T) {
	dir, _ := openTestReceiptDir(t)

	if err := ResolveInvocation(t.Context(), dir, "00000000000000000000000000000000", "", nil, time.Now()); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("ResolveInvocation(empty outcome) error = %v, want %v", err, ErrInvalidFrame)
	}

	if err := ResolveInvocation(t.Context(), dir, "00000000000000000000000000000000", "raw failure text", nil, time.Now()); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("ResolveInvocation(unknown outcome) error = %v, want %v", err, ErrInvalidFrame)
	}
}

func TestPruneInvocationsPreservesUnresolvedRecords(t *testing.T) {
	dir, _ := openTestReceiptDir(t)

	now := time.Unix(1000, 0).UTC()

	resolved := seedResolvedInvocations(t, dir, &now, 3)

	unresolved := seedUnresolvedInvocation(t, dir, now)
	unknownInv := seedUnknownOutcomeInvocation(t, dir, now)

	if err := pruneInvocations(dir, 4, now); err != nil {
		t.Fatalf("pruneInvocations() error = %v", err)
	}

	assertUnprunedRecords(t, dir, unresolved.ID, unknownInv.ID)
	assertTombstoneAndReplayFence(t, dir, resolved[0], now)
}

func seedResolvedInvocations(t *testing.T, dir *privatefs.Dir, now *time.Time, count int) []Invocation {
	t.Helper()

	resolved := make([]Invocation, 0, count)

	for range count {
		request := admittedRequest(t)

		invocation, _, err := AdmitInvocation(t.Context(), dir, request, *now)
		if err != nil {
			t.Fatal(err)
		}

		if err := ResolveInvocation(t.Context(), dir, invocation.ID, outcomeOK, nil, *now); err != nil {
			t.Fatal(err)
		}

		resolved = append(resolved, invocation)
		*now = now.Add(time.Second)
	}

	return resolved
}

func seedUnresolvedInvocation(t *testing.T, dir *privatefs.Dir, now time.Time) Invocation {
	t.Helper()

	unresolved, _, err := AdmitInvocation(t.Context(), dir, admittedRequest(t), now)
	if err != nil {
		t.Fatal(err)
	}

	return unresolved
}

func seedUnknownOutcomeInvocation(t *testing.T, dir *privatefs.Dir, now time.Time) Invocation {
	t.Helper()

	unknownInv, _, err := AdmitInvocation(t.Context(), dir, admittedRequest(t), now)
	if err != nil {
		t.Fatal(err)
	}

	if err := ResolveInvocation(t.Context(), dir, unknownInv.ID, string(action.CodeUncertain), nil, now); err != nil {
		t.Fatal(err)
	}

	return unknownInv
}

func assertUnprunedRecords(t *testing.T, dir *privatefs.Dir, unresolvedID, unknownID string) {
	t.Helper()

	if _, err := LookupInvocation(dir, unresolvedID); err != nil {
		t.Fatalf("pruning removed an unresolved record: %v", err)
	}

	if looked, err := LookupInvocation(dir, unknownID); err != nil || looked.Outcome != string(action.CodeUncertain) {
		t.Fatalf("pruning removed an unknown-outcome record: %v, looked=%#v", err, looked)
	}
}

func assertTombstoneAndReplayFence(t *testing.T, dir *privatefs.Dir, retired Invocation, now time.Time) {
	t.Helper()

	if _, err := dir.Read(retired.ID+".json", maxInvocationBytes); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pruned record file still on disk: %v", err)
	}

	fromFence, err := LookupInvocation(dir, retired.ID)
	if err != nil {
		t.Fatalf("LookupInvocation(pruned ID) error = %v, want tombstone", err)
	}

	if fromFence.Outcome != outcomeOK || fromFence.ID != retired.ID {
		t.Fatalf("tombstone = %#v, want durable outcome %q", fromFence, outcomeOK)
	}

	retryRequest := ActionRequest{
		InvocationID: retired.ID,
		IntentDigest: retired.IntentDigest,
		Action:       retired.Action,
	}

	retried, admitted, err := AdmitInvocation(t.Context(), dir, retryRequest, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("retrying pruned invocation ID error = %v", err)
	}

	if admitted {
		t.Fatal("pruned invocation ID was admitted as new! duplicate dispatch occurred")
	}

	if retried.Outcome != outcomeOK {
		t.Fatalf("retried invocation outcome = %q, want %q", retried.Outcome, outcomeOK)
	}

	conflictRequest := retryRequest
	conflictRequest.IntentDigest = strings.Repeat("f", 64)

	if _, _, err := AdmitInvocation(t.Context(), dir, conflictRequest, now.Add(time.Hour)); !errors.Is(err, ErrInvocationConflict) {
		t.Fatalf("AdmitInvocation(conflicting pruned) error = %v, want %v", err, ErrInvocationConflict)
	}
}

func TestPruneInvocationsRefusesWhenUnresolvedRecordsFillTheStore(t *testing.T) {
	dir, _ := openTestReceiptDir(t)

	now := time.Unix(1000, 0).UTC()

	for range 2 {
		request := admittedRequest(t)

		if _, _, err := AdmitInvocation(t.Context(), dir, request, now); err != nil {
			t.Fatal(err)
		}

		now = now.Add(time.Second)
	}

	if err := pruneInvocations(dir, 2, now); !errors.Is(err, ErrInvocationCapacity) {
		t.Fatalf("pruneInvocations() error = %v, want %v", err, ErrInvocationCapacity)
	}
}

func TestInvocationResultMapsDurableOutcomes(t *testing.T) {
	invocation := Invocation{
		Version:      ProtocolVersion,
		ID:           "0123456789abcdef0123456789abcdef",
		IntentDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Action:       action.SessionKill.ID,
		AdmittedAt:   time.Unix(1000, 0).UTC(),
	}

	invocation.Outcome = outcomeOK
	if result := invocation.Result(); !result.OK || result.Code != "" {
		t.Fatalf("success result = %#v", result)
	}

	invocation.Outcome = string(action.CodePartial)
	if result := invocation.Result(); result.OK || result.Code != action.CodePartial {
		t.Fatalf("partial result = %#v", result)
	}
}

func TestInvocationValidationRejectsInconsistentRecords(t *testing.T) {
	valid := Invocation{
		Version:      ProtocolVersion,
		ID:           "0123456789abcdef0123456789abcdef",
		IntentDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Action:       action.SessionKill.ID,
		AdmittedAt:   time.Unix(1000, 0).UTC(),
	}

	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	resolved := valid
	resolved.Outcome = outcomeOK
	resolved.ResolvedAt = time.Unix(1001, 0).UTC()

	if err := resolved.Validate(); err != nil {
		t.Fatalf("Validate(resolved) error = %v", err)
	}

	tests := map[string]func(*Invocation){
		"unsupported version":    func(i *Invocation) { i.Version = ProtocolVersion + 1 },
		"invalid id":             func(i *Invocation) { i.ID = "short" },
		"invalid digest":         func(i *Invocation) { i.IntentDigest = "short" },
		"unknown action":         func(i *Invocation) { i.Action = "push" },
		"missing admission time": func(i *Invocation) { i.AdmittedAt = time.Time{} },
		"unknown outcome":        func(i *Invocation) { i.Outcome = "raw failure" },
		"unresolved with result": func(i *Invocation) { i.ResolvedAt = time.Unix(1, 0).UTC() },
		"resolved without time":  func(i *Invocation) { i.Outcome = outcomeOK },
		"unresolved with output": func(i *Invocation) { i.Output = jsontext.Value(`{}`) },
		"malformed output": func(i *Invocation) {
			i.Outcome = outcomeOK
			i.ResolvedAt = time.Unix(1, 0).UTC()
			i.Output = jsontext.Value(`{"name":`)
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			record := valid
			mutate(&record)

			if err := record.Validate(); !errors.Is(err, ErrInvalidFrame) {
				t.Fatalf("Validate() error = %v, want %v", err, ErrInvalidFrame)
			}
		})
	}
}

func TestAdmitInvocationHonorsCancellation(t *testing.T) {
	dir, _ := openTestReceiptDir(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	request := admittedRequest(t)

	if _, _, err := AdmitInvocation(ctx, dir, request, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("AdmitInvocation() error = %v, want cancellation", err)
	}
}

func TestResolvedInvocationKeepsOutputThatFitsTheReceipt(t *testing.T) {
	dir, _ := openTestReceiptDir(t)
	now := time.Unix(1000, 0).UTC()

	small, _, err := AdmitInvocation(t.Context(), dir, admittedRequest(t), now)
	if err != nil {
		t.Fatal(err)
	}

	output := jsontext.Value(`{"session":{"id":"$1","name":"work"}}`)
	if err := ResolveInvocation(t.Context(), dir, small.ID, outcomeOK, output, now); err != nil {
		t.Fatal(err)
	}

	large, _, err := AdmitInvocation(t.Context(), dir, admittedRequest(t), now)
	if err != nil {
		t.Fatal(err)
	}

	oversized := jsontext.Value(`{"name":"` + strings.Repeat("x", maxInvocationBytes) + `"}`)
	if err := ResolveInvocation(t.Context(), dir, large.ID, string(action.CodePartial), oversized, now); err != nil {
		t.Fatalf("ResolveInvocation(oversized output) error = %v, want the outcome kept", err)
	}

	if result, err := LookupPeerInvocation(dir, small.ID); err != nil || !result.OK || string(result.Output) != string(output) {
		t.Fatalf("small receipt = %#v, %v; want success with its output", result, err)
	}

	if result, err := LookupPeerInvocation(dir, large.ID); err != nil || result.Code != action.CodePartial || len(result.Output) != 0 {
		t.Fatalf("oversized receipt = %#v, %v; want the partial outcome without output", result, err)
	}
}

func TestPruneInvocationsRemovesRecordsFromAnotherProtocol(t *testing.T) {
	dir, _ := openTestReceiptDir(t)
	now := time.Unix(1000, 0).UTC()

	stale := `{"version":1,"id":"0123456789abcdef0123456789abcdef","intentDigest":"` + strings.Repeat("a", 64) +
		`","operation":"kill","admittedAt":"1970-01-01T00:16:40Z","outcome":"","resolvedAt":"0001-01-01T00:00:00Z","durableOperationId":""}`
	if err := writeFileAtomic(dir, "0123456789abcdef0123456789abcdef.json", []byte(stale)); err != nil {
		t.Fatal(err)
	}

	staleFence := `{"version":1,"tombstones":{"fedcba9876543210fedcba9876543210":{"intentDigest":"` + strings.Repeat("b", 64) +
		`","operation":"kill","outcome":"ok","durableOperationId":"","retiredAt":"1970-01-01T00:16:40Z"}}}`
	if err := writeFileAtomic(dir, replayFenceName, []byte(staleFence)); err != nil {
		t.Fatal(err)
	}

	if err := pruneInvocations(dir, 1, now); err != nil {
		t.Fatalf("pruneInvocations() error = %v, want older records to free their space", err)
	}

	if _, err := dir.Read("0123456789abcdef0123456789abcdef.json", maxInvocationBytes); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("record from another protocol still on disk: %v", err)
	}

	if _, admitted, err := AdmitInvocation(t.Context(), dir, admittedRequest(t), now); err != nil || !admitted {
		t.Fatalf("admission next to a fence from another protocol = %t, %v; want admitted", admitted, err)
	}
}
