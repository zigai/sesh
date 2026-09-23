//go:build integration && (linux || darwin)

package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestProcessDeathAtCollectionBoundaries(t *testing.T) {
	phases := []string{
		collectionPhaseIntentSynced,
		collectionPhaseArtifactsUnlinked,
		collectionPhaseManifestUnlinked,
		collectionPhaseGenerationUnlinked,
		collectionPhaseOutcomeSynced,
	}

	for _, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)
			retired := commitFixture(t, r)
			current := commitCompleteFixture(t, r)

			if err := r.Delete(t.Context(), retired.ID); err != nil {
				t.Fatal(err)
			}

			cmd := collectionWorker(t, "collect", path, retired.ID, phase)
			killWorkerAtBoundary(t, cmd)

			verify := collectionWorker(t, "verify", path, retired.ID, phase)
			verify.Env = append(verify.Env, "SESH_TEST_PRIOR="+current.ID)

			if output, err := verify.CombinedOutput(); err != nil {
				t.Fatalf("fresh collection recovery: %v\n%s", err, output)
			}
		})
	}
}

func TestProcessDeathPreservesInterruptedSaveSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	orphan := interruptedSaveFixture(t, r)
	prior := commitCompleteFixture(t, r)

	if _, err := r.Read(t.Context(), prior.ID); err != nil {
		t.Fatal(err)
	}

	cmd := collectionWorker(t, "verify", path, orphan, "none")
	cmd.Env = append(cmd.Env, "SESH_TEST_PRIOR="+prior.ID, "SESH_TEST_MODE=preserve")

	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pending save source lost: %v\n%s", err, output)
	}
}

func TestConcurrentCollectionProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generations := retireGenerations(t, r, 6)
	retired, current := generations[:4], generations[5]

	var workers sync.WaitGroup

	for range 3 {
		cmd := collectionWorker(t, "drain", path, "", "")

		workers.Go(func() {
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("collector: %v\n%s", err, output)
			}
		})
	}

	workers.Wait()

	if t.Failed() {
		t.FailNow()
	}

	for _, generation := range retired {
		assertGenerationAbsent(t, r, generation.ID)
		assertRepositoryUnavailable(t, r, generation.ID)
	}

	assertGenerationPresent(t, path, current.ID)

	if _, err := r.Read(t.Context(), current.ID); err != nil {
		t.Fatal(err)
	}

	assertEveryGenerationDirectoryIsMember(t, r)
}

func TestProcessDeathDuringArtifactRemovalResumes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	retired := commitTwoArtifactFixture(t, r)
	current := commitCompleteFixture(t, r)

	if err := r.Delete(t.Context(), retired.ID); err != nil {
		t.Fatal(err)
	}

	killWorkerAtBoundary(t, collectionWorker(t, "collect", path, retired.ID, collectionPhaseArtifactUnlinked))

	remaining := storedArtifactNames(t, path, retired.ID)
	if len(remaining) != 1 {
		t.Fatalf("killed worker left an unexpected partial state: %v", remaining)
	}

	intent, err := r.InspectCollection(t.Context(), retired.ID)
	if err != nil {
		t.Fatal("durable intent lost across process death", err)
	}

	if intent.State != collectionStateDeleting || len(intent.Files) != 2 {
		t.Fatalf("durable intent after process death = %+v", intent)
	}

	verify := collectionWorker(t, "verify", path, retired.ID, collectionPhaseArtifactUnlinked)
	verify.Env = append(verify.Env, "SESH_TEST_PRIOR="+current.ID)

	if output, err := verify.CombinedOutput(); err != nil {
		t.Fatalf("resumed partial removal: %v\n%s", err, output)
	}
}

func collectionWorker(t *testing.T, mode, path, generation, phase string) *exec.Cmd {
	t.Helper()

	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)

	cmd := exec.CommandContext(ctx, program, "-test.run=^TestCollectionWorker$")
	cmd.Env = []string{
		"SESH_TEST_WORKER=" + mode,
		"SESH_TEST_ROOT=" + path,
		"SESH_TEST_GENERATION=" + generation,
		"SESH_TEST_PHASE=" + phase,
	}

	return cmd
}

func TestCollectionWorker(t *testing.T) {
	mode := os.Getenv("SESH_TEST_WORKER")
	if mode == "" {
		t.Skip("subprocess entry point")
	}

	r := openRepository(t, os.Getenv("SESH_TEST_ROOT"))
	generation := os.Getenv("SESH_TEST_GENERATION")

	switch mode {
	case "drain":
		drainCollectionWorker(t, r)
	case "verify":
		verifyCollectionWorker(t, r, generation, os.Getenv("SESH_TEST_PRIOR"), os.Getenv("SESH_TEST_PHASE"))
	case "collect":
		killableCollectionWorker(t, r, os.Getenv("SESH_TEST_PHASE"))
	default:
		t.Fatal("invalid collection worker mode")
	}
}

func drainCollectionWorker(t *testing.T, r *Repository) {
	t.Helper()

	for range 3 {
		if _, err := r.Collect(t.Context(), CollectionBudget{Generations: 8, Bytes: 1 << 30}); err != nil {
			t.Fatal(err)
		}
	}
}

func killableCollectionWorker(t *testing.T, r *Repository, phase string) {
	t.Helper()

	ready := os.NewFile(3, "collection-boundary")

	t.Cleanup(func() {
		if err := ready.Close(); err != nil {
			t.Error(err)
		}
	})

	r.fault = func(current string) error {
		if current != phase {
			return nil
		}

		if _, err := ready.Write([]byte{1}); err != nil {
			return fmt.Errorf("signal collection boundary: %w", err)
		}

		<-t.Context().Done()

		return errInjected
	}

	if _, err := r.Collect(t.Context(), CollectionBudget{Generations: 8, Bytes: 1 << 30}); err != nil {
		t.Fatal(err)
	}

	t.Fatal("collection worker escaped interruption")
}

func verifyCollectionWorker(t *testing.T, r *Repository, generation, prior, phase string) {
	t.Helper()

	if os.Getenv("SESH_TEST_MODE") == "preserve" {
		assertPendingSourcePreserved(t, r, generation, prior)

		return
	}

	result, err := r.Collect(t.Context(), CollectionBudget{Generations: 8, Bytes: 1 << 30})
	if err != nil {
		t.Fatalf("resumed collection failed: %v", err)
	}

	assertRecoveredCollection(t, r, generation, prior, phase, result)

	settled, err := r.Collect(t.Context(), CollectionBudget{Generations: 8, Bytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}

	if len(settled.Removed)+len(settled.Deferred)+len(settled.Preserved)+len(settled.Quarantined) != 0 || settled.Bytes != 0 {
		t.Fatalf("recovered repository still reports work: %+v", settled)
	}
}

func assertRecoveredCollection(t *testing.T, r *Repository, generation, prior, phase string, result CollectionResult) {
	t.Helper()

	if phase == collectionPhaseOutcomeSynced {
		if result.Resumed || len(result.Removed) != 0 || result.Bytes != 0 {
			t.Fatalf("finished interruption still reported work: %+v", result)
		}
	} else if len(result.Removed) != 1 || result.Removed[0] != generation {
		t.Fatalf("interrupted collection did not complete: %+v", result)
	}

	if _, err := r.Read(t.Context(), prior); err != nil {
		t.Fatal("prior generation unreadable after process death", err)
	}

	if _, err := r.Current(t.Context()); err != nil {
		t.Fatal("current selection broken after process death", err)
	}

	intent, err := r.InspectCollection(t.Context(), generation)
	if err != nil {
		t.Fatal(err)
	}

	if intent.State != collectionStateRemoved {
		t.Fatalf("intent state after recovery = %s", intent.State)
	}

	assertGenerationAbsent(t, r, generation)
	assertEveryGenerationDirectoryIsMember(t, r)
}

func assertPendingSourcePreserved(t *testing.T, r *Repository, orphan, prior string) {
	t.Helper()

	result, err := r.Collect(t.Context(), CollectionBudget{Generations: 8, Bytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Removed) != 0 {
		t.Fatalf("pending save source collected: %+v", result)
	}

	assertPendingSourcePresent(t, r, orphan)
	assertCommittedReadable(t, r, prior)
}

func assertGenerationAbsent(t *testing.T, r *Repository, id string) {
	t.Helper()

	dir, err := r.openGenerationDirectory(id)
	if err == nil {
		if closeErr := dir.Close(); closeErr != nil {
			t.Error(closeErr)
		}

		t.Fatal("retired generation bytes survived recovery")
	}

	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired generation state unclear: %v", err)
	}
}

func assertCommittedReadable(t *testing.T, r *Repository, id string) {
	t.Helper()

	if _, err := r.Read(t.Context(), id); err != nil {
		t.Fatalf("committed generation lost: %v", err)
	}
}

func assertPendingSourcePresent(t *testing.T, r *Repository, orphan string) {
	t.Helper()

	dir, err := r.openGenerationDirectory(orphan)
	if err != nil {
		t.Fatal("pending save source destroyed", err)
	}

	names, readErr := dir.Names()
	closeErr := dir.Close()

	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(names, collectionManifestName) {
		t.Fatal("pending save source lost its manifest")
	}
}

func assertEveryGenerationDirectoryIsMember(t *testing.T, r *Repository) {
	t.Helper()

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	names, err := r.generationNames()
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range names {
		if catalog.generationIndex(name) < 0 {
			t.Fatalf("unmembered generation directory survived: %s", name)
		}
	}
}
