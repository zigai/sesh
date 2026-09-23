package session

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/samber/oops"
)

const (
	maxDirectorySuggestions = 256
	maxDirectoryEntriesRead = 1024
)

var ErrDirectoryLimit = errors.New("directory suggestions limit exceeded")

func EnsureTrailingSeparator(path string) string {
	if path == "" || strings.HasSuffix(path, string(os.PathSeparator)) {
		return path
	}

	return path + string(os.PathSeparator)
}

func DefaultCreateSessionDirectory(cwd string, sessionName string, suggestion string) string {
	suggestion = strings.TrimSpace(suggestion)
	if suggestion == "" {
		return filepath.Join(cwd, sessionName)
	}

	path := strings.ReplaceAll(suggestion, "{session}", sessionName)
	if expanded, ok := ExpandHomePath(path); ok {
		path = expanded
	}

	if !filepath.IsAbs(path) {
		return filepath.Join(cwd, path)
	}

	return path
}

func DirectoryPathSuggestions(ctx context.Context, input, cwd string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, oops.In("session").Code("operation_canceled").With("operation", "directory_suggestions").Wrap(err)
	}

	dirPart, namePrefix, parent, err := resolveParentAndPrefix(input, cwd)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, oops.In("session").Code("directory_open_failed").With("operation", "directory_suggestions").Wrap(err)
	}

	entries, readErr := f.ReadDir(maxDirectoryEntriesRead + 1)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}

	closeErr := f.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, oops.In("session").Code("directory_read_failed").With("operation", "directory_suggestions").Wrap(err)
	}

	if len(entries) > maxDirectoryEntriesRead {
		return nil, oops.In("session").Code("directory_limit").With("operation", "directory_suggestions").Wrap(ErrDirectoryLimit)
	}

	return collectMatchingDirectories(ctx, entries, parent, dirPart, namePrefix)
}

func ExpandHomePath(path string) (string, bool) {
	separator := string(os.PathSeparator)
	if path != "~" && !strings.HasPrefix(path, "~"+separator) {
		return path, false
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path, false
	}

	if path == "~" {
		return home, true
	}

	return filepath.Join(home, strings.TrimPrefix(path, "~"+separator)), true
}

func sortDirectoryMatches(matches []string) {
	slices.SortFunc(matches, func(left, right string) int {
		leftBase := strings.TrimSuffix(filepath.Base(left), string(os.PathSeparator))
		rightBase := strings.TrimSuffix(filepath.Base(right), string(os.PathSeparator))
		leftHidden := strings.HasPrefix(leftBase, ".")
		rightHidden := strings.HasPrefix(rightBase, ".")

		if leftHidden != rightHidden {
			if leftHidden {
				return 1
			}

			return -1
		}

		leftLower := strings.ToLower(leftBase)

		rightLower := strings.ToLower(rightBase)
		if order := strings.Compare(leftLower, rightLower); order != 0 {
			return order
		}

		return strings.Compare(leftBase, rightBase)
	})
}

func entryIsDirectory(parent string, entry os.DirEntry) bool {
	if entry.IsDir() {
		return true
	}

	if entry.Type()&os.ModeSymlink == 0 {
		return false
	}

	info, err := os.Stat(filepath.Join(parent, entry.Name()))

	return err == nil && info.IsDir()
}

func resolveParentAndPrefix(input, cwd string) (string, string, string, error) {
	separator := string(os.PathSeparator)

	switch {
	case input == "~":
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", "", "", oops.In("session").Code("directory_resolve_failed").With("operation", "resolve_parent").Wrap(err)
		}

		return "~" + separator, "", home, nil
	case strings.HasPrefix(input, "~"+separator):
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", "", "", oops.In("session").Code("directory_resolve_failed").With("operation", "resolve_parent").Wrap(err)
		}

		rel := strings.TrimPrefix(input, "~"+separator)
		relDir, relName := filepath.Split(rel)

		parent := home
		if relDir != "" {
			parent = filepath.Join(home, filepath.Clean(relDir))
		}

		return "~" + separator + relDir, relName, parent, nil
	default:
		dirPart, namePrefix := filepath.Split(input)

		parent := dirPart
		if parent == "" {
			parent = cwd
		} else if !filepath.IsAbs(parent) {
			parent = filepath.Join(cwd, parent)
		}

		return dirPart, namePrefix, parent, nil
	}
}

func collectMatchingDirectories(
	ctx context.Context,
	entries []os.DirEntry,
	parent, dirPart, namePrefix string,
) ([]string, error) {
	separator := string(os.PathSeparator)
	matches := make([]string, 0, min(len(entries), maxDirectorySuggestions))

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, oops.In("session").Code("operation_canceled").With("operation", "directory_suggestions").Wrap(err)
		}

		if !strings.HasPrefix(entry.Name(), namePrefix) || !entryIsDirectory(parent, entry) {
			continue
		}

		suggestion := dirPart + entry.Name() + separator
		matches = append(matches, suggestion)

		if len(matches) > maxDirectorySuggestions {
			return nil, oops.In("session").Code("directory_limit").With("operation", "directory_suggestions").Wrap(ErrDirectoryLimit)
		}
	}

	sortDirectoryMatches(matches)

	return matches, nil
}
