//go:build linux || darwin

package doctor

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	maxServiceUnits     = 256
	maxServiceUnitBytes = 64 << 10
	execPrefixes        = "-@:+!|"
	staleUnitsShown     = 3
	// argv0Override is the position of the argv[0] word that the "@" Exec prefix adds after the
	// executable path.
	argv0Override = 1
)

var execKeys = []string{"ExecStart", "ExecStartPre", "ExecStartPost", "ExecReload", "ExecStop", "ExecStopPost", "ExecCondition"}

// staleUnit is a service unit with a sesh command line this sesh no longer accepts.
type staleUnit struct {
	name  string
	cause error
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

// checkServiceCommands warns when a systemd user unit runs a sesh command line that this sesh
// rejects, such as a renamed command or a removed flag left behind by an upgrade. Such a unit
// fails on every start or stop, which silently disables the saves and restores it was written
// for. It covers sesh-managed and hand-maintained units alike and reports nothing when no unit
// runs sesh.
func (s *Service) checkServiceCommands() []Result {
	if s.serviceDir == "" || s.validateCommand == nil {
		return nil
	}

	entries, err := os.ReadDir(s.serviceDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return []Result{{
			Check:   CheckServiceCommands,
			Status:  StatusWarning,
			Reason:  ReasonInspectionFailed,
			Message: "service unit directory could not be read; sesh command lines in units were not checked",
			Fix:     "Check the permissions of " + s.serviceDir,
		}}
	}

	checked := 0

	var stale []staleUnit

	for _, entry := range entries[:min(len(entries), maxServiceUnits)] {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".service") {
			continue
		}

		commands, err := s.unitSeshCommands(filepath.Join(s.serviceDir, entry.Name()))
		if err != nil || len(commands) == 0 {
			continue
		}

		checked++

		for _, args := range commands {
			if cause := s.validateCommand(args); cause != nil {
				stale = append(stale, staleUnit{name: entry.Name(), cause: cause})

				break
			}
		}
	}

	return serviceCommandsResult(checked, stale)
}

func serviceCommandsResult(checked int, stale []staleUnit) []Result {
	if checked == 0 {
		return nil
	}

	if len(stale) == 0 {
		return []Result{{
			Check:   CheckServiceCommands,
			Status:  StatusOK,
			Reason:  "",
			Message: fmt.Sprintf("sesh command lines in %d service units are accepted by this sesh", checked),
			Fix:     "",
		}}
	}

	names := make([]string, 0, len(stale))
	for _, unit := range stale {
		names = append(names, unit.name)
	}

	shown := names[:min(len(names), staleUnitsShown)]

	message := fmt.Sprintf("%d service units run sesh command lines this sesh rejects, so they fail on every start or stop: %s (%s: %v)",
		len(stale), strings.Join(shown, ", "), stale[0].name, stale[0].cause)
	if len(names) > len(shown) {
		message = fmt.Sprintf("%s, and %d more", message, len(names)-len(shown))
	}

	return []Result{{
		Check:   CheckServiceCommands,
		Status:  StatusError,
		Reason:  ReasonStale,
		Message: message,
		Fix: "Regenerate sesh-managed units with 'sesh setup services --apply' (add the flags you set them up with), " +
			"update hand-maintained units to the current commands, then run 'systemctl --user daemon-reload'",
	}}
}

// unitSeshCommands returns the arguments of every Exec line in a unit file whose program is sesh.
func (s *Service) unitSeshCommands(path string) ([][]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open service unit: %w", err)
	}

	data, readErr := io.ReadAll(io.LimitReader(file, maxServiceUnitBytes+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, err
	}

	if len(data) > maxServiceUnitBytes {
		return nil, nil
	}

	var commands [][]string

	for _, line := range unitLines(data) {
		key, value, ok := strings.Cut(line, "=")
		if !ok || !slices.Contains(execKeys, strings.TrimSpace(key)) {
			continue
		}

		argv := splitExecLine(value)
		if len(argv) == 0 || !s.isSeshProgram(argv[0]) {
			continue
		}

		commands = append(commands, argv[1:])
	}

	return commands, nil
}

func (s *Service) isSeshProgram(program string) bool {
	return filepath.Base(program) == "sesh" || (s.executable != "" && program == s.executable)
}

// unitLines returns the logical lines of a unit file: comments dropped and lines ending in a
// backslash joined with the next one.
func unitLines(data []byte) []string {
	var (
		lines   []string
		pending strings.Builder
	)

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, maxServiceUnitBytes), maxServiceUnitBytes)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if pending.Len() == 0 && (strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";")) {
			continue
		}

		if continued, ok := strings.CutSuffix(line, `\`); ok {
			pending.WriteString(continued)
			pending.WriteString(" ")

			continue
		}

		pending.WriteString(line)
		lines = append(lines, pending.String())
		pending.Reset()
	}

	if pending.Len() > 0 {
		lines = append(lines, pending.String())
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
