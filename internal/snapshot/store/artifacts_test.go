//go:build linux || darwin

package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/snapshot/model"
)

func TestGenerationArtifactsAreBoundToSource(t *testing.T) {
	for _, kind := range []string{"missing", "tampered", "truncated", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)
			prior := commitCompleteFixture(t, r)
			current := commitCompleteFixture(t, r)
			file := filepath.Join(path, "generations", current.ID, "artifacts", "a0.bin.gz")
			corruptArtifact(t, file, kind)

			if _, err := r.Current(t.Context()); err == nil {
				t.Fatal("accepted corrupted current artifact or fell back")
			}

			if _, err := r.Read(t.Context(), current.ID); err == nil {
				t.Fatal("explicit selection bypassed artifact validation")
			}

			if _, err := r.Read(t.Context(), prior.ID); err != nil {
				t.Fatal("unrelated recovery source changed", err)
			}
		})
	}
}

func corruptArtifact(t *testing.T, file, kind string) {
	t.Helper()

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}

	switch kind {
	case "missing":
		return
	case "tampered":
		err = os.WriteFile(file, []byte("not a gzip artifact"), 0o600)
	case "truncated":
		rewriteFixtureFile(t, file, data[:len(data)-1])
	default:
		external := filepath.Join(t.TempDir(), "external")
		rewriteFixtureFile(t, external, data)

		if kind == "symlink" {
			err = os.Symlink(external, file)
		} else {
			err = os.Link(external, file)
		}
	}

	if err != nil {
		t.Fatal(err)
	}
}

func TestTerminalArtifactsPreserveExactBytes(t *testing.T) {
	for name, content := range map[string][]byte{
		"empty": {}, "single line": []byte("marker"), "spaces": []byte("line one\n\nline two  \n   \n\t\n"),
		"broadcast":      []byte("saved\nBroadcast message from synthetic host:\nThe system will power off now!\nhost% \n"),
		"terminal bytes": []byte("\x1b[31mred\x1b[0m\x00\xff\n"),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)
			generation := commitContentFixture(t, r, content)

			got := readGzipFixture(t, filepath.Join(path, "generations", generation.ID, "artifacts", "a0.bin.gz"))
			if !bytes.Equal(got, content) {
				t.Fatal("stored terminal bytes changed")
			}
		})
	}
}

func commitContentFixture(t *testing.T, r *Repository, content []byte) Generation {
	t.Helper()

	m, artifacts := snapshotFixture()
	m.Artifacts[0].Length, m.Artifacts[0].SHA256 = int64(len(content)), digest(content)
	artifacts["a0"] = bytes.NewReader(content)

	attempt, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	generation, err := r.Commit(t.Context(), attempt.ID, m, artifacts)
	if err != nil {
		t.Fatal(err)
	}

	return generation
}

func readGzipFixture(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	result, readErr := io.ReadAll(reader)
	if err := errors.Join(readErr, reader.Close()); err != nil {
		t.Fatal(err)
	}

	return result
}

func TestSameTimestampSavesKeepTheirOwnContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	first := commitContentFixture(t, r, []byte("first\n"))

	second := commitContentFixture(t, r, []byte("other\n"))
	if first.ID == second.ID || first.Sequence >= second.Sequence {
		t.Fatal("same-time saves collided")
	}

	for _, item := range []struct {
		generation Generation
		content    string
	}{{first, "first\n"}, {second, "other\n"}} {
		m, err := r.Read(t.Context(), item.generation.ID)
		if err != nil {
			t.Fatal(err)
		}

		if m.Started != 1 || m.Finished != 2 {
			t.Fatal("fixture did not exercise equal capture timestamps")
		}

		got := readGzipFixture(t, filepath.Join(path, "generations", item.generation.ID, "artifacts", "a0.bin.gz"))
		if string(got) != item.content {
			t.Fatal("generation read another attempt's pane content")
		}
	}
}

func TestArtifactVerificationKeepsOpenedSourceAcrossRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	plaintext := bytes.Repeat([]byte("deterministic terminal stream verification payload\n"), 100)
	gen := commitContentFixture(t, r, plaintext)
	artifactsDir := openGenerationArtifacts(t, r, gen.ID)

	artFile := filepath.Join(path, "generations", gen.ID, "artifacts", "a0.bin.gz")

	origStat, err := os.Stat(artFile)
	if err != nil {
		t.Fatal(err)
	}

	origSize := origStat.Size()
	altBytes := createAlternateGzipEncoding(t, plaintext, origSize)

	file, err := artifactsDir.File("a0.bin.gz", unix.O_RDONLY)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
	})

	tempAlt := filepath.Join(t.TempDir(), "alt.bin.gz")
	if err := os.WriteFile(tempAlt, altBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(tempAlt, artFile); err != nil {
		t.Fatal(err)
	}

	descriptor := model.Artifact{
		ID:     "a0",
		Kind:   "terminal-text",
		Length: int64(len(plaintext)),
		SHA256: digest(plaintext),
	}

	verifiedSize, err := verifyArtifactFile(t.Context(), file, descriptor, io.Discard)
	if err != nil {
		t.Fatalf("verifyArtifactFile on open descriptor failed: %v", err)
	}

	if verifiedSize != origSize {
		t.Fatalf("verified size %d != original opened descriptor size %d (paired with new file %d)",
			verifiedSize, origSize, len(altBytes))
	}
}

func openGenerationArtifacts(t *testing.T, r *Repository, id string) *privatefs.Dir {
	t.Helper()

	parent, err := r.root.Child("generations", false)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := parent.Close(); err != nil {
			t.Error(err)
		}
	})

	dir, err := parent.Child(id, false)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := dir.Close(); err != nil {
			t.Error(err)
		}
	})

	artifactsDir, err := dir.Child("artifacts", false)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := artifactsDir.Close(); err != nil {
			t.Error(err)
		}
	})

	return artifactsDir
}

func createAlternateGzipEncoding(t *testing.T, plaintext []byte, origSize int64) []byte {
	t.Helper()

	var altBuf bytes.Buffer

	gw, err := gzip.NewWriterLevel(&altBuf, gzip.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}

	gw.Header.Extra = []byte("extra-padding-bytes")

	if _, err := gw.Write(plaintext); err != nil {
		t.Fatal(err)
	}

	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}

	altBytes := altBuf.Bytes()
	if int64(len(altBytes)) == origSize {
		t.Fatal("alternate gzip stream unexpectedly has same compressed length as original")
	}

	return altBytes
}

func TestArtifactVerificationContextCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	content := []byte("cancellation payload\n")
	gen := commitContentFixture(t, r, content)
	artifactsDir := openGenerationArtifacts(t, r, gen.ID)

	descriptor := model.Artifact{
		ID:     "a0",
		Kind:   "terminal-text",
		Length: int64(len(content)),
		SHA256: digest(content),
	}

	file, err := artifactsDir.File("a0.bin.gz", unix.O_RDONLY)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = verifyArtifactFile(ctx, file, descriptor, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled before stat, got %v", err)
	}
}

func TestArtifactVerificationTrailingAndSizeValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	content := []byte("trailing bytes test content\n")
	gen := commitContentFixture(t, r, content)
	artifactsDir := openGenerationArtifacts(t, r, gen.ID)

	artFile := filepath.Join(path, "generations", gen.ID, "artifacts", "a0.bin.gz")

	origBytes, err := os.ReadFile(artFile)
	if err != nil {
		t.Fatal(err)
	}

	descriptor := model.Artifact{
		ID:     "a0",
		Kind:   "terminal-text",
		Length: int64(len(content)),
		SHA256: digest(content),
	}

	corrupted := append(bytes.Clone(origBytes), 0xff, 0xfe)
	rewriteFixtureFile(t, artFile, corrupted)

	file, err := artifactsDir.File("a0.bin.gz", unix.O_RDONLY)
	if err != nil {
		t.Fatal(err)
	}

	_, err = verifyArtifactFile(t.Context(), file, descriptor, io.Discard)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt on trailing bytes, got %v", err)
	}

	if !errors.Is(err, errTrailingArtifactBytes) {
		t.Fatalf("expected errTrailingArtifactBytes cause, got %v", err)
	}
}
