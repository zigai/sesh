//go:build linux || darwin

package doctor

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

const (
	maxServiceUnitKiB   = 64
	maxServiceUnitBytes = maxServiceUnitKiB << 10
	execPrefixes        = "-@:+!|"
	// argv0Override is the position of the argv[0] word that the "@" Exec prefix adds after the
	// executable path.
	argv0Override = 1
)

var errUnitTooLarge = errors.New("unit file is too large")

var execKeys = []string{"ExecStart", "ExecStartPre", "ExecStartPost", "ExecReload", "ExecStop", "ExecStopPost", "ExecCondition"}

// unitCommand is one sesh command line of a unit and the file and line that set it.
type unitCommand struct {
	args   []string
	source string
}

type unitLine struct {
	text   string
	number int
}

// execWords splits an Exec value into words, honoring single and double quotes and backslash
// escapes.
type execWords struct {
	words   []string
	current strings.Builder
	quote   rune
	inWord  bool
	escaped bool
}

// checkServiceUnits fails when a systemd user unit runs a sesh command line that this sesh
// rejects, such as a renamed command or a removed flag left behind by an upgrade. Such a unit
// fails on every start or stop, which silently disables the saves and restores it was written
// for. It reads units the way systemd resolves them: the first unit file of a name along the
// search path, links followed, with its drop-ins applied.
func (s *Service) checkServiceUnits() []Result {
	if runtime.GOOS != "linux" || s.opts.ServiceDirs == nil {
		return nil
	}
	if s.opts.ValidateCommand == nil {
		return []Result{newResult(CheckServiceUnits, StatusSkip, "Service command validation is unavailable")}
	}

	units, unreadable := s.unitFiles()

	checked := 0

	var stale []string

	for _, name := range slices.Sorted(maps.Keys(units)) {
		commands, err := s.unitCommands(name, units[name])
		if err != nil {
			unreadable = append(unreadable, err.Error())

			continue
		}

		if len(commands) == 0 {
			continue
		}

		checked++

		for _, command := range commands {
			if cause := s.opts.ValidateCommand(command.args); cause != nil {
				stale = append(stale, fmt.Sprintf("%s: %v (%s)", name, cause, command.source))

				break
			}
		}
	}

	return s.serviceUnitsResult(checked, stale, unreadable)
}

func (s *Service) serviceUnitsResult(checked int, stale, unreadable []string) []Result {
	if len(stale) > 0 {
		return []Result{newResult(CheckServiceUnits, StatusFail,
			plural(len(stale), "unit")+" run sesh command lines this sesh rejects, so they fail on every start or stop").
			withReason(ReasonStale).
			withDetails(stale...).
			withDetails(unreadable...).
			withManual("Regenerate sesh-managed units with 'sesh setup services --apply' (add the flags you set them up with), " +
				"update hand-maintained units to the current commands, then run 'systemctl --user daemon-reload'")}
	}

	if len(unreadable) > 0 {
		return []Result{newResult(CheckServiceUnits, StatusWarn, plural(len(unreadable), "unit location")+" could not be read, so their sesh commands were not checked").
			withReason(ReasonInspectionFailed).
			withDetails(unreadable...).
			withManual("Make the listed files and directories readable by you")}
	}

	if checked == 0 {
		return nil
	}

	return []Result{newResult(CheckServiceUnits, StatusOK, plural(checked, "unit")+" run sesh command lines this sesh accepts")}
}

// unitFiles maps each service unit name to the file systemd loads for it: the first along the
// search path, with links followed. Masked and dangling units are left out.
func (s *Service) unitFiles() (map[string]string, []string) {
	units := make(map[string]string)
	shadowed := make(map[string]bool)

	var unreadable []string

	for _, dir := range s.opts.ServiceDirs {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			unreadable = append(unreadable, fmt.Sprintf("%s: %v", DisplayPath(dir), err))

			continue
		}

		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".service") || shadowed[name] {
				continue
			}

			shadowed[name] = true

			path := filepath.Join(dir, name)
			info, err := os.Stat(path)
			switch {
			case errors.Is(err, fs.ErrNotExist):
				continue
			case err != nil:
				unreadable = append(unreadable, fmt.Sprintf("%s: %v", DisplayPath(path), err))
			case info.Mode().IsRegular():
				units[name] = path
			}
		}
	}

	return units, unreadable
}

// unitCommands returns the sesh command lines a unit runs after its drop-ins apply. An empty
// Exec assignment clears the commands set before it, as in systemd.
func (s *Service) unitCommands(name, path string) ([]unitCommand, error) {
	execs := make(map[string][]unitCommand)
	dropIns, err := s.dropIns(name)
	if err != nil {
		return nil, err
	}

	for _, file := range append([]string{path}, dropIns...) {
		lines, err := readUnitLines(file)
		if err != nil {
			return nil, err
		}

		for _, line := range lines {
			key, value, ok := strings.Cut(line.text, "=")
			key = strings.TrimSpace(key)

			if !ok || !slices.Contains(execKeys, key) {
				continue
			}

			argv := splitExecLine(value)
			if len(argv) == 0 {
				execs[key] = nil

				continue
			}

			if s.isSeshProgram(argv[0]) {
				execs[key] = append(execs[key], unitCommand{args: argv[1:], source: fmt.Sprintf("%s:%d", DisplayPath(file), line.number)})
			}
		}
	}

	var commands []unitCommand
	for _, key := range execKeys {
		commands = append(commands, execs[key]...)
	}

	return commands, nil
}

// dropIns lists the drop-in files of a unit in the order systemd applies them: by file name,
// with a name in an earlier search directory shadowing the same name in a later one.
func (s *Service) dropIns(unit string) ([]string, error) {
	byName := make(map[string]string)
	var inspectionErr error

	for _, dir := range s.opts.ServiceDirs {
		path := filepath.Join(dir, unit+".d")
		entries, err := os.ReadDir(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			inspectionErr = errors.Join(inspectionErr, fmt.Errorf("%s: %w", DisplayPath(path), err))
			continue
		}

		for _, entry := range entries {
			name := entry.Name()
			if _, shadowed := byName[name]; shadowed || !strings.HasSuffix(name, ".conf") {
				continue
			}

			byName[name] = filepath.Join(dir, unit+".d", name)
		}
	}

	files := make([]string, 0, len(byName))
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		files = append(files, byName[name])
	}

	return files, inspectionErr
}

func readUnitLines(path string) ([]unitLine, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", DisplayPath(path), err)
	}

	data, readErr := io.ReadAll(io.LimitReader(file, maxServiceUnitBytes+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, fmt.Errorf("%s: %w", DisplayPath(path), err)
	}

	if len(data) > maxServiceUnitBytes {
		return nil, fmt.Errorf("%s: %w, over %d KiB", DisplayPath(path), errUnitTooLarge, maxServiceUnitKiB)
	}

	return unitLines(data), nil
}

func (s *Service) isSeshProgram(program string) bool {
	return filepath.Base(program) == programName || (s.opts.Executable != "" && program == s.opts.Executable)
}

// unitLines returns the logical lines of a unit file with the number of the line each starts on:
// comments dropped and lines ending in a backslash joined with the next one.
func unitLines(data []byte) []unitLine {
	var (
		lines   []unitLine
		pending strings.Builder
		start   int
	)

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, maxServiceUnitBytes), maxServiceUnitBytes)

	for number := 1; scanner.Scan(); number++ {
		line := strings.TrimSpace(scanner.Text())
		if pending.Len() == 0 && (strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";")) {
			continue
		}

		if pending.Len() == 0 {
			start = number
		}

		if continued, ok := strings.CutSuffix(line, `\`); ok {
			pending.WriteString(continued)
			pending.WriteString(" ")

			continue
		}

		pending.WriteString(line)
		lines = append(lines, unitLine{text: pending.String(), number: start})
		pending.Reset()
	}

	if pending.Len() > 0 {
		lines = append(lines, unitLine{text: pending.String(), number: start})
	}

	return lines
}

// splitExecLine splits a systemd Exec value into the executable and its arguments. It removes the
// special executable prefixes, the argv[0] override that "@" introduces, quotes and the escapes
// sesh setup writes ("%%", "$$" and backslashes).
func splitExecLine(value string) []string {
	value = strings.TrimSpace(value)
	prefix := value[:len(value)-len(strings.TrimLeft(value, execPrefixes))]

	var words execWords
	for _, r := range value[len(prefix):] {
		words.add(r)
	}

	words.endWord()

	unescape := strings.NewReplacer("%%", "%", "$$", "$")
	for index, word := range words.words {
		words.words[index] = unescape.Replace(word)
	}

	if strings.Contains(prefix, "@") && len(words.words) > argv0Override {
		return slices.Delete(words.words, argv0Override, argv0Override+1)
	}

	return words.words
}

func (w *execWords) add(r rune) {
	switch {
	case w.escaped:
		w.escaped = false
		w.write(r)
	case r == '\\':
		w.escaped, w.inWord = true, true
	case w.quote != 0:
		w.addQuoted(r)
	case r == '"' || r == '\'':
		w.quote, w.inWord = r, true
	case r == ' ' || r == '\t':
		w.endWord()
	default:
		w.write(r)
	}
}

func (w *execWords) addQuoted(r rune) {
	if r == w.quote {
		w.quote = 0

		return
	}

	w.write(r)
}

func (w *execWords) write(r rune) {
	w.current.WriteRune(r)
	w.inWord = true
}

func (w *execWords) endWord() {
	if !w.inWord {
		return
	}

	w.words = append(w.words, w.current.String())
	w.current.Reset()
	w.inWord = false
}
