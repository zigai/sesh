//go:build linux || darwin

package privatefs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestEnsureCreatesPrivateHierarchyWithoutChangingAncestors(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(root, "new", "nested", "history")

	dir, err := Ensure(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := dir.Close(); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{root, filepath.Join(root, "new"), filepath.Dir(path), path} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}

		want := os.FileMode(0o700)
		if name == root {
			want = 0o755
		}

		if info.Mode().Perm() != want {
			t.Fatalf("directory mode = %o, want %o", info.Mode().Perm(), want)
		}
	}
}

func TestEnsureRejectsUnsafeLeafAndSymlinkAncestors(t *testing.T) {
	root := t.TempDir()

	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}

	if dir, err := Ensure(target); !errors.Is(err, ErrUnsafe) || dir != nil {
		t.Fatalf("unsafe leaf accepted: %v", err)
	}

	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if dir, err := Ensure(filepath.Join(link, "new", "history")); err == nil || dir != nil {
		t.Fatalf("symlink ancestor accepted: %v", err)
	}

	if _, err := os.Stat(filepath.Join(target, "new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created through symlink: %v", err)
	}

	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("unrelated mode changed: %v", err)
	}
}

func TestEnsureRetainsDirectoryAfterPathReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state")

	dir, err := Ensure(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := dir.Close(); err != nil {
			t.Error(err)
		}
	})

	old := filepath.Join(root, "original")
	if err := os.Rename(path, old); err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	file, err := dir.File("owned", unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY)
	if err != nil {
		t.Fatal(err)
	}

	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(old, "owned")); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(path, "owned")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("write followed replacement: %v", err)
	}
}

func TestOpenParentDoesNotWeakenPrivateDirectoryOpen(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}

	if dir, err := Open(root, false); !errors.Is(err, ErrUnsafe) || dir != nil {
		t.Fatalf("Open accepted non-private leaf: %v", err)
	}

	if dir, err := Ensure(root); !errors.Is(err, ErrUnsafe) || dir != nil {
		t.Fatalf("Ensure accepted non-private leaf: %v", err)
	}
}

func TestOpenParentPinsNonPrivateDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(root, "status.json")

	dir, name, err := OpenParent(path, false)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := dir.Close(); err != nil {
			t.Error(err)
		}
	})

	if name != "status.json" {
		t.Fatalf("name = %q, want status.json", name)
	}

	moved := root + "-moved"
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}

	file, err := dir.File(name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		t.Fatal(err)
	}

	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(moved, name)); err != nil {
		t.Fatalf("pinned parent file missing: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("write followed replacement: %v", err)
	}
}

func TestOpenParentAcceptsRootParent(t *testing.T) {
	dir, name, err := OpenParent("/status.json", false)
	if err != nil {
		t.Fatal(err)
	}

	if name != "status.json" {
		t.Fatalf("name = %q, want status.json", name)
	}

	if err := dir.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenParentRejectsSymlinkedParent(t *testing.T) {
	root := t.TempDir()

	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	dir, _, err := OpenParent(filepath.Join(link, "status.json"), false)
	if err == nil || dir != nil {
		if dir != nil {
			_ = dir.Close()
		}

		t.Fatalf("OpenParent followed symlink: %v", err)
	}
}
