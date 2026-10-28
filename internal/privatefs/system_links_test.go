//go:build linux || darwin

package privatefs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestResolveTrustedLinksFollowsOnlyLinksRootControls(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	sys := filepath.Join(root, "sys")
	open := filepath.Join(root, "open")
	realDir := filepath.Join(sys, "real")

	for _, dir := range []string{realDir, open} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	links := map[string]string{
		filepath.Join(sys, "relative"): "../sys/real",
		filepath.Join(sys, "absolute"): realDir,
		filepath.Join(sys, "chained"):  "relative",
		filepath.Join(sys, "user"):     "real",
		filepath.Join(sys, "loop-a"):   "loop-b",
		filepath.Join(sys, "loop-b"):   "loop-a",
		filepath.Join(sys, "escape"):   "user/../..",
		filepath.Join(open, "link"):    "../sys/real",
	}
	for link, target := range links {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}

	trustedPaths := []string{sys, realDir}
	for _, name := range []string{"relative", "absolute", "chained", "loop-a", "loop-b", "escape"} {
		trustedPaths = append(trustedPaths, filepath.Join(sys, name))
	}

	trustedPaths = append(trustedPaths, filepath.Join(open, "link"))
	for dir := root; ; dir = filepath.Dir(dir) {
		trustedPaths = append(trustedPaths, dir)
		if dir == "/" {
			break
		}
	}

	trusted := make(map[string]bool)

	for _, path := range trustedPaths {
		var stat unix.Stat_t
		if err := unix.Lstat(path, &stat); err != nil {
			t.Fatal(err)
		}

		trusted[inodeOf(&stat)] = true
	}

	isTrusted := func(stat *unix.Stat_t) bool {
		return trusted[inodeOf(stat)]
	}

	cases := []struct {
		name string
		path string
		want string
		err  error
	}{
		{"relative target with parent step", filepath.Join(sys, "relative", "data"), filepath.Join(realDir, "data"), nil},
		{"absolute target", filepath.Join(sys, "absolute", "data"), filepath.Join(realDir, "data"), nil},
		{"link to a link", filepath.Join(sys, "chained", "data"), filepath.Join(realDir, "data"), nil},
		{"missing components stay", filepath.Join(sys, "relative", "missing", "deeper"), filepath.Join(realDir, "missing", "deeper"), nil},
		{"untrusted link stays", filepath.Join(sys, "user", "data"), filepath.Join(sys, "user", "data"), nil},
		{"trusted link in untrusted directory stays", filepath.Join(open, "link", "data"), filepath.Join(open, "link", "data"), nil},
		{"link loop", filepath.Join(sys, "loop-a", "data"), "", ErrSystemLinkLoop},
		{"parent step past an untrusted link", filepath.Join(sys, "escape", "data"), "", ErrUnsafe},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveTrustedLinks(tc.path, isTrusted)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("resolveTrustedLinks(%q) error = %v, want %v", tc.path, err, tc.err)
				}

				return
			}

			if err != nil || got != tc.want {
				t.Fatalf("resolveTrustedLinks(%q) = %q, %v, want %q", tc.path, got, err, tc.want)
			}
		})
	}
}

func inodeOf(stat *unix.Stat_t) string {
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
}
