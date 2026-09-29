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
	maxDirectoryEntriesRead = 16384
	directoryReadBatch      = 512
)

var ErrDirectoryLimit = errors.New("directory suggestions limit exceeded")

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

	entries, readErr := readPrefixedEntries(ctx, f, namePrefix)

	closeErr := f.Close()
	if readErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}

	if closeErr != nil {
		return nil, oops.In("session").Code("directory_read_failed").With("operation", "directory_suggestions").Wrap(closeErr)
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

// readPrefixedEntries keeps only entries matching namePrefix while reading in batches, so the
// scan budget bounds work in huge directories without rejecting a prefix that narrows the results.
func readPrefixedEntries(ctx context.Context, f *os.File, namePrefix string) ([]os.DirEntry, error) {
	var matches []os.DirEntry

	scanned := 0

	for {
		if err := ctx.Err(); err != nil {
			return nil, oops.In("session").Code("operation_canceled").With("operation", "directory_suggestions").Wrap(err)
		}

		batch, err := f.ReadDir(directoryReadBatch)
		scanned += len(batch)

		if scanned > maxDirectoryEntriesRead {
			return nil, oops.In("session").Code("directory_limit").With("operation", "directory_suggestions").Wrap(ErrDirectoryLimit)
		}

		for _, entry := range batch {
			if strings.HasPrefix(entry.Name(), namePrefix) {
				matches = append(matches, entry)
			}
		}

		if errors.Is(err, io.EOF) || (err == nil && len(batch) == 0) {
			return matches, nil
		}

		if err != nil {
			return nil, oops.In("session").Code("directory_read_failed").With("operation", "directory_suggestions").Wrap(err)
		}
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
