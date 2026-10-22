//go:build linux || darwin

package config_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/config"
)

func TestReadSourceFileReturnsExactBoundedBytes(t *testing.T) {
	t.Parallel()

	content := []byte("[remote]\nssh_path = '/private/bin/ssh'\n")

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := config.ReadSourceFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, content) {
		t.Fatalf("ReadSourceFile() = %q, want exact source bytes", got)
	}
}

func TestReadSourceFileRejectsStdinPath(t *testing.T) {
	t.Parallel()

	if _, err := config.ReadSourceFile("-"); err == nil {
		t.Fatal("ReadSourceFile accepted stdin as a watched file")
	}
}

func TestReadSourceFileRejectsSymlinkedParent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(realDir, "config.toml"), []byte("[remote]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	linkedDir := filepath.Join(root, "linked")
	if err := os.Symlink(realDir, linkedDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, err := config.ReadSourceFile(filepath.Join(linkedDir, "config.toml"))
	if !errors.Is(err, config.ErrConfigSymlink) {
		t.Fatalf("ReadSourceFile() error = %v, want ErrConfigSymlink", err)
	}
}

func TestReadSourceFileRejectsSpecialAndOversizedFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	fifo := filepath.Join(root, "config.fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	if _, err := config.ReadSourceFile(fifo); !errors.Is(err, config.ErrConfigSymlink) {
		t.Fatalf("FIFO error = %v, want unsafe-file rejection", err)
	}

	oversized := filepath.Join(root, "oversized.toml")
	if err := os.WriteFile(oversized, bytes.Repeat([]byte{'x'}, (1<<20)+1), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := config.ReadSourceFile(oversized); !errors.Is(err, config.ErrFileTooLarge) {
		t.Fatalf("oversized error = %v, want ErrFileTooLarge", err)
	}
}
