//go:build linux || darwin

package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// DisplayPath abbreviates the home directory to ~ so a report pastes cleanly into an issue.
func DisplayPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == "/" {
		return path
	}

	if path == home {
		return "~"
	}

	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~/" + rest
	}

	return path
}

// shellArg quotes value for a POSIX shell when it needs quoting.
func shellArg(value string) string {
	if shellSafe.MatchString(value) {
		return value
	}

	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// shellPath renders path for a command line, abbreviating the home directory only when the
// result needs no quoting, because a quoted ~ does not expand.
func shellPath(path string) string {
	if display := DisplayPath(path); display != path && shellSafe.MatchString(display) {
		return display
	}

	return shellArg(path)
}

// pathCopies lists every executable named name in the PATH directories of pathEnv, in PATH
// order, counting links to the same file once.
func pathCopies(pathEnv, name string) []string {
	var (
		copies []string
		seen   []os.FileInfo
	)

	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}

		candidate := filepath.Join(dir, name)

		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			continue
		}

		duplicate := false

		for _, prior := range seen {
			if os.SameFile(prior, info) {
				duplicate = true

				break
			}
		}

		if duplicate {
			continue
		}

		seen = append(seen, info)
		copies = append(copies, candidate)
	}

	return copies
}

// lookPath resolves a command the way a shell would with pathEnv as PATH.
func lookPath(pathEnv, command string) (string, bool) {
	if strings.Contains(command, "/") {
		info, err := os.Stat(command)

		return command, err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
	}

	copies := pathCopies(pathEnv, command)
	if len(copies) == 0 {
		return "", false
	}

	return copies[0], true
}

func plural(count int, noun string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, noun)
	}

	if stem, ok := strings.CutSuffix(noun, "y"); ok {
		return fmt.Sprintf("%d %sies", count, stem)
	}

	return fmt.Sprintf("%d %ss", count, noun)
}
