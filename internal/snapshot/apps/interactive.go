package apps

import (
	"slices"
	"strings"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func isGitTUI(name string) bool {
	return slices.Contains([]string{"lazygit", "tig"}, name)
}

func isFileManager(name string) bool {
	return slices.Contains([]string{"ranger", "yazi"}, name)
}

func isPager(name string) bool {
	return slices.Contains([]string{"less", "man"}, name)
}

func prepareGitTUI(observed model.Launch, name string) (model.Launch, Reason) {
	rawArgs, ok := cleanArguments(observed.Arguments)
	if !ok {
		return observed, ReasonUnsafeArguments
	}

	if len(rawArgs) == 1 {
		return restartLaunch(observed, []string{name}), ReasonGitTUIRestart
	}

	var argv []string

	switch name {
	case "lazygit":
		argv, ok = parseLazygitArguments(rawArgs[1:])
	case "tig":
		argv, ok = parseTigArguments(rawArgs[1:])
	default:
		return observed, ReasonUnsupportedApplication
	}

	if !ok {
		return observed, ReasonUnsafeArguments
	}

	return restartLaunch(observed, argv), ReasonGitTUIRestart
}

func parseLazygitArguments(args []string) ([]string, bool) {
	if len(args) == 1 && slices.Contains([]string{"status", "branch", "log", "stash"}, args[0]) {
		return []string{"lazygit", args[0]}, true
	}

	argv := []string{"lazygit"}

	for i := 0; i < len(args); {
		nextI, ok := stepLazygitArgument(args, i, &argv)
		if !ok {
			return nil, false
		}

		i = nextI
	}

	return argv, true
}

func stepLazygitArgument(args []string, i int, argv *[]string) (int, bool) {
	if val, nextI, matched, ok := matchFlagValue(args, i, "-p", "--path"); matched {
		if !ok || !isSafePath(val) || val[0] == '-' {
			return i, false
		}

		*argv = append(*argv, args[i:nextI+1]...)

		return nextI + 1, true
	}

	if val, nextI, matched, ok := matchFlagValue(args, i, "-f", "--filter"); matched {
		if !ok || !isSafePath(val) || val[0] == '-' {
			return i, false
		}

		*argv = append(*argv, args[i:nextI+1]...)

		return nextI + 1, true
	}

	return i, false
}

func parseTigArguments(args []string) ([]string, bool) {
	argv := []string{"tig"}
	validSubviews := []string{"status", "log", "diff", "reflog", "show", "blame"}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--all" {
			argv = append(argv, arg)

			continue
		}

		if slices.Contains(validSubviews, arg) {
			argv = append(argv, arg)
			if arg == "blame" {
				i++
				if i >= len(args) || !isSafePath(args[i]) || args[i][0] == '-' {
					return nil, false
				}

				argv = append(argv, args[i])
			}

			continue
		}

		return nil, false
	}

	return argv, true
}

func prepareFileManager(observed model.Launch, name string) (model.Launch, Reason) {
	rawArgs, ok := cleanArguments(observed.Arguments)
	if !ok {
		return observed, ReasonUnsafeArguments
	}

	if len(rawArgs) == 1 {
		return restartLaunch(observed, []string{name}), ReasonFileManagerRestart
	}

	var argv []string

	switch name {
	case "yazi":
		argv, ok = parseYaziArguments(rawArgs[1:])
	case "ranger":
		argv, ok = parseRangerArguments(rawArgs[1:])
	default:
		return observed, ReasonUnsupportedApplication
	}

	if !ok {
		return observed, ReasonUnsafeArguments
	}

	return restartLaunch(observed, argv), ReasonFileManagerRestart
}

func parseYaziArguments(args []string) ([]string, bool) {
	if len(args) == 0 {
		return []string{"yazi"}, true
	}

	yaziSubcommands := []string{"select", "pub", "sub", "init"}
	if slices.Contains(yaziSubcommands, args[0]) {
		return nil, false
	}

	if args[0] == "--" {
		entries := args[1:]
		if len(entries) == 0 {
			return nil, false
		}

		for _, entry := range entries {
			if !isSafePath(entry) {
				return nil, false
			}
		}

		argv := append([]string{"yazi", "--"}, entries...)

		return argv, true
	}

	for _, entry := range args {
		if !isSafePath(entry) || entry[0] == '-' {
			return nil, false
		}
	}

	argv := append([]string{"yazi", "--"}, args...)

	return argv, true
}

func parseRangerArguments(args []string) ([]string, bool) {
	if len(args) == 0 {
		return []string{"ranger"}, true
	}

	if len(args) == 1 && isSafePath(args[0]) && args[0][0] != '-' {
		return []string{"ranger", "--", args[0]}, true
	}

	if len(args) == 2 && args[0] == "--" && isSafePath(args[1]) {
		return []string{"ranger", "--", args[1]}, true
	}

	return nil, false
}

func preparePager(observed model.Launch, name string) (model.Launch, Reason) {
	rawArgs, ok := cleanArguments(observed.Arguments)
	if !ok {
		return observed, ReasonUnsafeArguments
	}

	if len(rawArgs) <= 1 {
		return observed, ReasonUnsafeArguments
	}

	var argv []string

	switch name {
	case "less":
		argv, ok = parseLessArguments(rawArgs[1:])
	case "man":
		argv, ok = parseManArguments(rawArgs[1:])
	default:
		return observed, ReasonUnsupportedApplication
	}

	if !ok {
		return observed, ReasonUnsafeArguments
	}

	return restartLaunch(observed, argv), ReasonPagerRestart
}

func parseLessArguments(args []string) ([]string, bool) {
	if len(args) == 0 {
		return nil, false
	}

	flags := make([]string, 0, len(args))
	files := make([]string, 0, len(args))
	endOfOptions := false

	for _, arg := range args {
		if !classifyLessArg(arg, &endOfOptions, &flags, &files) {
			return nil, false
		}
	}

	if len(files) == 0 {
		return nil, false
	}

	argv := append([]string{"less"}, flags...)
	argv = append(argv, "--")
	argv = append(argv, files...)

	return argv, true
}

func classifyLessArg(arg string, endOfOptions *bool, flags, files *[]string) bool {
	if !*endOfOptions && arg == "--" {
		*endOfOptions = true

		return true
	}

	if !*endOfOptions && strings.HasPrefix(arg, "-") {
		if !isValidLessFlag(arg) {
			return false
		}

		*flags = append(*flags, arg)

		return true
	}

	if !*endOfOptions && strings.HasPrefix(arg, "+") {
		return false
	}

	if arg == "-" || !isSafePath(arg) {
		return false
	}

	*files = append(*files, arg)

	return true
}

func parseManArguments(args []string) ([]string, bool) {
	if len(args) == 0 || len(args) > 2 {
		return nil, false
	}

	if len(args) == 1 {
		page := args[0]
		if !isValidManPage(page) {
			return nil, false
		}

		return []string{"man", page}, true
	}

	section := args[0]
	page := args[1]

	if !isValidManSection(section) || !isValidManPage(page) {
		return nil, false
	}

	return []string{"man", section, page}, true
}

func isValidLessFlag(flag string) bool {
	if len(flag) < 2 || flag[0] != '-' {
		return false
	}

	for i := range len(flag) - 1 {
		b := flag[1+i]
		if !slices.Contains([]byte{'R', 'N', 'S', 'F', 'X', 'i', 'M', 'I'}, b) {
			return false
		}
	}

	return true
}

func isValidManSection(sec string) bool {
	if len(sec) < 1 || len(sec) > 4 {
		return false
	}

	if sec[0] < '1' || sec[0] > '9' {
		return false
	}

	for i := 1; i < len(sec); i++ {
		if !isAlpha(sec[i]) {
			return false
		}
	}

	return true
}

func isValidManPage(page string) bool {
	if page == "" || len(page) > 64 || page[0] == '-' {
		return false
	}

	for i := range len(page) {
		if !isManPageChar(page[i]) {
			return false
		}
	}

	return true
}

func isManPageChar(b byte) bool {
	return isAlphaNum(b) || b == '_' || b == '-' || b == '.' || b == ':'
}

func isSafePath(path string) bool {
	if path == "" || len(path) > maxArgumentBytes {
		return false
	}

	for i := range len(path) {
		b := path[i]
		if b < 0x20 || b == 0x7f {
			return false
		}
	}

	return true
}
