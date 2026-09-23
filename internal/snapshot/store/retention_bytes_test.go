//go:build linux || darwin

package store

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/snapshot/model"
)

func TestRetentionBytesExactByteBoundary(t *testing.T) {
	t.Run("boundary_A_exact_fit", func(t *testing.T) {
		r, _, _, total, protectedBytes := setupExactBoundaryFixture(t)

		testExactBoundaryA(t, r, total, protectedBytes)
	})

	t.Run("boundary_B_prune_oldest", func(t *testing.T) {
		r, gens, sizes, total, _ := setupExactBoundaryFixture(t)

		testExactBoundaryB(t, r, gens[0].ID, total, sizes[0])
	})

	t.Run("boundary_C_exact_fit_remaining", func(t *testing.T) {
		r, _, sizes, total, _ := setupExactBoundaryFixture(t)
		expectedRetainedB := total - sizes[0]

		if _, err := r.RetainBytes(t.Context(), total-1); err != nil {
			t.Fatal(err)
		}

		testExactBoundaryC(t, r, expectedRetainedB)
	})

	t.Run("boundary_D_prune_second_oldest", func(t *testing.T) {
		r, gens, sizes, total, protectedBytes := setupExactBoundaryFixture(t)
		expectedRetainedB := total - sizes[0]

		if _, err := r.RetainBytes(t.Context(), total-1); err != nil {
			t.Fatal(err)
		}

		testExactBoundaryD(t, r, gens[1].ID, expectedRetainedB, protectedBytes)
	})

	t.Run("boundary_E_protected_over_budget", func(t *testing.T) {
		r, _, sizes, total, protectedBytes := setupExactBoundaryFixture(t)

		if _, err := r.RetainBytes(t.Context(), total-sizes[0]-sizes[1]); err != nil {
			t.Fatal(err)
		}

		testExactBoundaryE(t, r, protectedBytes)
	})
}

func setupExactBoundaryFixture(t *testing.T) (*Repository, []Generation, []int64, int64, int64) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)

	gens := make([]Generation, 0, 7)
	sizes := make([]int64, 0, 7)
	total := int64(0)

	for i := range 7 {
		g := commitContentFixture(t, r, bytes.Repeat([]byte{byte('a' + i)}, 100*(i+1)))
		gens = append(gens, g)
		size := generationDiskBytes(t, path, g.ID)
		sizes = append(sizes, size)
		total += size
	}

	protectedBytes := sizes[2] + sizes[3] + sizes[4] + sizes[5] + sizes[6]

	return r, gens, sizes, total, protectedBytes
}

func testExactBoundaryA(t *testing.T, r *Repository, total, protectedBytes int64) {
	t.Helper()

	resA, err := r.RetainBytes(t.Context(), total)
	if err != nil {
		t.Fatalf("Boundary A: %v", err)
	}

	if len(resA.Removed) != 0 {
		t.Errorf("Boundary A: removed %v, want 0", resA.Removed)
	}

	if resA.RetainedBytes != total {
		t.Errorf("Boundary A: RetainedBytes = %d, want %d", resA.RetainedBytes, total)
	}

	if resA.ProtectedBytes != protectedBytes {
		t.Errorf("Boundary A: ProtectedBytes = %d, want %d", resA.ProtectedBytes, protectedBytes)
	}

	if resA.BudgetUnmet {
		t.Errorf("Boundary A: BudgetUnmet should be false")
	}
}

func testExactBoundaryB(t *testing.T, r *Repository, oldestID string, total, oldestSize int64) {
	t.Helper()

	resB, err := r.RetainBytes(t.Context(), total-1)
	if err != nil {
		t.Fatalf("Boundary B: %v", err)
	}

	if diff := cmp.Diff([]string{oldestID}, resB.Removed); diff != "" {
		t.Fatalf("Boundary B: Removed mismatch (-want +got):\n%s", diff)
	}

	expectedRetainedB := total - oldestSize
	if resB.RetainedBytes != expectedRetainedB {
		t.Errorf("Boundary B: RetainedBytes = %d, want %d", resB.RetainedBytes, expectedRetainedB)
	}

	if resB.BudgetUnmet {
		t.Errorf("Boundary B: BudgetUnmet should be false")
	}
}

func testExactBoundaryC(t *testing.T, r *Repository, expectedRetainedB int64) {
	t.Helper()

	resC, err := r.RetainBytes(t.Context(), expectedRetainedB)
	if err != nil {
		t.Fatalf("Boundary C: %v", err)
	}

	if len(resC.Removed) != 0 {
		t.Errorf("Boundary C: removed %v, want 0", resC.Removed)
	}

	if resC.RetainedBytes != expectedRetainedB {
		t.Errorf("Boundary C: RetainedBytes = %d, want %d", resC.RetainedBytes, expectedRetainedB)
	}
}

func testExactBoundaryD(t *testing.T, r *Repository, secondOldestID string, expectedRetainedB, protectedBytes int64) {
	t.Helper()

	resD, err := r.RetainBytes(t.Context(), expectedRetainedB-1)
	if err != nil {
		t.Fatalf("Boundary D: %v", err)
	}

	if diff := cmp.Diff([]string{secondOldestID}, resD.Removed); diff != "" {
		t.Fatalf("Boundary D: Removed mismatch (-want +got):\n%s", diff)
	}

	if resD.RetainedBytes != protectedBytes {
		t.Errorf("Boundary D: RetainedBytes = %d, want %d", resD.RetainedBytes, protectedBytes)
	}

	if resD.BudgetUnmet {
		t.Errorf("Boundary D: BudgetUnmet should be false")
	}
}

func testExactBoundaryE(t *testing.T, r *Repository, protectedBytes int64) {
	t.Helper()

	resE, err := r.RetainBytes(t.Context(), protectedBytes-1)
	if err != nil {
		t.Fatalf("Boundary E: %v", err)
	}

	if len(resE.Removed) != 0 {
		t.Errorf("Boundary E: removed %v, want 0", resE.Removed)
	}

	if resE.RetainedBytes != protectedBytes || resE.ProtectedBytes != protectedBytes {
		t.Errorf("Boundary E: RetainedBytes = %d, want %d", resE.RetainedBytes, protectedBytes)
	}

	if !resE.BudgetUnmet {
		t.Errorf("Boundary E: BudgetUnmet must be true")
	}
}

func TestRetentionBytesUnevenCompressedSizesAndManifestOverhead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)

	contents := [][]byte{
		bytes.Repeat([]byte("A"), 10),
		bytes.Repeat([]byte("B"), 5000),
		bytes.Repeat([]byte("C"), 100),
		bytes.Repeat([]byte("D"), 20000),
		bytes.Repeat([]byte("E"), 200),
		bytes.Repeat([]byte("F"), 10000),
		bytes.Repeat([]byte("G"), 50),
	}

	gens := make([]Generation, 0, len(contents))

	for _, c := range contents {
		gens = append(gens, commitContentFixture(t, r, c))
	}

	u, err := r.Usage(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	targetBudget := u.TotalBytes - u.GenerationBytes[gens[0].ID] - u.GenerationBytes[gens[1].ID]

	res, err := r.RetainBytes(t.Context(), targetBudget)
	if err != nil {
		t.Fatal(err)
	}

	expectedRemoved := []string{gens[1].ID, gens[0].ID}
	if diff := cmp.Diff(expectedRemoved, res.Removed); diff != "" {
		t.Fatalf("Removed mismatch (-want +got):\n%s", diff)
	}

	if res.RetainedBytes != targetBudget {
		t.Errorf("RetainedBytes = %d, want %d", res.RetainedBytes, targetBudget)
	}

	if res.BudgetUnmet {
		t.Errorf("BudgetUnmet should be false")
	}
}

func TestRetentionBytesOldestFirstDeterministicSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)

	gens := make([]Generation, 0, 8)
	for i := range 8 {
		gens = append(gens, commitContentFixture(t, r, bytes.Repeat([]byte{byte('0' + i)}, 100)))
	}

	u, err := r.Usage(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	budget := u.TotalBytes - u.GenerationBytes[gens[0].ID]

	res, err := r.RetainBytes(t.Context(), budget)
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff([]string{gens[0].ID}, res.Removed); diff != "" {
		t.Fatalf("Oldest first removed mismatch (-want +got):\n%s", diff)
	}

	budget2 := res.RetainedBytes - u.GenerationBytes[gens[1].ID]

	res2, err := r.RetainBytes(t.Context(), budget2)
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff([]string{gens[1].ID}, res2.Removed); diff != "" {
		t.Fatalf("Second oldest removed mismatch (-want +got):\n%s", diff)
	}
}

func TestRetentionBytesComposition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)

	baseTime := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	g0 := commitSnapshotAt(t, r, baseTime.Add(-3*time.Hour))
	g1 := commitSnapshotAt(t, r, baseTime.Add(-1*time.Hour))
	g2 := commitSnapshotAt(t, r, baseTime.Add(-30*time.Minute))

	protectedGens := make([]Generation, 0, 5)
	for i := range 5 {
		protectedGens = append(protectedGens, commitSnapshotAt(t, r, baseTime.Add(time.Duration(i)*time.Minute)))
	}

	u, err := r.Usage(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	var protectedBytes int64
	for _, g := range protectedGens {
		protectedBytes += u.GenerationBytes[g.ID]
	}

	policy := RetentionPolicy{
		MaxAge:   2 * time.Hour,
		MaxCount: 6,
		MaxBytes: protectedBytes,
	}

	res, err := r.RetainPolicy(t.Context(), baseTime, policy)
	if err != nil {
		t.Fatalf("RetainPolicy composition: %v", err)
	}

	expectedRemoved := []string{g2.ID, g1.ID, g0.ID}
	if diff := cmp.Diff(expectedRemoved, res.Removed); diff != "" {
		t.Fatalf("Composition removed mismatch (-want +got):\n%s", diff)
	}

	if res.RetainedBytes != protectedBytes {
		t.Errorf("RetainedBytes = %d, want %d", res.RetainedBytes, protectedBytes)
	}

	if res.BudgetUnmet {
		t.Errorf("BudgetUnmet should be false")
	}
}

func TestRetentionBytesProtectedSourcesOverBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)

	for i := range 5 {
		commitContentFixture(t, r, bytes.Repeat([]byte{byte('A' + i)}, 200))
	}

	u, err := r.Usage(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	res, err := r.RetainBytes(t.Context(), 50)
	if err != nil {
		t.Fatalf("RetainBytes over budget: %v", err)
	}

	if len(res.Removed) != 0 {
		t.Errorf("protected sources must not be pruned: removed %v", res.Removed)
	}

	if !res.BudgetUnmet {
		t.Errorf("BudgetUnmet must be true")
	}

	if res.RetainedBytes != u.TotalBytes {
		t.Errorf("RetainedBytes = %d, want %d", res.RetainedBytes, u.TotalBytes)
	}

	if res.ProtectedBytes != u.TotalBytes {
		t.Errorf("ProtectedBytes = %d, want %d", res.ProtectedBytes, u.TotalBytes)
	}
}

func TestRetentionBytesPinnedSourceRace(t *testing.T) {
	t.Run("permanent_pin_survival", testPermanentPinSurvival)
	t.Run("concurrent_pin_retain_race", testConcurrentPinRetainRace)
}

func testPermanentPinSurvival(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)

	gens := make([]Generation, 0, 7)
	for i := range 7 {
		gens = append(gens, commitContentFixture(t, r, bytes.Repeat([]byte{byte('0' + i)}, 300)))
	}

	pinnedID := gens[0].ID

	_, err := r.Pin(t.Context(), pinnedID, model.Bytes("target"), []Action{{ID: "act1", Kind: actionLaunch, State: "planned"}})
	if err != nil {
		t.Fatal(err)
	}

	u, err := r.Usage(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	budget := u.TotalBytes - u.GenerationBytes[gens[1].ID]

	res, err := r.RetainBytes(t.Context(), budget)
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff([]string{gens[1].ID}, res.Removed); diff != "" {
		t.Fatalf("Only gen 1 should be removed (-want +got):\n%s", diff)
	}

	if _, err := r.Read(t.Context(), pinnedID); err != nil {
		t.Fatalf("pinned source must remain readable: %v", err)
	}
}

func testConcurrentPinRetainRace(t *testing.T) {
	for trial := range 10 {
		runConcurrentTrial(t, trial)
	}
}

func runConcurrentTrial(t *testing.T, trial int) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)

	gens := make([]Generation, 0, 8)

	for i := range 8 {
		b := "abcdefghijklmnopqrstuvwxyz"[trial%26]
		gens = append(gens, commitContentFixture(t, r, bytes.Repeat([]byte{b}, 100*(i+1))))
	}

	_, err := r.Pin(t.Context(), gens[0].ID, model.Bytes("perm-target"), []Action{{ID: "act-perm", Kind: actionLaunch, State: "planned"}})
	if err != nil {
		t.Fatal(err)
	}

	u, err := r.Usage(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	targetID := gens[1].ID
	budget := u.TotalBytes - u.GenerationBytes[targetID]

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)

	var (
		wg        sync.WaitGroup
		pinErr    error
		retainRes RetentionResult
		retainErr error
	)

	wg.Go(func() {
		_, pinErr = r.Pin(ctx, targetID, model.Bytes("race-target"), []Action{{ID: "act-race", Kind: actionLaunch, State: "planned"}})
	})

	wg.Go(func() {
		retainRes, retainErr = r.RetainBytes(ctx, budget)
	})

	wg.Wait()
	cancel()

	if retainErr != nil {
		t.Fatalf("trial %d: RetainBytes failed: %v", trial, retainErr)
	}

	targetRemoved := slices.Contains(retainRes.Removed, targetID)
	verifyPinTrialOutcome(t, trial, pinErr, targetRemoved, targetID, gens[0].ID, r)
}

func verifyPinTrialOutcome(t *testing.T, trial int, pinErr error, targetRemoved bool, targetID, permID string, r *Repository) {
	t.Helper()

	if pinErr == nil {
		if targetRemoved {
			t.Fatalf("trial %d: invariant violation: successful pin to a removed source", trial)
		}

		if _, err := r.Read(t.Context(), targetID); err != nil {
			t.Fatalf("trial %d: pinned source not readable after winning pin: %v", trial, err)
		}
	}

	if pinErr != nil {
		if !errors.Is(pinErr, ErrUnavailable) {
			t.Fatalf("trial %d: expected ErrUnavailable when pinning pruned source, got: %v", trial, pinErr)
		}

		if !targetRemoved {
			t.Fatalf("trial %d: pin failed with ErrUnavailable but target was not removed", trial)
		}
	}

	if _, err := r.Read(t.Context(), permID); err != nil {
		t.Fatalf("trial %d: permanently pinned source unavailable: %v", trial, err)
	}
}

func TestRetentionBytesDisabledPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)

	for range 7 {
		commitContentFixture(t, r, []byte("data"))
	}

	res, err := r.RetainBytes(t.Context(), 0)
	if err != nil {
		t.Fatalf("RetainBytes(0): %v", err)
	}

	if len(res.Removed) != 0 {
		t.Errorf("disabled policy should remove nothing, got %v", res.Removed)
	}

	if res.RetainedBytes != 0 || res.ProtectedBytes != 0 || res.BudgetUnmet {
		t.Errorf("disabled policy should not compute byte totals")
	}

	_, err = r.RetainBytes(t.Context(), -1)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("RetainBytes(-1) err = %v, want ErrConflict", err)
	}
}

func TestRetentionBytesTamperedManifestDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)

	gens := make([]Generation, 0, 7)
	for range 7 {
		gens = append(gens, commitContentFixture(t, r, []byte("data")))
	}

	manifestPath := filepath.Clean(filepath.Join(path, "generations", gens[0].ID, "manifest.json"))

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	data[len(data)-2] = 'Z'
	rewriteFixtureFile(t, manifestPath, data)

	_, err = r.RetainBytes(t.Context(), 100)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt on tampered manifest, got %v", err)
	}
}

func TestRetentionBytesCorruptedArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)

	gens := make([]Generation, 0, 7)
	for range 7 {
		gens = append(gens, commitContentFixture(t, r, []byte("sample artifact data")))
	}

	artifactFile := filepath.Clean(filepath.Join(path, "generations", gens[0].ID, "artifacts", "a0.bin.gz"))
	rewriteFixtureFile(t, artifactFile, []byte("corrupt gzip payload!"))

	_, err := r.RetainBytes(t.Context(), 100)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt on corrupted artifact, got %v", err)
	}
}

func TestRetentionBytesTruncatedArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)

	gens := make([]Generation, 0, 7)
	for range 7 {
		gens = append(gens, commitContentFixture(t, r, bytes.Repeat([]byte("test"), 100)))
	}

	artifactFile := filepath.Clean(filepath.Join(path, "generations", gens[0].ID, "artifacts", "a0.bin.gz"))

	data, err := os.ReadFile(artifactFile)
	if err != nil {
		t.Fatal(err)
	}

	truncated := data
	if len(truncated) > 3 {
		truncated = truncated[:3]
	}

	rewriteFixtureFile(t, artifactFile, truncated)

	_, err = r.RetainBytes(t.Context(), 100)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt on truncated artifact, got %v", err)
	}
}

func TestRetentionBytesOversizedArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)

	gens := make([]Generation, 0, 7)
	for range 7 {
		gens = append(gens, commitContentFixture(t, r, []byte("short")))
	}

	artifactFile := filepath.Clean(filepath.Join(path, "generations", gens[0].ID, "artifacts", "a0.bin.gz"))

	f, err := os.OpenFile(artifactFile, os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	if err := f.Truncate(maxCompressedBytes + 1024); err != nil {
		_ = f.Close()

		t.Fatal(err)
	}

	_ = f.Close()

	_, err = r.RetainBytes(t.Context(), 100)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt on oversized artifact, got %v", err)
	}
}

func TestRetentionBytesSymlinksHardlinksFIFOs(t *testing.T) {
	t.Run("symlink", testSymlinkArtifact)
	t.Run("hardlink", testHardlinkArtifact)
	t.Run("fifo", testFIFOArtifact)
}

func testSymlinkArtifact(t *testing.T) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)
	gens := make([]Generation, 0, 7)

	for range 7 {
		gens = append(gens, commitContentFixture(t, r, []byte("data")))
	}

	artFile := filepath.Clean(filepath.Join(path, "generations", gens[0].ID, "artifacts", "a0.bin.gz"))
	targetFile := filepath.Clean(filepath.Join(t.TempDir(), "target.bin.gz"))
	_ = os.WriteFile(targetFile, []byte("target"), 0o600)

	_ = os.Remove(artFile)
	if err := os.Symlink(targetFile, artFile); err != nil {
		t.Fatal(err)
	}

	_, err := r.RetainBytes(t.Context(), 100)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt on symlink, got %v", err)
	}

	if !errors.Is(err, unix.ELOOP) && !errors.Is(err, privatefs.ErrUnsafe) {
		t.Fatalf("expected ELOOP or ErrUnsafe cause, got %v", err)
	}
}

func testHardlinkArtifact(t *testing.T) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)
	gens := make([]Generation, 0, 7)

	for range 7 {
		gens = append(gens, commitContentFixture(t, r, []byte("data")))
	}

	artFile := filepath.Clean(filepath.Join(path, "generations", gens[0].ID, "artifacts", "a0.bin.gz"))
	linkFile := filepath.Clean(filepath.Join(t.TempDir(), "art_link.bin.gz"))

	if err := os.Link(artFile, linkFile); err != nil {
		t.Fatal(err)
	}

	_, err := r.RetainBytes(t.Context(), 100)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt on hardlink, got %v", err)
	}

	if !errors.Is(err, privatefs.ErrUnsafe) {
		t.Fatalf("expected privatefs.ErrUnsafe cause, got %v", err)
	}
}

func testFIFOArtifact(t *testing.T) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)
	gens := make([]Generation, 0, 7)

	for range 7 {
		gens = append(gens, commitContentFixture(t, r, []byte("data")))
	}

	artFile := filepath.Clean(filepath.Join(path, "generations", gens[0].ID, "artifacts", "a0.bin.gz"))

	_ = os.Remove(artFile)
	if err := unix.Mkfifo(artFile, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := r.RetainBytes(t.Context(), 100)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt on FIFO, got %v", err)
	}

	if !errors.Is(err, privatefs.ErrUnsafe) {
		t.Fatalf("expected privatefs.ErrUnsafe cause, got %v", err)
	}
}

func TestRetentionBytesOverflowSyntheticInputs(t *testing.T) {
	cat := Catalog{
		Version:  catalogVersion,
		Sequence: 10,
		Current:  "gen9",
		Generations: []Generation{
			{ID: "gen9", Sequence: 9, ManifestSHA256: "0000000000000000000000000000000000000000000000000000000000000000", Complete: true},
			{ID: "gen8", Sequence: 8, ManifestSHA256: "0000000000000000000000000000000000000000000000000000000000000000", Complete: true},
			{ID: "gen7", Sequence: 7, ManifestSHA256: "0000000000000000000000000000000000000000000000000000000000000000", Complete: true},
			{ID: "gen6", Sequence: 6, ManifestSHA256: "0000000000000000000000000000000000000000000000000000000000000000", Complete: true},
			{ID: "gen5", Sequence: 5, ManifestSHA256: "0000000000000000000000000000000000000000000000000000000000000000", Complete: true},
			{ID: "gen4", Sequence: 4, ManifestSHA256: "0000000000000000000000000000000000000000000000000000000000000000", Complete: true},
		},
	}

	syntheticSizes := map[string]int64{
		"gen9": math.MaxInt64 - 10,
		"gen8": 20,
		"gen7": 100,
		"gen6": 100,
		"gen5": 100,
		"gen4": 100,
	}

	policy := RetentionPolicy{MaxAge: 0, MaxCount: 0, MaxBytes: 1000}

	_, err := cat.evaluateRetention(time.Time{}, policy, nil, syntheticSizes)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt on synthetic size overflow, got %v", err)
	}
}

func TestRetentionBytesCancelUncertainPublish(t *testing.T) {
	t.Run("cancellation", testRetentionBytesCancellation)
	t.Run("uncertain_before_replacement", testRetentionBytesUncertainBefore)
	t.Run("uncertain_after_replacement", testRetentionBytesUncertainAfter)
}

func testRetentionBytesCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)
	gens := make([]Generation, 0, 7)

	for range 7 {
		gens = append(gens, commitContentFixture(t, r, []byte("data")))
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	res, err := r.RetainBytes(ctx, 10)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	if len(res.Removed) != 0 {
		t.Errorf("canceled retain should report no removals, got %v", res.Removed)
	}

	for _, g := range gens {
		if _, err := r.Read(t.Context(), g.ID); err != nil {
			t.Fatalf("generation %s missing after cancellation: %v", g.ID, err)
		}
	}
}

func testRetentionBytesUncertainBefore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)
	gens := make([]Generation, 0, 7)

	for range 7 {
		gens = append(gens, commitContentFixture(t, r, []byte("data")))
	}

	u, err := r.Usage(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	budget := u.TotalBytes - u.GenerationBytes[gens[0].ID]

	r.fault = func(phase string) error {
		if phase == faultCatalogSynced {
			return errInjected
		}

		return nil
	}

	res, err := r.RetainBytes(t.Context(), budget)
	if !errors.Is(err, errInjected) {
		t.Fatalf("expected errInjected on pre-replacement fault, got %v", err)
	}

	if len(res.Removed) != 0 {
		t.Errorf("failed retain must not claim removals were applied, got %v", res.Removed)
	}

	reopened := openRepository(t, path)

	for _, g := range gens {
		if _, err := reopened.Read(t.Context(), g.ID); err != nil {
			t.Fatalf("generation %s unavailable after pre-replacement fault: %v", g.ID, err)
		}
	}
}

func testRetentionBytesUncertainAfter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)
	gens := make([]Generation, 0, 7)

	for range 7 {
		gens = append(gens, commitContentFixture(t, r, []byte("data")))
	}

	u, err := r.Usage(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	budget := u.TotalBytes - u.GenerationBytes[gens[0].ID]

	r.fault = func(phase string) error {
		if phase == faultCatalogReplaced {
			return errInjected
		}

		return nil
	}

	res, err := r.RetainBytes(t.Context(), budget)
	if !errors.Is(err, ErrUncertain) {
		t.Fatalf("expected ErrUncertain on post-replacement fault, got %v", err)
	}

	if len(res.Removed) != 0 {
		t.Errorf("uncertain retain must not claim removals were applied, got %v", res.Removed)
	}

	reopened := openRepository(t, path)

	catalog, err := reopened.catalog()
	if err != nil {
		t.Fatalf("reopened catalog corrupt: %v", err)
	}

	if err := catalog.validate(); err != nil {
		t.Fatalf("reopened catalog invalid: %v", err)
	}

	for _, g := range gens {
		manifestPath := filepath.Join(path, "generations", g.ID, "manifest.json")
		if _, err := os.Stat(manifestPath); err != nil {
			t.Fatalf("manifest destroyed after uncertain publication: %v", err)
		}

		artifactPath := filepath.Join(path, "generations", g.ID, "artifacts", "a0.bin.gz")
		if _, err := os.Stat(artifactPath); err != nil {
			t.Fatalf("artifact destroyed after uncertain publication: %v", err)
		}
	}
}

func TestRetentionBytesPhysicalSourcePreservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)

	gens := make([]Generation, 0, 7)
	for i := range 7 {
		gens = append(gens, commitContentFixture(t, r, bytes.Repeat([]byte{byte('a' + i)}, 100)))
	}

	u, err := r.Usage(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	budget := u.TotalBytes - u.GenerationBytes[gens[0].ID]

	res, err := r.RetainBytes(t.Context(), budget)
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff([]string{gens[0].ID}, res.Removed); diff != "" {
		t.Fatalf("Removed mismatch (-want +got):\n%s", diff)
	}

	if _, err := r.Read(t.Context(), gens[0].ID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("retired generation still readable from catalog: %v", err)
	}

	manifestPath := filepath.Join(path, "generations", gens[0].ID, "manifest.json")
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("manifest.json destroyed on disk: %v", err)
	}

	artifactPath := filepath.Join(path, "generations", gens[0].ID, "artifacts", "a0.bin.gz")
	if _, err := os.Stat(artifactPath); err != nil {
		t.Fatalf("artifact destroyed on disk: %v", err)
	}
}

func TestRetentionBytesNoOpDoesNotPublish(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)

	for range 5 {
		commitContentFixture(t, r, []byte("data"))
	}

	catalogPath := filepath.Join(path, "catalog.json")

	bytesBefore, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}

	r.fault = func(phase string) error {
		if phase == faultCatalogSynced || phase == faultCatalogReplaced {
			return errInjected
		}

		return nil
	}

	res, err := r.RetainBytes(t.Context(), 10)
	if err != nil {
		t.Fatalf("no-op retention should succeed without publish: %v", err)
	}

	if len(res.Removed) != 0 {
		t.Errorf("no removals should occur, got %v", res.Removed)
	}

	if !res.BudgetUnmet {
		t.Errorf("BudgetUnmet should be true")
	}

	bytesAfter, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(bytesBefore, bytesAfter) {
		t.Fatal("catalog.json bytes modified on disk during no-op retention")
	}
}

func TestRetentionBytesMissingSizeMapRejected(t *testing.T) {
	cat := Catalog{
		Version:  catalogVersion,
		Sequence: 10,
		Current:  "gen9",
		Generations: []Generation{
			{ID: "gen9", Sequence: 9, ManifestSHA256: "0000000000000000000000000000000000000000000000000000000000000000", Complete: true},
			{ID: "gen8", Sequence: 8, ManifestSHA256: "0000000000000000000000000000000000000000000000000000000000000000", Complete: true},
			{ID: "gen7", Sequence: 7, ManifestSHA256: "0000000000000000000000000000000000000000000000000000000000000000", Complete: true},
			{ID: "gen6", Sequence: 6, ManifestSHA256: "0000000000000000000000000000000000000000000000000000000000000000", Complete: true},
			{ID: "gen5", Sequence: 5, ManifestSHA256: "0000000000000000000000000000000000000000000000000000000000000000", Complete: true},
			{ID: "gen4", Sequence: 4, ManifestSHA256: "0000000000000000000000000000000000000000000000000000000000000000", Complete: true},
		},
	}

	incompleteSizes := map[string]int64{
		"gen9": 100,
		"gen8": 100,
		"gen7": 100,
		"gen6": 100,
		"gen5": 100,
	}

	policy := RetentionPolicy{MaxAge: 0, MaxCount: 0, MaxBytes: 1000}

	_, err := cat.evaluateRetention(time.Time{}, policy, nil, incompleteSizes)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt on missing size map entry, got %v", err)
	}

	disabledPolicy := RetentionPolicy{MaxAge: 0, MaxCount: 0, MaxBytes: 0}

	res, err := cat.evaluateRetention(time.Time{}, disabledPolicy, nil, incompleteSizes)
	if err != nil {
		t.Fatalf("disabled byte policy should not evaluate sizes: %v", err)
	}

	if len(res.Removed) != 0 {
		t.Errorf("disabled policy should remove nothing, got %v", res.Removed)
	}
}
