package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestDirectoryPathSuggestionsIncludesOnlyMatchingDirectories(t *testing.T) {
	cwd := t.TempDir()
	for _, dir := range []string{"api", "app", "web"} {
		if err := os.Mkdir(filepath.Join(cwd, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s failed: %v", dir, err)
		}
	}

	if err := os.WriteFile(filepath.Join(cwd, "apple.txt"), []byte("file"), 0o600); err != nil {
		t.Fatalf("write file failed: %v", err)
	}

	got, err := DirectoryPathSuggestions(t.Context(), "ap", cwd)
	if err != nil {
		t.Fatalf("DirectoryPathSuggestions() err = %v", err)
	}

	want := []string{"api" + string(os.PathSeparator), "app" + string(os.PathSeparator)}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("DirectoryPathSuggestions() = %#v, want %#v", got, want)
	}
}

func TestDirectoryPathSuggestionsSupportsRelativeAndAbsolutePaths(t *testing.T) {
	cwd := t.TempDir()

	src := filepath.Join(cwd, "src")
	if err := os.MkdirAll(filepath.Join(src, "server"), 0o755); err != nil {
		t.Fatalf("mkdir server failed: %v", err)
	}

	relative, err := DirectoryPathSuggestions(t.Context(), "src/se", cwd)
	if err != nil {
		t.Fatalf("DirectoryPathSuggestions(relative) err = %v", err)
	}

	if want := "src" + string(os.PathSeparator) + "server" + string(os.PathSeparator); len(relative) != 1 || relative[0] != want {
		t.Fatalf("relative suggestions = %#v, want %q", relative, want)
	}

	absoluteInput := filepath.Join(src, "se")

	absolute, err := DirectoryPathSuggestions(t.Context(), absoluteInput, cwd)
	if err != nil {
		t.Fatalf("DirectoryPathSuggestions(absolute) err = %v", err)
	}

	if want := EnsureTrailingSeparator(filepath.Join(src, "server")); len(absolute) != 1 || absolute[0] != want {
		t.Fatalf("absolute suggestions = %#v, want %q", absolute, want)
	}
}

func TestPathCompletionExpandsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := os.Mkdir(filepath.Join(home, "foo"), 0o755); err != nil {
		t.Fatalf("mkdir home child: %v", err)
	}

	if got := DefaultCreateSessionDirectory(t.TempDir(), "session", "~"); got != home {
		t.Fatalf("DefaultCreateSessionDirectory(~) = %q, want %q", got, home)
	}

	suggestions, err := DirectoryPathSuggestions(t.Context(), "~/fo", t.TempDir())
	if err != nil {
		t.Fatalf("DirectoryPathSuggestions(~/fo) err = %v", err)
	}

	want := "~" + string(os.PathSeparator) + "foo" + string(os.PathSeparator)
	if len(suggestions) != 1 || suggestions[0] != want {
		t.Fatalf("DirectoryPathSuggestions(~/fo) = %#v, want %q", suggestions, want)
	}
}

func TestDirectoryPathSuggestionsSortsHiddenAfterNormalAndIgnoresMissingParents(t *testing.T) {
	cwd := t.TempDir()
	for _, dir := range []string{".config", "zeta", "alpha", ".cache", "beta"} {
		if err := os.Mkdir(filepath.Join(cwd, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s failed: %v", dir, err)
		}
	}

	got, err := DirectoryPathSuggestions(t.Context(), "", cwd)
	if err != nil {
		t.Fatalf("DirectoryPathSuggestions() err = %v", err)
	}

	want := []string{
		"alpha" + string(os.PathSeparator),
		"beta" + string(os.PathSeparator),
		"zeta" + string(os.PathSeparator),
		".cache" + string(os.PathSeparator),
		".config" + string(os.PathSeparator),
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("DirectoryPathSuggestions() = %#v, want %#v", got, want)
	}

	missing, err := DirectoryPathSuggestions(t.Context(), "missing/path", cwd)
	if err != nil {
		t.Fatalf("missing parent err = %v, want nil", err)
	}

	if len(missing) != 0 {
		t.Fatalf("missing parent suggestions = %#v, want empty", missing)
	}
}

func TestDirectoryPathSuggestionsIncludesDirectorySymlinks(t *testing.T) {
	cwd := t.TempDir()

	target := filepath.Join(cwd, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir target failed: %v", err)
	}

	link := filepath.Join(cwd, "target-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	got, err := DirectoryPathSuggestions(t.Context(), "target-", cwd)
	if err != nil {
		t.Fatalf("DirectoryPathSuggestions(symlink) err = %v", err)
	}

	want := []string{"target-link" + string(os.PathSeparator)}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("DirectoryPathSuggestions() = %#v, want %#v", got, want)
	}
}

func TestDirectoryPathSuggestionsContextCancellation(t *testing.T) {
	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, "dir1"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	got, err := DirectoryPathSuggestions(ctx, "", cwd)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got err = %v, results = %v", err, got)
	}
}

func TestDirectoryPathSuggestionsBoundsResults(t *testing.T) {
	cwd := t.TempDir()

	for i := range maxDirectorySuggestions + 20 {
		name := "d" + strconv.Itoa(i)
		if err := os.Mkdir(filepath.Join(cwd, name), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	got, err := DirectoryPathSuggestions(t.Context(), "", cwd)
	if !errors.Is(err, ErrDirectoryLimit) {
		t.Fatalf("expected ErrDirectoryLimit, got err = %v, len = %d", err, len(got))
	}

	if got != nil {
		t.Fatalf("expected nil results on limit exceeded, got %v", got)
	}
}

func TestDirectoryPathSuggestionsNarrowsLargeDirectoriesByPrefix(t *testing.T) {
	cwd := t.TempDir()

	for i := range 2*directoryReadBatch + 100 {
		if err := os.WriteFile(filepath.Join(cwd, "file"+strconv.Itoa(i)), nil, 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}
	}

	if err := os.Mkdir(filepath.Join(cwd, "project"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := DirectoryPathSuggestions(t.Context(), "pro", cwd)
	if err != nil {
		t.Fatalf("DirectoryPathSuggestions() err = %v", err)
	}

	if want := []string{"project" + string(os.PathSeparator)}; !slices.Equal(got, want) {
		t.Fatalf("DirectoryPathSuggestions() = %#v, want %#v", got, want)
	}
}

func TestDefaultCreateSessionDirectory(t *testing.T) {
	cwd := "/test/cwd"

	if got := DefaultCreateSessionDirectory(cwd, "my-sess", ""); got != filepath.Join(cwd, "my-sess") {
		t.Fatalf("got %q, want %q", got, filepath.Join(cwd, "my-sess"))
	}

	if got := DefaultCreateSessionDirectory(cwd, "my-sess", "projects/{session}"); got != filepath.Join(cwd, "projects", "my-sess") {
		t.Fatalf("got %q, want %q", got, filepath.Join(cwd, "projects", "my-sess"))
	}

	if got := DefaultCreateSessionDirectory(cwd, "my-sess", "/opt/{session}"); got != "/opt/my-sess" {
		t.Fatalf("got %q, want /opt/my-sess", got)
	}
}

func EnsureTrailingSeparator(path string) string {
	if path == "" || strings.HasSuffix(path, string(os.PathSeparator)) {
		return path
	}

	return path + string(os.PathSeparator)
}
