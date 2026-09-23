//go:build linux || darwin

package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zigai/sesh/internal/privatefs"
)

func stagedRewriteFixture(t *testing.T) (*Repository, string, Rewrite) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)
	id := newID()

	r.fault = func(phase string) error {
		if phase == "rewrite_stage_synced" {
			return errInjected
		}

		return nil
	}
	if _, err := r.RemoveSessions(t.Context(), id, source.ID, []string{"s0"}); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal(err)
	}

	r.fault = nil

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	return r, path, catalog.Rewrites[catalog.rewriteIndex(id)]
}

func TestResumeRewritePreservesCorruptKnownStageFiles(t *testing.T) {
	for _, relative := range []string{"manifest.json", "artifacts/a0.bin.gz"} {
		t.Run(relative, func(t *testing.T) {
			r, path, rw := stagedRewriteFixture(t)
			file := filepath.Join(path, "staging", rw.Replacement, relative)

			want := []byte("foreign-content-with-an-expected-filename")
			if err := os.WriteFile(file, want, 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := r.ResumeRewrite(t.Context(), rw.ID)
			if !errors.Is(err, ErrCorrupt) {
				t.Errorf("corrupt stage accepted: %v", err)
			}

			got, readErr := os.ReadFile(file)
			if readErr != nil || !bytes.Equal(got, want) {
				t.Fatalf("corrupt recovery source changed: %q, %v", got, readErr)
			}
		})
	}
}

func TestResumeRewriteRetainsUnsafeEntryCause(t *testing.T) {
	r, path, rw := stagedRewriteFixture(t)

	file := filepath.Join(path, "staging", rw.Replacement, "manifest.json")
	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := r.ResumeRewrite(t.Context(), rw.ID); !errors.Is(err, privatefs.ErrUnsafe) {
		t.Fatalf("lost unsafe-entry cause: %v", err)
	}
}

func assertPendingRewriteSource(t *testing.T, r *Repository, rw Rewrite) {
	t.Helper()

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	index := catalog.rewriteIndex(rw.ID)
	if index < 0 || catalog.Rewrites[index].State != rewriteStatePending || catalog.Current != rw.Source || catalog.generationIndex(rw.Replacement) >= 0 {
		t.Fatalf("recovery changed authoritative membership: %+v", catalog)
	}

	if _, err := r.Read(t.Context(), rw.Source); err != nil {
		t.Fatalf("protected source unavailable: %v", err)
	}
}

func TestResumeRewriteCompletesMatchingPrefixesInPlace(t *testing.T) {
	for _, relative := range []string{"manifest.json", "artifacts/a0.bin.gz"} {
		t.Run(relative, func(t *testing.T) {
			verifyCompletedPrefixInPlace(t, relative)
		})
	}
}

func verifyCompletedPrefixInPlace(t *testing.T, relative string) {
	t.Helper()
	r, path, rw := stagedRewriteFixture(t)
	file := filepath.Join(path, "staging", rw.Replacement, relative)

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	before, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Truncate(file, int64(len(data)/2)); err != nil {
		t.Fatal(err)
	}

	if _, err := r.ResumeRewrite(t.Context(), rw.ID); err != nil {
		t.Fatal(err)
	}

	finalFile := filepath.Join(path, "generations", rw.Replacement, relative)

	after, err := os.Stat(finalFile)
	if err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(finalFile)
	if err != nil {
		t.Fatal(err)
	}

	if !os.SameFile(before, after) || !bytes.Equal(got, data) {
		t.Fatal("recovery replaced the file or changed the expected completed bytes")
	}
}

func TestResumeRewritePreflightsAllArtifactsBeforeAppending(t *testing.T) {
	r, path, rw := stagedRewriteFixture(t)
	first := filepath.Join(path, "staging", rw.Replacement, "artifacts", "a0.bin.gz")
	last := filepath.Join(path, "staging", rw.Replacement, "artifacts", "a1.bin.gz")

	if err := os.Truncate(first, 10); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(last, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := r.ResumeRewrite(t.Context(), rw.ID); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}

	after, err := os.ReadFile(first)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("partial recovery source modified: %v", err)
	}

	assertPendingRewriteSource(t, r, rw)
}

func TestResumeRewriteCancellationBeforePublication(t *testing.T) {
	for _, phase := range []string{"rewrite_resume_stage_validated", "rewrite_resume_pre_publish"} {
		t.Run(phase, func(t *testing.T) {
			r, _, rw := stagedRewriteFixture(t)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			r.fault = func(current string) error {
				if current == phase {
					cancel()
				}

				return nil
			}
			if _, err := r.ResumeRewrite(ctx, rw.ID); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrRecoveryRequired) {
				t.Fatalf("cancellation did not retain pending recovery: %v", err)
			}

			r.fault = nil
			assertPendingRewriteSource(t, r, rw)

			if _, err := r.ResumeRewrite(t.Context(), rw.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResumeRewriteReconfirmsRenamedGenerationDurability(t *testing.T) {
	r, path, rw := stagedRewriteFixture(t)
	if err := os.Rename(filepath.Join(path, "staging", rw.Replacement), filepath.Join(path, "generations", rw.Replacement)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	r.fault = func(phase string) error {
		if phase == "rewrite_resume_generation_sync" {
			cancel()
			return errInjected
		}

		return nil
	}

	_, err := r.ResumeRewrite(ctx, rw.ID)
	if !errors.Is(err, ErrUncertain) || !errors.Is(err, ErrRecoveryRequired) || !errors.Is(err, errInjected) || !errors.Is(err, context.Canceled) {
		t.Fatalf("durability failure lost independent causes: %v", err)
	}

	r.fault = nil
	assertPendingRewriteSource(t, r, rw)

	if _, err := r.ResumeRewrite(t.Context(), rw.ID); err != nil {
		t.Fatal(err)
	}
}
