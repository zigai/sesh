package doctor

import (
	"fmt"
	"io"
	"strings"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/doctor"
)

const (
	headerLabelWidth = 10
	statusWidth      = 4
	columnGap        = 2
	fixIndent        = 2 + statusWidth + columnGap
	fixLabel         = "fix: "
	minWrapWidth     = 24
)

var statusColors = map[doctor.Status]string{
	doctor.StatusOK:   "\x1b[32m",
	doctor.StatusInfo: "\x1b[94m",
	doctor.StatusWarn: "\x1b[33m",
	doctor.StatusFail: "\x1b[31m",
	doctor.StatusSkip: "\x1b[90m",
}

// writeReport prints the identity header, one row per check under its group heading, and the
// verdict. A positive width wraps text at word boundaries with a hanging indent; piped output
// keeps each line whole for grep.
func writeReport(output io.Writer, report Report, width int, color bool) error {
	var b strings.Builder

	writeHeader(&b, report)

	nameWidth := 0
	for _, result := range report.Checks {
		nameWidth = max(nameWidth, len(result.Name()))
	}

	messageIndent := 2 + statusWidth + columnGap + nameWidth + columnGap
	group := ""

	for _, result := range report.Checks {
		if result.Group != group {
			group = result.Group
			fmt.Fprintf(&b, "\n%s\n", group)
		}

		message := result.Message
		if result.Needs != "" {
			message = "needs " + result.Needs
		}

		lines := wrap(message, width, messageIndent)
		for _, detail := range result.Details {
			lines = append(lines, wrap(detail, width, messageIndent)...)
		}

		fmt.Fprintf(&b, "  %s%s  %-*s  %s\n", statusWord(result.Status, color), strings.Repeat(" ", statusWidth-len(result.Status)),
			nameWidth, result.Name(), lines[0])

		for _, line := range lines[1:] {
			fmt.Fprintf(&b, "%s%s\n", strings.Repeat(" ", messageIndent), line)
		}

		if result.Fix != nil {
			fix := result.Fix.Command
			if fix == "" {
				fix = result.Fix.Manual
			}

			fixLines := wrap(fix, width, fixIndent+len(fixLabel))
			fmt.Fprintf(&b, "%s%s%s\n", strings.Repeat(" ", fixIndent), fixLabel, fixLines[0])

			for _, line := range fixLines[1:] {
				fmt.Fprintf(&b, "%s%s\n", strings.Repeat(" ", fixIndent+len(fixLabel)), line)
			}
		}
	}

	fmt.Fprintf(&b, "\n%s\n%s\n", verdict(report.Summary), counts(report.Summary))

	_, err := io.WriteString(output, b.String())

	return err
}

func writeHeader(b *strings.Builder, report Report) {
	fmt.Fprintf(b, "sesh %s (%s, %s) %s\n", report.Version, report.Build.Commit, report.Build.Date, report.Build.Target)

	field := func(label string, values ...string) {
		for index, value := range values {
			if index > 0 {
				label = ""
			}

			fmt.Fprintf(b, "  %-*s  %s\n", headerLabelWidth, label, value)
		}
	}

	paths := report.Paths
	configs := make([]string, 0, len(paths.Config))

	for _, file := range paths.Config {
		configs = append(configs, fmt.Sprintf("%s (%s)", configDisplay(file.Path), withSource(configStatus(file.Status), file.Source)))
	}

	field("executable", report.Install.Executable)
	field("config", configs...)
	field("data", location(paths.Data))
	field("state", location(paths.State))
	field("log", location(paths.Log))
	field("snapshots", location(paths.Snapshots))
	field("history", location(paths.History))
	field("socket", location(paths.Socket))

	env := make([]string, 0, len(report.Env))
	for _, variable := range report.Env {
		env = append(env, variable.Name+"="+variable.Value)
	}

	field("env", env...)
}

func configDisplay(path string) string {
	if path == "-" {
		return "stdin"
	}

	return path
}

func configStatus(status string) string {
	switch status {
	case doctor.ConfigNotFound:
		return "not found"
	case doctor.ConfigNotLoaded:
		return "not loaded"
	case doctor.ConfigSkipped:
		return "skipped, --no-config"
	default:
		return status
	}
}

func location(loc Location) string {
	if loc.Source == "" {
		return loc.Path
	}

	return fmt.Sprintf("%s (via %s)", loc.Path, loc.Source)
}

func withSource(status, source string) string {
	if source == "" {
		return status
	}

	return status + ", via " + source
}

func statusWord(status doctor.Status, color bool) string {
	if !color {
		return string(status)
	}

	return statusColors[status] + string(status) + "\x1b[0m"
}

// wrap breaks text to fit a terminal of the given width after indent columns. A width of zero
// means output is not a terminal, which is never wrapped.
func wrap(text string, width, indent int) []string {
	if width <= 0 {
		return []string{text}
	}

	lines := clibase.WrapText(text, max(width-indent, minWrapWidth))
	if len(lines) == 0 {
		return []string{""}
	}

	return lines
}

func verdict(summary doctor.Summary) string {
	failures := countNoun(summary.Fail, "failure")
	warnings := countNoun(summary.Warn, "warning")

	switch {
	case summary.Fail > 0 && summary.Warn > 0:
		return failures + " and " + warnings + " need attention."
	case summary.Fail == 1:
		return failures + " needs attention."
	case summary.Fail > 1:
		return failures + " need attention."
	case summary.Warn > 0:
		return "No failures; " + warnings + "."
	default:
		return "No problems found."
	}
}

func counts(summary doctor.Summary) string {
	parts := make([]string, 0, len(doctor.Statuses))
	for _, status := range doctor.Statuses {
		parts = append(parts, fmt.Sprintf("%d %s", summary.Count(status), status))
	}

	return strings.Join(parts, ", ")
}

func countNoun(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}

	return fmt.Sprintf("%d %ss", count, noun)
}
