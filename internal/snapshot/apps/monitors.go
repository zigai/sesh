package apps

import (
	"slices"
	"strconv"
	"strings"

	"github.com/zigai/sesh/internal/snapshot/model"
)

var validTopSortFields = map[string]struct{}{
	"PID": {}, "PPID": {}, "UID": {}, "USER": {}, "RUID": {}, "RUSER": {},
	"SUID": {}, "SUSER": {}, "GID": {}, "GROUP": {}, "PGRP": {}, "TTY": {},
	"TPGID": {}, "SID": {}, "PR": {}, "NI": {}, "nTH": {}, "P": {},
	"%CPU": {}, "TIME": {}, "TIME+": {}, "%MEM": {}, "VIRT": {}, "SWAP": {},
	"RES": {}, "CODE": {}, "DATA": {}, "SHR": {}, "nMaj": {}, "nMin": {},
	"nDRT": {}, "S": {}, "COMMAND": {}, "WCHAN": {}, "Flags": {}, "CGROUPS": {},
	"SUPGIDS": {}, "SUPGRPS": {}, "TGID": {}, "OOMa": {}, "OOMs": {}, "ENVIRON": {},
	"vMj": {}, "vMn": {}, "USED": {}, "nsIPC": {}, "nsMNT": {}, "nsNET": {},
	"nsPID": {}, "nsUSER": {}, "nsUTS": {}, "LXC": {}, "RSan": {}, "RSfd": {},
	"RSlk": {}, "RSsh": {}, "CGNAME": {}, "NU": {}, "LOGID": {}, "EXE": {},
	"RSS": {}, "PSS": {}, "PSan": {}, "PSfd": {}, "PSsh": {}, "USS": {},
	"ioR": {}, "ioRop": {}, "ioW": {}, "ioWop": {}, "AGID": {}, "AGNI": {},
	"STARTED": {}, "ELAPSED": {}, "%CUU": {}, "%CUC": {}, "nsCGROUP": {}, "nsTIME": {},
	"CLS": {}, "Docker": {}, "nFD": {},
}

var validHtopSortFields = map[string]struct{}{
	"AUTOGROUP_ID":       {},
	"AUTOGROUP_NICE":     {},
	"CCGROUP":            {},
	"CGROUP":             {},
	"CMAJFLT":            {},
	"CMINFLT":            {},
	"CNCLWB":             {},
	"COMM":               {},
	"CONTAINER":          {},
	"CSTIME":             {},
	"CTXT":               {},
	"CUTIME":             {},
	"CWD":                {},
	"Command":            {},
	"ELAPSED":            {},
	"EXE":                {},
	"GPU_PERCENT":        {},
	"GPU_TIME":           {},
	"IO_PRIORITY":        {},
	"IO_RATE":            {},
	"IO_READ_RATE":       {},
	"IO_WRITE_RATE":      {},
	"ISCONTAINER":        {},
	"MAJFLT":             {},
	"MINFLT":             {},
	"M_DRS":              {},
	"M_EPSS":             {},
	"M_LRS":              {},
	"M_PRIV":             {},
	"M_PSS":              {},
	"M_PSSWP":            {},
	"M_RESIDENT":         {},
	"M_SHARE":            {},
	"M_SWAP":             {},
	"M_TRS":              {},
	"M_VIRT":             {},
	"NICE":               {},
	"NLWP":               {},
	"OOM":                {},
	"PERCENT_CPU":        {},
	"PERCENT_CPU_DELAY":  {},
	"PERCENT_IO_DELAY":   {},
	"PERCENT_MEM":        {},
	"PERCENT_NORM_CPU":   {},
	"PERCENT_SWAP_DELAY": {},
	"PGRP":               {},
	"PID":                {},
	"PPID":               {},
	"PRIORITY":           {},
	"PROCESSOR":          {},
	"RBYTES":             {},
	"RCHAR":              {},
	"SCHEDULERPOLICY":    {},
	"SECATTR":            {},
	"SESSION":            {},
	"STARTTIME":          {},
	"STATE":              {},
	"STIME":              {},
	"ST_UID":             {},
	"SYSCR":              {},
	"SYSCW":              {},
	"TGID":               {},
	"TIME":               {},
	"TPGID":              {},
	"TTY":                {},
	"USER":               {},
	"UTIME":              {},
	"WBYTES":             {},
	"WCHAR":              {},
}

func isValidHtopSort(sortField string) bool {
	_, ok := validHtopSortFields[sortField]

	return ok
}

func appendHtopParam(argv *[]string, args []string, i, nextI int, short, value string) {
	if strings.HasPrefix(args[i], "--") {
		*argv = append(*argv, args[i:nextI+1]...)

		return
	}

	*argv = append(*argv, short, value)
}

func appendBtopParam(argv *[]string, arg, short, long, value string) {
	if strings.HasPrefix(arg, "--") {
		*argv = append(*argv, long, value)

		return
	}

	*argv = append(*argv, short, value)
}

func prepareMonitor(observed model.Launch, name string) (model.Launch, Reason) {
	rawArgs, ok := cleanArguments(observed.Arguments)
	if !ok {
		return observed, ReasonUnsafeArguments
	}

	if len(rawArgs) == 1 {
		return restartLaunch(observed, []string{name}), ReasonMonitorRestart
	}

	var argv []string

	switch name {
	case "top":
		argv, ok = parseTopArguments(name, rawArgs[1:])
	case "htop":
		argv, ok = parseHtopArguments(name, rawArgs[1:])
	case "btop":
		argv, ok = parseBtopArguments(name, rawArgs[1:])
	default:
		return observed, ReasonUnsupportedApplication
	}

	if !ok {
		return observed, ReasonUnsafeArguments
	}

	return restartLaunch(observed, argv), ReasonMonitorRestart
}

func parseTopArguments(name string, args []string) ([]string, bool) {
	argv := []string{name}
	hasEUser := false
	hasAnyUser := false

	for i := 0; i < len(args); {
		nextI, ok := stepTopArgument(args, i, &argv, &hasEUser, &hasAnyUser)
		if !ok {
			return nil, false
		}

		i = nextI
	}

	return argv, true
}

func stepTopArgument(args []string, i int, argv *[]string, hasEUser, hasAnyUser *bool) (int, bool) {
	if matched, nextI := consumeTopToggle(args, i, argv); matched {
		return nextI, true
	}

	if matched, nextI, ok := consumeTopUser(args, i, argv, hasEUser, hasAnyUser); matched {
		if !ok {
			return i, false
		}

		return nextI, true
	}

	if matched, nextI, ok := consumeTopParam(args, i, argv); matched {
		if !ok {
			return i, false
		}

		return nextI, true
	}

	return i, false
}

func consumeTopToggle(args []string, i int, argv *[]string) (bool, int) {
	arg := args[i]
	if isTopToggle(arg) {
		*argv = append(*argv, arg)

		return true, i + 1
	}

	return false, i
}

func consumeTopUser(args []string, i int, argv *[]string, hasEUser, hasAnyUser *bool) (bool, int, bool) {
	if val, nextI, matched, ok := matchFlagValue(args, i, "-u", "--filter-only-euser"); matched {
		if !ok || *hasAnyUser || !isValidTopUser(val) {
			return true, i, false
		}

		*hasEUser = true

		*argv = append(*argv, args[i:nextI+1]...)

		return true, nextI + 1, true
	}

	if val, nextI, matched, ok := matchFlagValue(args, i, "-U", "--filter-any-user"); matched {
		if !ok || *hasEUser || !isValidTopUser(val) {
			return true, i, false
		}

		*hasAnyUser = true

		*argv = append(*argv, args[i:nextI+1]...)

		return true, nextI + 1, true
	}

	return false, i, false
}

func consumeTopParam(args []string, i int, argv *[]string) (bool, int, bool) {
	if val, nextI, matched, ok := matchFlagValue(args, i, "-d", "--delay"); matched {
		if !ok || !isValidTopDelay(val) {
			return true, i, false
		}

		*argv = append(*argv, args[i:nextI+1]...)

		return true, nextI + 1, true
	}

	if val, nextI, matched, ok := matchFlagValue(args, i, "-o", "--sort-override"); matched {
		if !ok || !isValidTopSort(val) {
			return true, i, false
		}

		*argv = append(*argv, args[i:nextI+1]...)

		return true, nextI + 1, true
	}

	if handled, nextI, ok := consumeTopScale(args, i, argv); handled {
		return handled, nextI, ok
	}

	return consumeTopWidth(args, i, argv)
}

func consumeTopScale(args []string, i int, argv *[]string) (bool, int, bool) {
	if val, nextI, matched, ok := matchFlagValue(args, i, "-E", "--scale-summary-mem"); matched {
		if !ok || !isValidTopScaleSummary(val) {
			return true, i, false
		}

		*argv = append(*argv, args[i:nextI+1]...)

		return true, nextI + 1, true
	}

	if val, nextI, matched, ok := matchFlagValue(args, i, "-e", "--scale-task-mem"); matched {
		if !ok || !isValidTopScaleTask(val) {
			return true, i, false
		}

		*argv = append(*argv, args[i:nextI+1]...)

		return true, nextI + 1, true
	}

	return false, i, false
}

func isTopToggle(arg string) bool {
	return arg == "-c" || arg == "--cmdline-toggle" ||
		arg == "-i" || arg == "--idle-toggle" ||
		arg == "-H" || arg == "--threads-show" ||
		arg == "-1" || arg == "--single-cpu-toggle" ||
		arg == "-S" || arg == "--accum-time-toggle" ||
		arg == "-s" || arg == "--secure-mode"
}

func consumeTopWidth(args []string, i int, argv *[]string) (bool, int, bool) {
	arg := args[i]
	if arg == "-w" || arg == "--width" {
		if i+1 < len(args) && isValidTopWidth(args[i+1]) {
			*argv = append(*argv, arg, args[i+1])

			const flagAndValueOffset = 2

			return true, i + flagAndValueOffset, true
		}

		*argv = append(*argv, arg)

		return true, i + 1, true
	}

	if val, nextI, matched, ok := matchFlagValue(args, i, "-w", "--width"); matched {
		if !ok || !isValidTopWidth(val) {
			return true, i, false
		}

		*argv = append(*argv, args[i:nextI+1]...)

		return true, nextI + 1, true
	}

	return false, i, false
}

func isValidTopWidth(width string) bool {
	n, err := strconv.Atoi(width)

	return err == nil && n >= 1 && n <= 512
}

func isValidTopDelay(delay string) bool {
	f, err := strconv.ParseFloat(delay, 64)

	return err == nil && f > 0 && f <= 3600
}

func isValidTopUser(user string) bool {
	target := strings.TrimPrefix(user, "!")

	return isValidUsernameOrUID(target)
}

func isValidTopSort(sortField string) bool {
	field := strings.TrimPrefix(strings.TrimPrefix(sortField, "+"), "-")
	_, ok := validTopSortFields[field]

	return ok
}

func isValidTopScaleSummary(scale string) bool {
	if len(scale) != 1 {
		return false
	}

	return strings.ContainsRune("kmgtpeKMGTPE", rune(scale[0]))
}

func isValidTopScaleTask(scale string) bool {
	if len(scale) != 1 {
		return false
	}

	return strings.ContainsRune("kmgtpKMGTP", rune(scale[0]))
}

func parseHtopArguments(name string, args []string) ([]string, bool) {
	argv := []string{name}

	for i := 0; i < len(args); {
		nextI, ok := stepHtopArgument(args, i, &argv)
		if !ok {
			return nil, false
		}

		i = nextI
	}

	return argv, true
}

func stepHtopArgument(args []string, i int, argv *[]string) (int, bool) {
	if matched, nextI := consumeHtopToggle(args, i, argv); matched {
		return nextI, true
	}

	if matched, nextI, ok := consumeHtopParam(args, i, argv); matched {
		if !ok {
			return i, false
		}

		return nextI, true
	}

	if matched, nextI, ok := consumeHtopView(args, i, argv); matched {
		if !ok {
			return i, false
		}

		return nextI, true
	}

	return i, false
}

func consumeHtopToggle(args []string, i int, argv *[]string) (bool, int) {
	arg := args[i]
	if isHtopToggle(arg) {
		*argv = append(*argv, arg)

		return true, i + 1
	}

	return false, i
}

func consumeHtopParam(args []string, i int, argv *[]string) (bool, int, bool) {
	if val, nextI, matched, ok := matchFlagValue(args, i, "-d", "--delay"); matched {
		if !ok || !isValidHtopDelay(val) {
			return true, i, false
		}

		appendHtopParam(argv, args, i, nextI, "-d", val)

		return true, nextI + 1, true
	}

	if val, nextI, matched, ok := matchFlagValue(args, i, "-F", "--filter"); matched {
		if !ok || !isSafeFilterText(val) {
			return true, i, false
		}

		appendHtopParam(argv, args, i, nextI, "-F", val)

		return true, nextI + 1, true
	}

	return consumeHtopFilterUser(args, i, argv)
}

func consumeHtopFilterUser(args []string, i int, argv *[]string) (bool, int, bool) {
	if val, nextI, matched, ok := matchFlagValue(args, i, "-u", "--user"); matched {
		if !ok || !isValidUsernameOrUID(val) {
			return true, i, false
		}

		appendHtopParam(argv, args, i, nextI, "-u", val)

		return true, nextI + 1, true
	}

	if val, nextI, matched, ok := matchFlagValue(args, i, "-s", "--sort-key"); matched {
		if !ok || !isValidHtopSort(val) {
			return true, i, false
		}

		appendHtopParam(argv, args, i, nextI, "-s", val)

		return true, nextI + 1, true
	}

	return false, i, false
}

func consumeHtopView(args []string, i int, argv *[]string) (bool, int, bool) {
	if handled, nextI, ok := consumeHtopTree(args, i, argv); handled {
		if !ok {
			return true, i, false
		}

		return true, nextI, true
	}

	if handled, nextI, ok := consumeHtopHighlight(args, i, argv); handled {
		if !ok {
			return true, i, false
		}

		return true, nextI, true
	}

	return false, i, false
}

func isHtopToggle(arg string) bool {
	return arg == "-C" || arg == "--no-color" ||
		arg == "-M" || arg == "--no-mouse" ||
		arg == "-U" || arg == "--no-unicode" ||
		arg == "--no-function-bar" ||
		arg == "--no-meters" ||
		arg == "--readonly"
}

func consumeHtopTree(args []string, i int, argv *[]string) (bool, int, bool) {
	arg := args[i]
	if arg == "-t" || arg == "--tree" {
		*argv = append(*argv, arg)

		return true, i + 1, true
	}

	if val, nextI, matched, ok := matchFlagValue(args, i, "-t", "--tree"); matched {
		if !ok || !isValidTreeMode(val) {
			return true, nextI, false
		}

		appendHtopParam(argv, args, i, nextI, "-t", val)

		return true, nextI + 1, true
	}

	return false, i, false
}

func consumeHtopHighlight(args []string, i int, argv *[]string) (bool, int, bool) {
	arg := args[i]
	if arg == "-H" || arg == "--highlight-changes" {
		*argv = append(*argv, arg)

		return true, i + 1, true
	}

	if val, nextI, matched, ok := matchFlagValue(args, i, "-H", "--highlight-changes"); matched {
		if !ok || !isPositiveInt(val) {
			return true, nextI, false
		}

		appendHtopParam(argv, args, i, nextI, "-H", val)

		return true, nextI + 1, true
	}

	return false, i, false
}

func isValidHtopDelay(delay string) bool {
	n, err := strconv.Atoi(delay)

	return err == nil && n > 0 && n <= 36000
}

func parseBtopArguments(name string, args []string) ([]string, bool) {
	argv := []string{name}

	for i := 0; i < len(args); {
		nextI, ok := stepBtopArgument(args, i, &argv)
		if !ok {
			return nil, false
		}

		i = nextI
	}

	return argv, true
}

func stepBtopArgument(args []string, i int, argv *[]string) (int, bool) {
	arg := args[i]
	if isBtopToggle(arg) {
		*argv = append(*argv, arg)

		return i + 1, true
	}

	if val, nextI, matched, ok := matchFlagValue(args, i, "-p", "--preset"); matched {
		if !ok || !isValidPreset(val) {
			return i, false
		}

		appendBtopParam(argv, arg, "-p", "--preset", val)

		return nextI + 1, true
	}

	if val, nextI, matched, ok := matchFlagValue(args, i, "-f", "--filter"); matched {
		if !ok || !isSafeFilterText(val) {
			return i, false
		}

		appendBtopParam(argv, arg, "-f", "--filter", val)

		return nextI + 1, true
	}

	if val, nextI, matched, ok := matchFlagValue(args, i, "-u", "--update"); matched {
		if !ok || !isValidUpdateRate(val) {
			return i, false
		}

		appendBtopParam(argv, arg, "-u", "--update", val)

		return nextI + 1, true
	}

	return i, false
}

func isBtopToggle(arg string) bool {
	return arg == "-l" || arg == "--low-color" ||
		arg == "-t" || arg == "--tty" ||
		arg == "--no-tty" ||
		arg == "--force-utf"
}

func matchFlagValue(args []string, i int, short, long string) (string, int, bool, bool) {
	arg := args[i]

	if arg == short || (long != "" && arg == long) {
		if i+1 >= len(args) {
			return "", i, true, false
		}

		return args[i+1], i + 1, true, true
	}

	if short != "" {
		if rest, ok := strings.CutPrefix(arg, short+"="); ok {
			return rest, i, true, true
		}

		if strings.HasPrefix(arg, short) && len(arg) > len(short) {
			return strings.TrimPrefix(arg, short), i, true, true
		}
	}

	if long != "" {
		if rest, ok := strings.CutPrefix(arg, long+"="); ok {
			return rest, i, true, true
		}
	}

	return "", i, false, false
}

func isPositiveInt(raw string) bool {
	n, err := strconv.Atoi(raw)

	return err == nil && n > 0 && n <= 1000000
}

func isSafeFilterText(text string) bool {
	if text == "" || len(text) > 256 || text[0] == '-' {
		return false
	}

	for i := range len(text) {
		b := text[i]
		if b < 0x20 || b == 0x7f {
			return false
		}
	}

	return true
}

func isValidUsernameOrUID(user string) bool {
	if user == "" || len(user) > 32 {
		return false
	}

	for i := range len(user) {
		if !isUserChar(user[i]) {
			return false
		}
	}

	return true
}

func isUserChar(b byte) bool {
	return isAlphaNum(b) || b == '_' || b == '-' || b == '.'
}

func isValidTreeMode(mode string) bool {
	lower := strings.ToLower(mode)

	return slices.Contains([]string{"classic", "soft", "hard"}, lower)
}

func isValidPreset(preset string) bool {
	if len(preset) != 1 {
		return false
	}

	return preset[0] >= '0' && preset[0] <= '9'
}

func isValidUpdateRate(rate string) bool {
	n, err := strconv.Atoi(rate)

	return err == nil && n >= 100 && n <= 60000
}
