//go:build integration && (linux || darwin)

package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestProcessDeathAtInitializationBoundaries(t *testing.T) {
	phases := []string{
		faultInitRecorded,
		faultInitGenerationsCreated,
		faultInitStagingCreated,
		faultCatalogSynced,
		faultCatalogReplaced,
		faultInitCatalogConfirmed,
		faultInitMarkerRemoved,
		faultInitFinalized,
	}

	for _, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")

			killInitWorker(t, path, phase)

			cmd := initWorker(t, "verify", path, "")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("recovery after %s death failed: %v\n%s", phase, err, output)
			}

			reopened := openRepository(t, path)

			catalog, err := reopened.Inspect(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			if len(catalog.Generations) != 1 {
				t.Fatalf("expected 1 generation, got: %d", len(catalog.Generations))
			}
		})
	}
}

func TestProcessDeathRepeatedInterruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")

	phases := []string{
		faultInitRecorded,
		faultInitGenerationsCreated,
		faultInitStagingCreated,
		faultCatalogSynced,
	}

	for _, phase := range phases {
		killInitWorker(t, path, phase)
	}

	cmd := initWorker(t, "verify", path, "")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("recovery after repeated death failed: %v\n%s", err, output)
	}

	reopened := openRepository(t, path)

	catalog, err := reopened.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(catalog.Generations) != 1 {
		t.Fatalf("expected 1 committed generation, got: %d", len(catalog.Generations))
	}
}

func TestProcessDeathConcurrentOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")

	const count = 4

	var wg sync.WaitGroup

	cmds := make([]*exec.Cmd, count)
	outputs := make([]bytes.Buffer, count)
	errs := make([]error, count)

	for i := range count {
		cmds[i] = initWorker(t, "concurrent", path, "")
		cmds[i].Stdout = &outputs[i]
		cmds[i].Stderr = &outputs[i]
	}

	for i := range count {
		wg.Go(func() {
			errs[i] = cmds[i].Run()
		})
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent process %d failed: %v\n%s", i, err, outputs[i].String())
		}
	}

	r := openRepository(t, path)

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if catalog.Sequence != 0 {
		t.Fatalf("unexpected sequence: %d", catalog.Sequence)
	}

	gen := commitFixture(t, r)
	if _, err := r.Read(t.Context(), gen.ID); err != nil {
		t.Fatal(err)
	}
}

func initWorker(t *testing.T, mode, path, phase string) *exec.Cmd {
	t.Helper()

	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)

	cmd := exec.CommandContext(ctx, program, "-test.run=^TestInitializationWorker$")
	cmd.Env = []string{
		"SESH_TEST_INIT_WORKER=" + mode,
		"SESH_TEST_INIT_ROOT=" + path,
		"SESH_TEST_INIT_PHASE=" + phase,
	}

	return cmd
}

func killInitWorker(t *testing.T, path, phase string) {
	t.Helper()

	cmd := initWorker(t, "init", path, phase)

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = read.Close()
	})

	cmd.ExtraFiles = []*os.File{write}

	var output bytes.Buffer

	cmd.Stdout, cmd.Stderr = &output, &output

	startErr := cmd.Start()
	if err := errors.Join(startErr, write.Close()); err != nil {
		t.Fatal(err)
	}

	reapWorker(t, cmd)

	ready := make(chan error, 1)
	done := make(chan struct{})

	t.Cleanup(func() {
		_ = read.Close()

		<-done
	})

	go func() {
		defer close(done)

		var val [1]byte

		_, rErr := read.Read(val[:])

		ready <- rErr
	}()

	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("worker failed before phase %s: %v", phase, err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("worker did not reach phase %s", phase)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}

	var exit *exec.ExitError
	if err := cmd.Wait(); !errors.As(err, &exit) {
		t.Fatalf("worker was not killed: %v\n%s", err, output.String())
	}
}

func TestInitializationWorker(t *testing.T) {
	mode := os.Getenv("SESH_TEST_INIT_WORKER")
	if mode == "" {
		t.Skip("subprocess entry point")
	}

	path := os.Getenv("SESH_TEST_INIT_ROOT")
	phase := os.Getenv("SESH_TEST_INIT_PHASE")

	switch mode {
	case "init":
		runWorkerInit(t, path, phase)
	case "verify":
		runWorkerVerify(t, path)
	case "concurrent":
		runWorkerConcurrent(t, path)
	default:
		t.Fatalf("unknown worker mode: %s", mode)
	}
}

func runWorkerInit(t *testing.T, path, phase string) {
	t.Helper()

	ready := os.NewFile(3, "init-boundary")

	defer func() {
		if err := ready.Close(); err != nil {
			t.Error(err)
		}
	}()

	r, err := openWithFault(t.Context(), path, func(current string) error {
		if current != phase {
			return nil
		}

		if _, err := ready.Write([]byte{1}); err != nil {
			return fmt.Errorf("signal init boundary: %w", err)
		}

		<-t.Context().Done()

		return errInjected
	})
	if r != nil {
		_ = r.Close()
	}

	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	t.Fatal("worker returned without interruption")
}

func runWorkerVerify(t *testing.T, path string) {
	t.Helper()

	r := openRepository(t, path)

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if catalog.Sequence != 0 {
		t.Fatalf("unexpected sequence: %d", catalog.Sequence)
	}

	_ = commitFixture(t, r)
}

func runWorkerConcurrent(t *testing.T, path string) {
	t.Helper()

	r, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if catalog.Sequence != 0 {
		t.Fatalf("unexpected sequence: %d", catalog.Sequence)
	}

	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}
