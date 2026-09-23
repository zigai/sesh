//go:build linux || darwin

package store

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/snapshot/model"
)

func generationDiskBytes(t *testing.T, repoPath, genID string) int64 {
	t.Helper()

	manifestPath := filepath.Join(repoPath, "generations", genID, "manifest.json")

	mStat, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	artifactsPath := filepath.Join(repoPath, "generations", genID, "artifacts")

	entries, err := os.ReadDir(artifactsPath)
	if err != nil {
		t.Fatal(err)
	}

	total := mStat.Size()

	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}

		total += info.Size()
	}

	return total
}

func assertGenerationUsageEquals(t *testing.T, r *Repository, genID string, want int64) {
	t.Helper()

	u, err := r.GenerationUsage(t.Context(), genID)
	if err != nil {
		t.Fatalf("GenerationUsage(%s): %v", genID, err)
	}

	if u != want {
		t.Errorf("generation %s size = %d, want %d", genID, u, want)
	}
}

func TestUsageEmptyRepository(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)

	usage, err := r.Usage(t.Context())
	if err != nil {
		t.Fatalf("unexpected usage error: %v", err)
	}

	if usage.TotalBytes != 0 || usage.ProtectedBytes != 0 ||
		usage.GenerationCount != 0 || len(usage.GenerationBytes) != 0 {
		t.Errorf("empty usage mismatch: %+v", usage)
	}
}

func TestUsageExactByteAccounting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)

	g1 := commitContentFixture(t, r, []byte("short content"))
	g2 := commitContentFixture(t, r, bytes.Repeat([]byte("repeating pattern of terminal text "), 100))
	g3 := commitContentFixture(t, r, bytes.Repeat([]byte("distinct buffer data \n"), 300))

	size1 := generationDiskBytes(t, path, g1.ID)
	size2 := generationDiskBytes(t, path, g2.ID)
	size3 := generationDiskBytes(t, path, g3.ID)

	assertGenerationUsageEquals(t, r, g1.ID, size1)
	assertGenerationUsageEquals(t, r, g2.ID, size2)
	assertGenerationUsageEquals(t, r, g3.ID, size3)

	usage, err := r.Usage(t.Context())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}

	expectedTotal := size1 + size2 + size3
	if usage.TotalBytes != expectedTotal {
		t.Errorf("usage.TotalBytes = %d, want %d", usage.TotalBytes, expectedTotal)
	}

	if usage.GenerationCount != 3 {
		t.Errorf("usage.GenerationCount = %d, want 3", usage.GenerationCount)
	}

	if usage.GenerationBytes[g1.ID] != size1 || usage.GenerationBytes[g2.ID] != size2 || usage.GenerationBytes[g3.ID] != size3 {
		t.Errorf("unexpected GenerationBytes map: %v", usage.GenerationBytes)
	}
}

func TestUsageProtectedBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)

	gens := make([]Generation, 0, 7)

	for i := range 7 {
		g := commitContentFixture(t, r, bytes.Repeat([]byte{byte('a' + i)}, 50*(i+1)))
		gens = append(gens, g)
	}

	pinnedID := gens[0].ID

	_, err := r.Pin(t.Context(), pinnedID, model.Bytes("target"), []Action{{ID: "act1", Kind: actionLaunch, State: "planned"}})
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}

	usage, err := r.Usage(t.Context())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}

	var expectedProtected int64

	for i, g := range gens {
		if i != 1 {
			expectedProtected += usage.GenerationBytes[g.ID]
		}
	}

	if usage.ProtectedBytes != expectedProtected {
		t.Errorf("usage.ProtectedBytes = %d, want %d", usage.ProtectedBytes, expectedProtected)
	}

	if usage.TotalBytes <= usage.ProtectedBytes {
		t.Errorf("TotalBytes (%d) should exceed ProtectedBytes (%d)", usage.TotalBytes, usage.ProtectedBytes)
	}
}

func TestUsageRejectsTamperedManifestDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)
	g := commitContentFixture(t, r, []byte("content"))

	manifestPath := filepath.Clean(filepath.Join(path, "generations", g.ID, "manifest.json"))

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	data[len(data)-2] = 'X'
	rewriteFixtureFile(t, manifestPath, data)

	_, err = r.Usage(t.Context())
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Usage error = %v, want ErrCorrupt", err)
	}

	_, err = r.GenerationUsage(t.Context(), g.ID)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("GenerationUsage error = %v, want ErrCorrupt", err)
	}
}

func TestUsageRejectsCorruptArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)
	g := commitContentFixture(t, r, []byte("valid content"))

	artifactFile := filepath.Clean(filepath.Join(path, "generations", g.ID, "artifacts", "a0.bin.gz"))
	rewriteFixtureFile(t, artifactFile, []byte("not valid gzip content at all!"))

	_, err := r.Usage(t.Context())
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Usage error = %v, want ErrCorrupt", err)
	}
}

func TestUsageRejectsTruncatedArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)
	g := commitContentFixture(t, r, bytes.Repeat([]byte("payload"), 100))

	artifactFile := filepath.Join(path, "generations", g.ID, "artifacts", "a0.bin.gz")

	data, err := os.ReadFile(artifactFile)
	if err != nil {
		t.Fatal(err)
	}

	truncated := data
	if len(truncated) > 5 {
		truncated = truncated[:5]
	}

	rewriteFixtureFile(t, artifactFile, truncated)

	_, err = r.Usage(t.Context())
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Usage error = %v, want ErrCorrupt", err)
	}
}

func TestUsageRejectsOversizedArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)
	g := commitContentFixture(t, r, []byte("sample"))

	artifactFile := filepath.Join(path, "generations", g.ID, "artifacts", "a0.bin.gz")

	f, err := os.OpenFile(artifactFile, os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	if err := f.Truncate(maxCompressedBytes + 1024); err != nil {
		_ = f.Close()

		t.Fatal(err)
	}

	_ = f.Close()

	_, err = r.Usage(t.Context())
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Usage error = %v, want ErrCorrupt", err)
	}
}

func TestUsageRejectsSymlinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)
	g := commitContentFixture(t, r, []byte("data"))

	artifactFile := filepath.Join(path, "generations", g.ID, "artifacts", "a0.bin.gz")
	targetFile := filepath.Join(t.TempDir(), "target.bin.gz")

	if err := os.WriteFile(targetFile, []byte("target data"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(artifactFile); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(targetFile, artifactFile); err != nil {
		t.Fatal(err)
	}

	_, err := r.Usage(t.Context())
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Usage error = %v, want ErrCorrupt", err)
	}

	if !errors.Is(err, unix.ELOOP) && !errors.Is(err, privatefs.ErrUnsafe) {
		t.Fatalf("Usage error = %v, want unix.ELOOP or privatefs.ErrUnsafe cause", err)
	}
}

func TestUsageRejectsHardlinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)
	g := commitContentFixture(t, r, []byte("data"))

	artifactFile := filepath.Join(path, "generations", g.ID, "artifacts", "a0.bin.gz")
	hardlinkFile := filepath.Join(t.TempDir(), "hardlink.bin.gz")

	if err := os.Link(artifactFile, hardlinkFile); err != nil {
		t.Fatal(err)
	}

	_, err := r.Usage(t.Context())
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Usage error = %v, want ErrCorrupt", err)
	}

	if !errors.Is(err, privatefs.ErrUnsafe) {
		t.Fatalf("Usage error = %v, want privatefs.ErrUnsafe cause", err)
	}
}

func TestUsageRejectsFIFOs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)
	g := commitContentFixture(t, r, []byte("data"))

	artifactFile := filepath.Join(path, "generations", g.ID, "artifacts", "a0.bin.gz")
	if err := os.Remove(artifactFile); err != nil {
		t.Fatal(err)
	}

	if err := unix.Mkfifo(artifactFile, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := r.Usage(t.Context())
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Usage error = %v, want ErrCorrupt", err)
	}

	if !errors.Is(err, privatefs.ErrUnsafe) {
		t.Fatalf("Usage error = %v, want privatefs.ErrUnsafe cause", err)
	}
}

func TestUsageOverflowSyntheticInput(t *testing.T) {
	_, err := safeAdd(math.MaxInt64, 1)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("safeAdd(MaxInt64, 1) error = %v, want ErrCorrupt", err)
	}

	_, err = safeAdd(math.MaxInt64-10, 20)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("safeAdd(MaxInt64-10, 20) error = %v, want ErrCorrupt", err)
	}

	_, err = safeAdd(-1, 10)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("safeAdd(-1, 10) error = %v, want ErrCorrupt", err)
	}

	_, err = safeAdd(10, -1)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("safeAdd(10, -1) error = %v, want ErrCorrupt", err)
	}

	val, err := safeAdd(math.MaxInt64, 0)
	if err != nil || val != math.MaxInt64 {
		t.Fatalf("safeAdd(MaxInt64, 0) = %d, %v; want MaxInt64, nil", val, err)
	}
}

func TestUsageContextCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)
	commitContentFixture(t, r, []byte("data"))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := r.Usage(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Usage error = %v, want context.Canceled", err)
	}
}

func TestGenerationUsageUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	r := openRepository(t, path)

	_, err := r.GenerationUsage(t.Context(), "0123456789abcdef0123456789abcdef")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("GenerationUsage error = %v, want ErrUnavailable", err)
	}
}
