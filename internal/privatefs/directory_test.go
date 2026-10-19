//go:build linux || darwin

package privatefs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRejectSymlinksIncludingAncestors(t *testing.T) {
	parent := t.TempDir()

	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{link, filepath.Join(link, "child")} {
		if dir, err := Open(path, true); err == nil {
			if err := dir.Close(); err != nil {
				t.Error(err)
			}

			t.Fatal("followed symlink")
		}
	}

	if _, err := os.Stat(filepath.Join(target, "child")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created through symlink")
	}
}

func TestRejectUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo", "permissions"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repo")

			dir, err := Open(path, true)
			if err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() {
				if err := dir.Close(); err != nil {
					t.Error(err)
				}
			})
			makeUnsafeFile(t, path, kind)

			file, err := dir.File("unsafe", unix.O_RDONLY)
			if err == nil {
				if err := file.Close(); err != nil {
					t.Error(err)
				}

				t.Fatal("accepted unsafe file")
			}
		})
	}
}

func makeUnsafeFile(t *testing.T, root, kind string) {
	t.Helper()

	path := filepath.Join(root, "unsafe")

	var err error

	switch kind {
	case "fifo":
		err = unix.Mkfifo(path, 0o600)
	case "permissions":
		err = os.WriteFile(path, nil, 0o644)
	case "symlink":
		err = os.Symlink("missing", path)
	case "hardlink":
		source := filepath.Join(root, "original")
		if err := os.WriteFile(source, nil, 0o600); err != nil {
			t.Fatal(err)
		}

		err = os.Link(source, path)
	default:
		t.Fatal("unknown fixture")
	}

	if err != nil {
		t.Fatal(err)
	}
}

func TestRemoveDirRejectsSymlinksAndTraversal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")

	dir, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := dir.Close(); err != nil {
			t.Error(err)
		}
	})

	target := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(target, filepath.Join(path, "link")); err != nil {
		t.Fatal(err)
	}

	if err := dir.RemoveDir("link"); err == nil {
		t.Fatal("removed a symlink as a directory")
	}

	if _, err := os.Lstat(filepath.Join(path, "link")); err != nil {
		t.Fatal("changed symlink", err)
	}

	if _, err := os.Stat(target); err != nil {
		t.Fatal("removed target", err)
	}

	for _, name := range []string{"..", "../target", "/target", ""} {
		if err := dir.RemoveDir(name); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("accepted unsafe name: %v", err)
		}
	}
}

func TestRemoveDirRequiresEmptyDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")

	dir, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := dir.Close(); err != nil {
			t.Error(err)
		}
	})

	child, err := dir.CreateDir("owned")
	if err != nil {
		t.Fatal(err)
	}

	if err := child.Close(); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(path, "owned", "data")
	if err := os.WriteFile(file, []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := dir.RemoveDir("owned"); err == nil {
		t.Fatal("removed nonempty directory")
	}

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}

	if err := dir.RemoveDir("owned"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(path, "owned")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory remains: %v", err)
	}
}
