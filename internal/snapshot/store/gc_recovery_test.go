//go:build linux || darwin

package store

import (
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/zigai/sesh/internal/privatefs"
)

func interruptCollection(t *testing.T, r *Repository, phase string) {
	t.Helper()

	r.fault = func(current string) error {
		if current == phase {
			return errInjected
		}

		return nil
	}

	if _, err := r.Collect(t.Context(), CollectionBudget{Generations: 8, Bytes: 1 << 30}); !errors.Is(err, errInjected) {
		t.Fatalf("interruption at %s = %v", phase, err)
	}

	r.fault = nil
}

func sortedSkips(skips []CollectionSkip) []CollectionSkip {
	ordered := slices.Clone(skips)
	slices.SortFunc(ordered, func(a, b CollectionSkip) int { return strings.Compare(a.Generation, b.Generation) })

	return ordered
}

func encodeFixtureIntent(t *testing.T, intent CollectionIntent) []byte {
	t.Helper()

	data, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func validFixtureIntent(generation string, sequence uint64) CollectionIntent {
	return CollectionIntent{
		Version:        collectionVersion,
		Generation:     generation,
		Sequence:       sequence,
		State:          collectionStateDeleting,
		Reason:         "",
		ManifestSHA256: digest([]byte("manifest")),
		ManifestBytes:  8,
		Files:          []CollectionFile{{Name: "a0.bin.gz", Bytes: 4}},
		Bytes:          12,
	}
}

func TestCollectionResumesAfterInterruptionAtEachBoundary(t *testing.T) {
	tests := []struct {
		phase    string
		finished bool
	}{
		{phase: collectionPhaseIntentSynced},
		{phase: collectionPhaseArtifactsUnlinked},
		{phase: collectionPhaseManifestUnlinked},
		{phase: collectionPhaseGenerationUnlinked},
		{phase: collectionPhaseOutcomeSynced, finished: true},
	}

	for _, test := range tests {
		t.Run(test.phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)
			generations := retireGenerations(t, r, 2)

			interruptCollection(t, r, test.phase)
			assertCollectionRecovery(t, r, path, generations[0].ID, generations[1].ID, test.finished)
		})
	}
}

func assertCollectionRecovery(t *testing.T, r *Repository, path, retired, current string, finished bool) {
	t.Helper()

	assertSelectionIntact(t, r, current)
	assertInterruptedIntent(t, r, retired, finished)

	result := collectGenerations(t, r)

	switch {
	case finished && (result.Resumed || len(result.Removed) != 0 || result.Bytes != 0):
		t.Fatalf("finished interruption reported work: %+v", result)
	case !finished && (!result.Resumed || len(result.Removed) != 1 || result.Removed[0] != retired):
		t.Fatalf("interrupted intent was not resumed: %+v", result)
	}

	assertGenerationRemoved(t, path, retired)
	assertGenerationPresent(t, path, current)
	assertRepositoryUnavailable(t, r, retired)
	assertCollectionSettled(t, r, retired)

	third := collectGenerations(t, r)
	if len(third.Removed)+len(third.Deferred)+len(third.Preserved)+len(third.Quarantined) != 0 || third.Bytes != 0 {
		t.Fatalf("completed collection was not a no-op: %+v", third)
	}
}

func assertSelectionIntact(t *testing.T, r *Repository, current string) {
	t.Helper()

	if _, err := r.Read(t.Context(), current); err != nil {
		t.Fatal("committed generation lost to interruption", err)
	}

	if _, err := r.Current(t.Context()); err != nil {
		t.Fatal("current selection lost to interruption", err)
	}
}

func assertInterruptedIntent(t *testing.T, r *Repository, retired string, finished bool) {
	t.Helper()

	wantState := collectionStateDeleting
	if finished {
		wantState = collectionStateRemoved
	}

	intent, err := r.InspectCollection(t.Context(), retired)
	if err != nil {
		t.Fatal(err)
	}

	if intent.State != wantState {
		t.Fatalf("interrupted intent state = %s, want %s", intent.State, wantState)
	}
}

func assertCollectionSettled(t *testing.T, r *Repository, retired string) {
	t.Helper()

	intent, err := r.InspectCollection(t.Context(), retired)
	if err != nil {
		t.Fatal(err)
	}

	if intent.State != collectionStateRemoved {
		t.Fatalf("resumed intent state = %s", intent.State)
	}
}

func changedContentFixtures() []struct {
	name   string
	damage func(t *testing.T, path, id string)
} {
	return []struct {
		name   string
		damage func(t *testing.T, path, id string)
	}{
		{name: "unknown entry appears after the intent", damage: plantGenerationEntry},
		{name: "remaining manifest is replaced", damage: truncateGenerationManifest},
		{name: "remaining artifact is replaced", damage: replaceArtifactBytes},
	}
}

func TestCollectionQuarantinesChangedGenerationContent(t *testing.T) {
	for _, test := range changedContentFixtures() {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)
			generations := retireGenerations(t, r, 2)
			retired, current := generations[0], generations[1]

			interruptCollection(t, r, collectionPhaseIntentSynced)
			test.damage(t, path, retired.ID)
			assertCollectionQuarantine(t, r, path, retired.ID, current.ID)
		})
	}
}

func assertCollectionQuarantine(t *testing.T, r *Repository, path, retired, current string) {
	t.Helper()

	tree := repositoryTree(t, path)
	result := collectGenerations(t, r)

	want := []CollectionSkip{{Generation: retired, Reason: CollectionQuarantineContent}}
	if diff := cmp.Diff(want, result.Quarantined); diff != "" {
		t.Fatal(diff)
	}

	if len(result.Removed) != 0 || result.Bytes != 0 {
		t.Fatalf("contradictory content was removed: %+v", result)
	}

	intent, err := r.InspectCollection(t.Context(), retired)
	if err != nil {
		t.Fatal(err)
	}

	if intent.State != collectionStateQuarantined || intent.Reason != CollectionQuarantineContent {
		t.Fatalf("quarantined intent = %+v", intent)
	}

	assertOnlyIntentRecordsChanged(t, tree, repositoryTree(t, path))

	next := collectGenerations(t, r)

	want = []CollectionSkip{{Generation: retired, Reason: CollectionReasonQuarantined}}
	if diff := cmp.Diff(want, next.Preserved); diff != "" {
		t.Fatal(diff)
	}

	if len(next.Quarantined) != 0 || len(next.Removed) != 0 {
		t.Fatalf("quarantined intent was resumed: %+v", next)
	}

	if _, err := r.Read(t.Context(), current); err != nil {
		t.Fatal(err)
	}

	assertRepositoryUnavailable(t, r, retired)
}

func assertOnlyIntentRecordsChanged(t *testing.T, before, after map[string]bool) {
	t.Helper()

	for entry := range after {
		if strings.HasSuffix(entry, collectionRecordSuffix) {
			delete(after, entry)
		}
	}

	for entry := range before {
		if strings.HasSuffix(entry, collectionRecordSuffix) {
			delete(before, entry)
		}
	}

	if diff := cmp.Diff(before, after); diff != "" {
		t.Fatal(diff)
	}
}

func TestCollectionQuarantinesContradictoryIntentEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generations := retireGenerations(t, r, 2)
	retired, current := generations[0], generations[1]

	interruptCollection(t, r, collectionPhaseIntentSynced)

	stored := readCollectionRecordFixture(t, path, retired.ID)
	stored.Sequence++
	writeCollectionRecordFixture(t, path, retired.ID, encodeFixtureIntent(t, stored))

	tree := repositoryTree(t, path)
	result := collectGenerations(t, r)

	want := []CollectionSkip{{Generation: retired.ID, Reason: CollectionQuarantineEvidence}}
	if diff := cmp.Diff(want, result.Quarantined); diff != "" {
		t.Fatal(diff)
	}

	if len(result.Removed) != 0 || result.Bytes != 0 {
		t.Fatalf("contradictory evidence removed bytes: %+v", result)
	}

	assertGenerationPresent(t, path, retired.ID)

	if _, err := r.Read(t.Context(), current.ID); err != nil {
		t.Fatal(err)
	}

	for entry := range tree {
		if strings.HasSuffix(entry, collectionRecordSuffix) {
			continue
		}

		if _, err := os.Lstat(filepath.Join(path, entry)); err != nil {
			t.Fatalf("generation byte %s was touched: %v", entry, err)
		}
	}
}

func withUnknownIntentMember(t *testing.T, intent CollectionIntent) []byte {
	t.Helper()

	data := encodeFixtureIntent(t, intent)

	return append(data[:len(data)-1], []byte(`,"extra":true}`)...)
}

func TestCollectionPreservesUnreadableIntentRecords(t *testing.T) {
	generation := "0123456789abcdef0123456789abcdef"

	tests := []struct {
		name string
		data []byte
	}{
		{name: "truncated", data: []byte(`{"version":1,`)},
		{name: "unknown member", data: withUnknownIntentMember(t, validFixtureIntent(generation, 1))},
		{name: "mislabeled generation", data: encodeFixtureIntent(t, validFixtureIntent("fedcba9876543210fedcba9876543210", 1))},
		{name: "invalid inventory", data: encodeFixtureIntent(t, CollectionIntent{
			Version:        collectionVersion,
			Generation:     generation,
			Sequence:       1,
			State:          collectionStateDeleting,
			Reason:         "",
			ManifestSHA256: digest([]byte("manifest")),
			ManifestBytes:  8,
			Files:          []CollectionFile{{Name: "a0.bin.gz", Bytes: 4}},
			Bytes:          13,
		})},
		{name: "unsafe file name", data: encodeFixtureIntent(t, CollectionIntent{
			Version:        collectionVersion,
			Generation:     generation,
			Sequence:       1,
			State:          collectionStateDeleting,
			Reason:         "",
			ManifestSHA256: digest([]byte("manifest")),
			ManifestBytes:  8,
			Files:          []CollectionFile{{Name: "../catalog.json", Bytes: 4}},
			Bytes:          12,
		})},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)
			generations := retireGenerations(t, r, 2)
			retired, current := generations[0], generations[1]

			writeCollectionRecordFixture(t, path, retired.ID, test.data)

			tree := repositoryTree(t, path)
			result := collectGenerations(t, r)

			want := []CollectionSkip{{Generation: retired.ID, Reason: CollectionReasonUnreadableIntent}}
			if diff := cmp.Diff(want, result.Preserved); diff != "" {
				t.Fatal(diff)
			}

			if len(result.Removed) != 0 || result.Bytes != 0 {
				t.Fatalf("unreadable intent authorized removal: %+v", result)
			}

			if diff := cmp.Diff(tree, repositoryTree(t, path)); diff != "" {
				t.Fatal(diff)
			}

			if _, err := r.Read(t.Context(), current.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCollectionInspectionReportsDurableIntent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generations := retireGenerations(t, r, 2)
	retired := generations[0]

	if _, err := r.InspectCollection(t.Context(), retired.ID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unrecorded intent = %v", err)
	}

	if _, err := r.InspectCollection(t.Context(), "not-a-generation"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("invalid generation = %v", err)
	}

	interruptCollection(t, r, collectionPhaseIntentSynced)

	pending, err := r.InspectCollection(t.Context(), retired.ID)
	if err != nil {
		t.Fatal(err)
	}

	if pending.State != collectionStateDeleting || pending.Generation != retired.ID || pending.Bytes <= 0 || len(pending.Files) == 0 {
		t.Fatalf("recorded intent = %+v", pending)
	}

	collectGenerations(t, r)

	done, err := r.InspectCollection(t.Context(), retired.ID)
	if err != nil {
		t.Fatal(err)
	}

	if done.State != collectionStateRemoved {
		t.Fatalf("terminal intent = %+v", done)
	}

	writeCollectionRecordFixture(t, path, generations[1].ID, []byte("{"))

	if _, err := r.InspectCollection(t.Context(), generations[1].ID); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("unreadable intent = %v", err)
	}
}

func TestCollectionIntentRecordsAreNotReusable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generations := retireGenerations(t, r, 3)
	retired := generations[0]

	interruptCollection(t, r, collectionPhaseArtifactsUnlinked)

	pending := readCollectionRecordFixture(t, path, retired.ID)
	if pending.State != collectionStateDeleting {
		t.Fatalf("pending record = %+v", pending)
	}

	collectGenerations(t, r)

	intent := readCollectionRecordFixture(t, path, retired.ID)
	if intent.State != collectionStateRemoved {
		t.Fatalf("terminal record = %+v", intent)
	}

	if intent.Sequence != pending.Sequence || intent.ManifestSHA256 != pending.ManifestSHA256 ||
		intent.Bytes != pending.Bytes || !slices.Equal(intent.Files, pending.Files) {
		t.Fatal("terminal record changed the authorized inventory")
	}

	tree := repositoryTree(t, path)
	collectGenerations(t, r)

	if diff := cmp.Diff(tree, repositoryTree(t, path)); diff != "" {
		t.Fatal("completed intent was rewritten")
	}

	manifest, artifacts := snapshotFixture()
	if _, err := r.Commit(t.Context(), retired.ID, manifest, artifacts); !errors.Is(err, ErrConflict) {
		t.Fatalf("retired generation ID was reused: %v", err)
	}
}

func TestCollectionResumesFromAnotherRepositoryHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generations := retireGenerations(t, r, 2)
	retired, current := generations[0], generations[1]

	interruptCollection(t, r, collectionPhaseManifestUnlinked)

	reopened := openRepository(t, path)

	result, err := reopened.Collect(t.Context(), CollectionBudget{Generations: 8, Bytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}

	if !result.Resumed {
		t.Fatal("reopened repository did not resume the intent")
	}

	if diff := cmp.Diff([]string{retired.ID}, result.Removed); diff != "" {
		t.Fatal(diff)
	}

	assertGenerationRemoved(t, path, retired.ID)
	assertGenerationPresent(t, path, current.ID)

	if _, err := reopened.Read(t.Context(), current.ID); err != nil {
		t.Fatal(err)
	}
}

func TestCollectionRecordsPrivateIntentInventory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generations := retireGenerations(t, r, 2)
	retired := generations[0]

	result := collectGenerations(t, r)

	if diff := cmp.Diff([]string{retired.ID}, result.Removed); diff != "" {
		t.Fatal(diff)
	}

	names, data := storedCollectionIntent(t, path, retired.ID)

	if diff := cmp.Diff([]string{retired.ID + collectionRecordSuffix}, names); diff != "" {
		t.Fatal(diff)
	}

	if len(data) == 0 || len(data) > maxCollectionRecord {
		t.Fatalf("stored record size = %d", len(data))
	}

	stored := readCollectionRecordFixture(t, path, retired.ID)

	if stored.Generation != retired.ID || stored.State != collectionStateRemoved {
		t.Fatalf("stored record = %+v", stored)
	}

	assertStoredIntentInventory(t, stored)
}

func assertStoredIntentInventory(t *testing.T, stored CollectionIntent) {
	t.Helper()

	if stored.ManifestSHA256 == "" || stored.ManifestBytes <= 0 || stored.Bytes <= 0 || len(stored.Files) == 0 {
		t.Fatalf("stored record lacks durable evidence: %+v", stored)
	}

	for _, file := range stored.Files {
		if !validCollectionFileName(file.Name) {
			t.Fatalf("stored record contains an unsafe name: %q", file.Name)
		}
	}
}

func storedCollectionIntent(t *testing.T, path, generation string) ([]string, []byte) {
	t.Helper()

	records, err := privatefs.Open(filepath.Join(path, collectionDirectory), false)
	if err != nil {
		t.Fatal(err)
	}

	names, err := records.Names()
	if err != nil {
		t.Fatal(err)
	}

	data, err := records.Read(generation+collectionRecordSuffix, maxCollectionRecord)
	if err != nil {
		t.Fatal(err)
	}

	if err := records.Close(); err != nil {
		t.Fatal(err)
	}

	return names, data
}

func TestCollectionKeepsIntentIdentityStableAcrossResumes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generations := retireGenerations(t, r, 2)
	retired := generations[0]

	interruptCollection(t, r, collectionPhaseIntentSynced)
	first := readCollectionRecordFixture(t, path, retired.ID)

	resumed := collectGenerations(t, r)

	if diff := cmp.Diff([]string{retired.ID}, resumed.Intents); diff != "" {
		t.Fatal(diff)
	}

	second := readCollectionRecordFixture(t, path, retired.ID)

	if first.Generation != second.Generation || first.Sequence != second.Sequence ||
		first.ManifestSHA256 != second.ManifestSHA256 || first.Bytes != second.Bytes {
		t.Fatal("resumed intent changed its authorized identity")
	}

	assertGenerationRemoved(t, path, retired.ID)
	assertGenerationPresent(t, path, generations[1].ID)
}
