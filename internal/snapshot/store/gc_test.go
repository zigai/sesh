//go:build linux || darwin

package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func retireGenerations(t *testing.T, r *Repository, count int) []Generation {
	t.Helper()

	generations := make([]Generation, 0, count)
	for range count {
		generations = append(generations, commitCompleteFixture(t, r))
	}

	for _, generation := range generations[:count-1] {
		if err := r.Delete(t.Context(), generation.ID); err != nil {
			t.Fatal(err)
		}
	}

	return generations
}

func collectGenerations(t *testing.T, r *Repository) CollectionResult {
	t.Helper()

	result, err := r.Collect(t.Context(), CollectionBudget{Generations: 8, Bytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}

	return result
}

func assertGenerationRemoved(t *testing.T, path, id string) {
	t.Helper()

	if _, err := os.Stat(filepath.Join(path, "generations", id)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("generation bytes survived collection: %v", err)
	}
}

func assertGenerationPresent(t *testing.T, path, id string) {
	t.Helper()

	if _, err := os.Stat(filepath.Join(path, "generations", id, "manifest.json")); err != nil {
		t.Fatal("generation bytes were destroyed", err)
	}
}

func generationStoredBytes(t *testing.T, path, id string) int64 {
	t.Helper()

	dir := filepath.Join(path, generationsDirectory, id)

	manifest, err := os.Stat(filepath.Join(dir, collectionManifestName))
	if err != nil {
		t.Fatal(err)
	}

	total := manifest.Size()

	entries, err := os.ReadDir(filepath.Join(dir, collectionArtifactsName))
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}

		total += info.Size()
	}

	return total
}

func repositoryTree(t *testing.T, path string) map[string]bool {
	t.Helper()

	tree := make(map[string]bool)

	err := filepath.WalkDir(path, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relative, err := filepath.Rel(path, current)
		if err != nil {
			return fmt.Errorf("resolve repository entry: %w", err)
		}

		tree[relative] = true

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return tree
}

func writeCollectionRecordFixture(t *testing.T, path, generation string, data []byte) {
	t.Helper()

	dir := filepath.Join(path, "gc")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, generation+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readCollectionRecordFixture(t *testing.T, path, generation string) CollectionIntent {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(path, "gc", generation+".json"))
	if err != nil {
		t.Fatal(err)
	}

	var intent CollectionIntent
	if err := json.Unmarshal(data, &intent); err != nil {
		t.Fatal(err)
	}

	if err := intent.validate(); err != nil {
		t.Fatal("fixture record is not a valid intent", err)
	}

	return intent
}

func TestCollectionRemovesOnlyRetiredGenerationBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generations := retireGenerations(t, r, 3)
	retired, current := generations[:2], generations[2]

	var want int64
	for _, generation := range retired {
		want += generationStoredBytes(t, path, generation.ID)
	}

	before := repositoryTree(t, path)

	result := collectGenerations(t, r)

	if diff := cmp.Diff([]string{retired[0].ID, retired[1].ID}, result.Removed); diff != "" {
		t.Fatal(diff)
	}

	if len(result.Deferred) != 0 || len(result.Quarantined) != 0 || len(result.Preserved) != 0 {
		t.Fatalf("unexpected collection report: %+v", result)
	}

	if result.Bytes != want {
		t.Fatalf("collected bytes = %d, want %d", result.Bytes, want)
	}

	for _, generation := range retired {
		assertGenerationRemoved(t, path, generation.ID)
		assertRepositoryUnavailable(t, r, generation.ID)
	}

	assertGenerationPresent(t, path, current.ID)

	if _, err := r.Read(t.Context(), current.ID); err != nil {
		t.Fatal("current generation unreadable after collection", err)
	}

	if _, err := r.Current(t.Context()); err != nil {
		t.Fatal("current selection broken after collection", err)
	}

	assertCollectionPublishesNothing(t, before, path, result)
}

func assertCollectionPublishesNothing(t *testing.T, before map[string]bool, path string, result CollectionResult) {
	t.Helper()

	after := repositoryTree(t, path)

	for entry := range before {
		if after[entry] {
			continue
		}

		if entry == "catalog.json" {
			t.Fatal("collection replaced the authoritative catalog")
		}

		if !slices.ContainsFunc(result.Removed, func(id string) bool { return generationSubtree(entry, id) }) {
			t.Fatalf("collection removed an unrelated entry: %s", entry)
		}
	}

	added := make([]string, 0)

	for entry := range after {
		if !before[entry] {
			added = append(added, entry)
		}
	}

	slices.Sort(added)

	intents := make([]string, 0, len(result.Intents)+1)
	intents = append(intents, collectionDirectory)

	for _, id := range result.Intents {
		intents = append(intents, filepath.Join(collectionDirectory, id+collectionRecordSuffix))
	}

	slices.Sort(intents)

	if diff := cmp.Diff(intents, added); diff != "" {
		t.Fatal(diff)
	}
}

func generationSubtree(entry, id string) bool {
	dir := filepath.Join(generationsDirectory, id)

	return entry == dir || strings.HasPrefix(entry, dir+string(filepath.Separator))
}

func assertRepositoryUnavailable(t *testing.T, r *Repository, id string) {
	t.Helper()

	if _, err := r.Read(t.Context(), id); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("retired generation still readable: %v", err)
	}
}

func TestCollectionDefersGenerationAboveByteBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generations := retireGenerations(t, r, 4)
	retired, current := generations[:3], generations[3]

	sizes := make([]int64, 0, len(retired))
	for _, generation := range retired {
		sizes = append(sizes, generationStoredBytes(t, path, generation.ID))
	}

	result, err := r.Collect(t.Context(), CollectionBudget{Generations: 8, Bytes: sizes[0] - 1})
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff([]string{retired[0].ID, retired[1].ID, retired[2].ID}, result.Deferred); diff != "" {
		t.Fatal(diff)
	}

	if len(result.Removed) != 0 || result.Bytes != 0 {
		t.Fatalf("budget breach: %+v", result)
	}

	for _, generation := range retired {
		assertGenerationPresent(t, path, generation.ID)
	}

	assertGenerationPresent(t, path, current.ID)
}

func TestCollectionGenerationBudgetRemovesOldestFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generations := retireGenerations(t, r, 4)
	retired, current := generations[:3], generations[3]

	sizes := make([]int64, 0, len(retired))
	for _, generation := range retired {
		sizes = append(sizes, generationStoredBytes(t, path, generation.ID))
	}

	result, err := r.Collect(t.Context(), CollectionBudget{Generations: 1, Bytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff([]string{retired[0].ID}, result.Removed); diff != "" {
		t.Fatal(diff)
	}

	if diff := cmp.Diff([]string{retired[1].ID, retired[2].ID}, result.Deferred); diff != "" {
		t.Fatal(diff)
	}

	if result.Bytes != sizes[0] {
		t.Fatalf("byte accounting = %d, want %d", result.Bytes, sizes[0])
	}

	assertGenerationRemoved(t, path, retired[0].ID)
	assertGenerationPresent(t, path, retired[1].ID)
	assertGenerationPresent(t, path, retired[2].ID)
	assertGenerationPresent(t, path, current.ID)

	result, err = r.Collect(t.Context(), CollectionBudget{Generations: 8, Bytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff([]string{retired[1].ID, retired[2].ID}, result.Removed); diff != "" {
		t.Fatal(diff)
	}

	if result.Bytes != sizes[1]+sizes[2] {
		t.Fatalf("byte accounting = %d, want %d", result.Bytes, sizes[1]+sizes[2])
	}

	if _, err := r.Read(t.Context(), current.ID); err != nil {
		t.Fatal(err)
	}
}

func TestCollectionRejectsInvalidBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generations := retireGenerations(t, r, 2)

	budgets := []CollectionBudget{
		{Generations: 0, Bytes: 1},
		{Generations: -1, Bytes: 1},
		{Generations: maxEntries + 1, Bytes: 1},
		{Generations: 1, Bytes: 0},
		{Generations: 1, Bytes: -1},
	}

	for _, budget := range budgets {
		result, err := r.Collect(t.Context(), budget)
		assertRepositoryDiagnostic(t, err, "collect", "repository_conflict", ErrConflict)

		if len(result.Removed) != 0 || result.Bytes != 0 {
			t.Fatalf("invalid budget removed bytes: %+v", result)
		}
	}

	assertGenerationPresent(t, path, generations[0].ID)

	if _, err := os.Stat(filepath.Join(path, collectionDirectory)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("invalid budget created collection state")
	}
}

func TestCollectionUncertainCatalogConfirmationPreventsDestruction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generations := retireGenerations(t, r, 2)

	r.fault = func(phase string) error {
		if phase == collectionPhaseCatalogConfirmed {
			return errInjected
		}

		return nil
	}

	result, err := r.Collect(t.Context(), CollectionBudget{Generations: 8, Bytes: 1 << 30})
	assertRepositoryDiagnostic(t, err, "collect", "publication_uncertain", ErrUncertain)

	if !errors.Is(err, errInjected) {
		t.Fatal("lost underlying confirmation cause")
	}

	if len(result.Intents) != 0 || len(result.Removed) != 0 || result.Bytes != 0 {
		t.Fatalf("destructive work followed an unconfirmed catalog: %+v", result)
	}

	assertGenerationPresent(t, path, generations[0].ID)

	if _, err := os.Stat(filepath.Join(path, collectionDirectory)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("unconfirmed catalog still authorized an intent record")
	}
}

func TestCollectionCancellationLeavesResumableIntent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generations := retireGenerations(t, r, 2)
	retired, current := generations[0], generations[1]

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	r.fault = func(phase string) error {
		if phase == collectionPhaseIntentSynced {
			cancel()
		}

		return nil
	}

	result, err := r.Collect(ctx, CollectionBudget{Generations: 8, Bytes: 1 << 30})
	assertRepositoryDiagnostic(t, err, "collect", "operation_canceled", context.Canceled)

	if diff := cmp.Diff([]string{retired.ID}, result.Intents); diff != "" {
		t.Fatal(diff)
	}

	if len(result.Removed) != 0 || result.Bytes != 0 {
		t.Fatalf("cancellation removed bytes: %+v", result)
	}

	assertGenerationPresent(t, path, retired.ID)

	intent, err := r.InspectCollection(t.Context(), retired.ID)
	if err != nil {
		t.Fatal(err)
	}

	if intent.State != collectionStateDeleting {
		t.Fatalf("cancellation did not leave a resumable intent: %s", intent.State)
	}

	r.fault = nil

	resumed := collectGenerations(t, r)

	if !resumed.Resumed {
		t.Fatal("outstanding intent was not resumed")
	}

	if diff := cmp.Diff([]string{retired.ID}, resumed.Removed); diff != "" {
		t.Fatal(diff)
	}

	assertGenerationRemoved(t, path, retired.ID)
	assertGenerationPresent(t, path, current.ID)
}

func TestCollectionKeepsPinnedAndCurrentSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitFixture(t, r)

	op, err := r.Pin(t.Context(), source.ID, model.Bytes("synthetic-target"), []Action{{ID: actionLaunch, Kind: actionLaunch, State: actionPlanned}})
	if err != nil {
		t.Fatal(err)
	}

	for range 6 {
		commitFixture(t, r)
	}

	if _, err := r.Retain(t.Context(), minimumRetained); err != nil {
		t.Fatal(err)
	}

	result := collectGenerations(t, r)

	if slices.Contains(result.Removed, source.ID) {
		t.Fatalf("collection removed a pinned source: %+v", result)
	}

	assertGenerationPresent(t, path, source.ID)

	if _, err := r.Read(t.Context(), source.ID); err != nil {
		t.Fatal("active recovery source pruned", err)
	}

	if err := r.Finish(t.Context(), op.ID, true); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Retain(t.Context(), minimumRetained); err != nil {
		t.Fatal(err)
	}

	result = collectGenerations(t, r)

	if !slices.Contains(result.Removed, source.ID) {
		t.Fatalf("released source was not collected: %+v", result)
	}

	assertGenerationRemoved(t, path, source.ID)
	assertRepositoryUnavailable(t, r, source.ID)
}

func TestCollectionKeepsCatalogAndForeignEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generations := retireGenerations(t, r, 2)
	retired := generations[0]

	foreign := []string{
		filepath.Join(path, "foreign.txt"),
		filepath.Join(path, "hooks.log"),
		filepath.Join(path, ".catalog-"+"0123456789abcdef0123456789abcdef"),
		filepath.Join(path, "notes"),
		filepath.Join(path, "staging", "0123456789abcdef0123456789abcdef"),
	}
	if err := os.WriteFile(foreign[0], []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(foreign[1], []byte("legacy log"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(foreign[2], []byte("partial catalog"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(foreign[3], 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(foreign[4], 0o700); err != nil {
		t.Fatal(err)
	}

	catalogBefore, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	result := collectGenerations(t, r)

	if diff := cmp.Diff([]string{retired.ID}, result.Removed); diff != "" {
		t.Fatal(diff)
	}

	for _, entry := range foreign {
		if _, err := os.Stat(entry); err != nil {
			t.Fatalf("collection touched unrelated entry %s: %v", entry, err)
		}
	}

	catalogAfter, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff(catalogBefore, catalogAfter); diff != "" {
		t.Fatal("collection republished catalog membership")
	}
}

func TestCollectionAfterUncertainRetentionUsesDurableMembership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	generations := make([]Generation, 0, 7)

	for range 7 {
		generations = append(generations, commitCompleteFixture(t, r))
	}

	r.fault = func(phase string) error {
		if phase == faultCatalogReplaced {
			return errInjected
		}

		return nil
	}

	if _, err := r.Retain(t.Context(), minimumRetained); !errors.Is(err, ErrUncertain) {
		t.Fatalf("retention durability = %v", err)
	}

	r.fault = nil

	retired := assertAttemptedRetentionRemoved(t, r, generations)
	result := collectGenerations(t, r)

	if diff := cmp.Diff(retired, result.Removed); diff != "" {
		t.Fatal(diff)
	}

	for _, id := range retired {
		assertGenerationRemoved(t, path, id)
		assertRepositoryUnavailable(t, r, id)
	}

	for _, generation := range generations[2:] {
		assertGenerationPresent(t, path, generation.ID)

		if _, err := r.Read(t.Context(), generation.ID); err != nil {
			t.Fatal("retained generation lost", err)
		}
	}

	reopened := openRepository(t, path)

	if _, err := reopened.Current(t.Context()); err != nil {
		t.Fatal("current selection lost after uncertain retention", err)
	}

	if _, err := reopened.Read(t.Context(), generations[6].ID); err != nil {
		t.Fatal("newest generation lost after uncertain retention", err)
	}
}

func assertAttemptedRetentionRemoved(t *testing.T, r *Repository, generations []Generation) []string {
	t.Helper()

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	retired := make([]string, 0, len(generations))
	for _, generation := range generations {
		if catalog.generationIndex(generation.ID) < 0 {
			retired = append(retired, generation.ID)
		}
	}

	want := []string{generations[0].ID, generations[1].ID}
	if diff := cmp.Diff(want, retired); diff != "" {
		t.Fatal(diff)
	}

	return retired
}

func twoArtifactFixture() (model.Manifest, map[string]io.Reader) {
	first := []byte("first synthetic terminal history\n")
	second := []byte("second synthetic terminal history\n")
	firstSum, secondSum := sha256.Sum256(first), sha256.Sum256(second)

	manifest := model.Manifest{
		Format: "sesh", Version: model.Schema, Started: 1, Finished: 2,
		Source:   model.Source{TmuxVersion: "3.6", LibraryVersion: "v0.3.0", Consistency: "observed"},
		Sessions: []model.Session{{ID: "s0", Name: model.Bytes("work"), Directory: model.Bytes("/tmp")}},
		Windows: []model.Window{{
			ID: "w0", Name: model.Bytes("editor"), Width: 81, Height: 24,
			Layout: model.Layout{Width: 81, Height: 24, Axis: "x", Children: []model.Layout{
				{Width: 40, Height: 24, Pane: "p0"},
				{Width: 40, Height: 24, Left: 41, Pane: "p1"},
			}},
		}},
		Links: []model.Link{{Session: "s0", Window: "w0", Index: 0, Active: true}},
		Panes: []model.Pane{
			{ID: "p0", Window: "w0", Index: 0, Directory: model.Bytes("/tmp"), Width: 40, Height: 24, Active: true, Terminal: model.Terminal{Artifact: "a0"}, Launch: model.Launch{Recovery: "unverified"}},
			{ID: "p1", Window: "w0", Index: 1, Directory: model.Bytes("/tmp"), Width: 40, Height: 24, Left: 41, Terminal: model.Terminal{Artifact: "a1"}, Launch: model.Launch{Recovery: "unverified"}},
		},
		Artifacts: []model.Artifact{
			{ID: "a0", Kind: "terminal-text", Length: int64(len(first)), SHA256: hex.EncodeToString(firstSum[:])},
			{ID: "a1", Kind: "terminal-text", Length: int64(len(second)), SHA256: hex.EncodeToString(secondSum[:])},
		},
	}
	domains := []string{"S01", "S02", "S03", "S04", "S05", "S06", "S07", "S08", "S09", "S10"}

	for _, domain := range domains {
		manifest.Evidence = append(manifest.Evidence, model.Evidence{Domain: domain, Status: "unverified", Reason: "not_captured"})
	}

	return manifest, map[string]io.Reader{"a0": bytes.NewReader(first), "a1": bytes.NewReader(second)}
}

func commitTwoArtifactFixture(t *testing.T, r *Repository) Generation {
	t.Helper()

	attempt, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	manifest, artifacts := twoArtifactFixture()

	generation, err := r.Commit(t.Context(), attempt.ID, manifest, artifacts)
	if err != nil {
		t.Fatal(err)
	}

	return generation
}

func storedArtifactNames(t *testing.T, path, id string) []string {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(path, generationsDirectory, id, collectionArtifactsName))
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	slices.Sort(names)

	return names
}

func TestCollectionResumesAfterPartialArtifactRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	retired := commitTwoArtifactFixture(t, r)
	current := commitCompleteFixture(t, r)

	if err := r.Delete(t.Context(), retired.ID); err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff([]string{"a0.bin.gz", "a1.bin.gz"}, storedArtifactNames(t, path, retired.ID)); diff != "" {
		t.Fatal(diff)
	}

	interruptCollection(t, r, collectionPhaseArtifactUnlinked)

	remaining := storedArtifactNames(t, path, retired.ID)
	if len(remaining) != 1 {
		t.Fatalf("partial removal state = %v", remaining)
	}

	intent, err := r.InspectCollection(t.Context(), retired.ID)
	if err != nil {
		t.Fatal(err)
	}

	if intent.State != collectionStateDeleting || len(intent.Files) != 2 {
		t.Fatalf("partial intent = %+v", intent)
	}

	result := collectGenerations(t, r)

	if !result.Resumed || !slices.Equal([]string{retired.ID}, result.Removed) {
		t.Fatalf("partial removal was not resumed: %+v", result)
	}

	if want := intent.Files[1].Bytes + intent.ManifestBytes; result.Bytes != want {
		t.Fatalf("resumed byte accounting = %d, want %d", result.Bytes, want)
	}

	assertGenerationRemoved(t, path, retired.ID)
	assertGenerationPresent(t, path, current.ID)
	assertCollectionSettled(t, r, retired.ID)

	if _, err := r.Read(t.Context(), current.ID); err != nil {
		t.Fatal(err)
	}
}

func TestCollectionRejectsPartialRemovalWithUnownedEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	retired := commitTwoArtifactFixture(t, r)
	current := commitCompleteFixture(t, r)

	if err := r.Delete(t.Context(), retired.ID); err != nil {
		t.Fatal(err)
	}

	interruptCollection(t, r, collectionPhaseArtifactUnlinked)

	foreign := filepath.Join(path, generationsDirectory, retired.ID, collectionArtifactsName, "foreign.bin.gz")
	if err := os.WriteFile(foreign, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}

	assertCollectionQuarantine(t, r, path, retired.ID, current.ID)

	if _, err := os.Stat(foreign); err != nil {
		t.Fatal("unowned partial-removal entry was removed", err)
	}
}

func TestCollectionIsIdempotentAfterCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generations := retireGenerations(t, r, 2)
	retired := generations[0]

	first := collectGenerations(t, r)
	if diff := cmp.Diff([]string{retired.ID}, first.Removed); diff != "" {
		t.Fatal(diff)
	}

	tree := repositoryTree(t, path)

	second := collectGenerations(t, r)

	if len(second.Removed)+len(second.Deferred)+len(second.Quarantined)+len(second.Preserved) != 0 || second.Bytes != 0 {
		t.Fatalf("repeated collection was not a no-op: %+v", second)
	}

	if diff := cmp.Diff(tree, repositoryTree(t, path)); diff != "" {
		t.Fatal(diff)
	}

	intent, err := r.InspectCollection(t.Context(), retired.ID)
	if err != nil {
		t.Fatal(err)
	}

	if intent.State != collectionStateRemoved || intent.Reason != "" {
		t.Fatalf("terminal intent state = %+v", intent)
	}
}

func TestCollectionIntentValidation(t *testing.T) {
	valid := CollectionIntent{
		Version:        collectionVersion,
		Generation:     "0123456789abcdef0123456789abcdef",
		Sequence:       3,
		State:          collectionStateDeleting,
		Reason:         "",
		ManifestSHA256: digest([]byte("manifest")),
		ManifestBytes:  8,
		Files:          []CollectionFile{{Name: "a0.bin.gz", Bytes: 4}},
		Bytes:          12,
	}

	if err := valid.validate(); err != nil {
		t.Fatal("valid intent rejected", err)
	}

	tests := []struct {
		name   string
		change func(*CollectionIntent)
	}{
		{name: "version", change: func(i *CollectionIntent) { i.Version = 2 }},
		{name: "generation", change: func(i *CollectionIntent) { i.Generation = "../catalog" }},
		{name: "digest", change: func(i *CollectionIntent) { i.ManifestSHA256 = "zz" }},
		{name: "manifest size", change: func(i *CollectionIntent) { i.ManifestBytes = 0 }},
		{name: "manifest oversize", change: func(i *CollectionIntent) { i.ManifestBytes = model.MaxManifestBytes + 1; i.Bytes = i.ManifestBytes + 4 }},
		{name: "path escaping file", change: func(i *CollectionIntent) {
			i.Files = []CollectionFile{{Name: "../../catalog.json", Bytes: 4}}
		}},
		{name: "unsuffixed file", change: func(i *CollectionIntent) { i.Files = []CollectionFile{{Name: "a0", Bytes: 4}} }},
		{name: "empty file", change: func(i *CollectionIntent) { i.Files = []CollectionFile{{Name: "a0.bin.gz", Bytes: 0}} }},
		{name: "duplicate file", change: func(i *CollectionIntent) {
			i.Files = []CollectionFile{{Name: "a0.bin.gz", Bytes: 4}, {Name: "a0.bin.gz", Bytes: 4}}
			i.Bytes = 16
		}},
		{name: "total mismatch", change: func(i *CollectionIntent) { i.Bytes = 13 }},
		{name: "state without reason", change: func(i *CollectionIntent) { i.State = collectionStateQuarantined }},
		{name: "unknown state", change: func(i *CollectionIntent) { i.State = "collecting" }},
		{name: "unknown quarantine reason", change: func(i *CollectionIntent) {
			i.State, i.Reason = collectionStateQuarantined, "mystery"
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			intent := valid
			intent.Files = slices.Clone(valid.Files)
			test.change(&intent)

			if !errors.Is(intent.validate(), ErrCorrupt) {
				t.Fatal("invalid intent accepted")
			}
		})
	}
}

func TestCollectionRemovesCommittedAndIncompleteGenerations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	committed := commitCompleteFixture(t, r)
	incomplete := commitFixture(t, r)
	current := commitFixture(t, r)

	for _, generation := range []Generation{committed, incomplete} {
		if err := r.Delete(t.Context(), generation.ID); err != nil {
			t.Fatal(err)
		}
	}

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	states := make(map[string]string, len(catalog.Attempts))
	for _, attempt := range catalog.Attempts {
		states[attempt.ID] = attempt.State
	}

	if states[committed.ID] != attemptStateCommitted || states[incomplete.ID] != attemptStateIncomplete {
		t.Fatalf("fixture attempt states = %v", states)
	}

	result := collectGenerations(t, r)

	if diff := cmp.Diff([]string{committed.ID, incomplete.ID}, result.Removed); diff != "" {
		t.Fatal(diff)
	}

	assertGenerationRemoved(t, path, committed.ID)
	assertGenerationRemoved(t, path, incomplete.ID)
	assertGenerationPresent(t, path, current.ID)
}
