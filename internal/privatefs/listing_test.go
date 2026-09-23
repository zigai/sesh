//go:build linux || darwin

package privatefs

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestBoundedDirectoryInventory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private")

	dir, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := dir.Close(); err != nil {
			t.Error(err)
		}
	})

	if names, err := dir.NamesLimit(0); err != nil || len(names) != 0 {
		t.Fatalf("empty inventory: %v %v", names, err)
	}

	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(path, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for limit, cause := range map[int]error{-1: ErrUnsafe, 0: ErrLimit, 1: ErrLimit, math.MaxInt: ErrUnsafe} {
		if names, err := dir.NamesLimit(limit); !errors.Is(err, cause) || names != nil {
			t.Fatalf("limit %d returned partial or accepted inventory: %v %v", limit, names, err)
		}
	}

	requireCompleteInventory(t, dir)
}

func requireCompleteInventory(t *testing.T, dir *Dir) {
	t.Helper()

	for _, limit := range []int{2, 3} {
		names, err := dir.NamesLimit(limit)
		if err != nil || !slices.Equal(slices.Sorted(slices.Values(names)), []string{"a", "b"}) {
			t.Fatalf("complete inventory: %v %v", names, err)
		}
	}
}
