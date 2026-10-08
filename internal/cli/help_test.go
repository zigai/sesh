package cli

import (
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestHelpShowsFlagPlaceholderOnlyBesideFlagName(t *testing.T) {
	stdout, stderr, err := executeCommand(t, "pick", "--help")
	if err != nil || stderr != "" {
		t.Fatalf("pick help failed: %v, stderr %q", err, stderr)
	}

	for _, want := range []string{
		"--socket <path>",
		"--agent-state <path>",
		"--ui <choice>",
		"--config <path>",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("pick help lacks %q:\n%s", want, stdout)
		}
	}

	for line := range strings.SplitSeq(stdout, "\n") {
		flag, description, found := strings.Cut(strings.TrimSpace(line), "  ")
		if !found || !strings.HasPrefix(flag, "-") {
			continue
		}

		if strings.ContainsAny(description, "`\x01") || strings.Contains(description, "(<") {
			t.Fatalf("flag description repeats its placeholder: %q", line)
		}
	}
}

func TestCompletionFlagDescriptionsOmitPlaceholders(t *testing.T) {
	stdout, _, err := executeCommand(t, "__complete", "pick", "--so")
	if err != nil {
		t.Fatalf("complete pick flags: %v", err)
	}

	line, _, _ := strings.Cut(stdout, "\n")
	if line != "--socket\tSocket path of the tmux server [default: the current tmux server, else tmux's default socket]" {
		t.Fatalf("socket completion = %q", line)
	}
}

func TestHelpFlagsKeepNonZeroDefaultsAndBooleanFlagsBare(t *testing.T) {
	stdout, _, err := executeCommand(t, "logs", "--help")
	if err != nil {
		t.Fatalf("logs help failed: %v", err)
	}

	lines := make(map[string]string)

	for line := range strings.SplitSeq(stdout, "\n") {
		flag, description, found := strings.Cut(strings.TrimSpace(line), "  ")
		if found && strings.HasPrefix(flag, "-") {
			lines[flag] = strings.TrimSpace(description)
		}
	}

	for flag, want := range map[string]string{
		"--color <choice>":     "Colorize output [choices: auto, always, never] [default: auto]",
		"-l, --level <choice>": "Minimum log level to display [choices: trace, debug, info, warn, error]",
		"-n, --lines <number>": "Number of most recent log lines to display (0 for all, maximum 100000) [default: 50]",
		"-a, --all":            "Display all historical log lines without limit",
	} {
		if lines[flag] != want {
			t.Fatalf("logs help %q = %q, want %q", flag, lines[flag], want)
		}
	}
}

func TestCommandAndFlagDescriptionsStartWithCapital(t *testing.T) {
	var lowercase []string

	var visit func(cmd *cobra.Command)

	visit = func(cmd *cobra.Command) {
		if startsLowercase(cmd.Short) {
			lowercase = append(lowercase, cmd.CommandPath()+": "+cmd.Short)
		}

		for _, flags := range []*pflag.FlagSet{cmd.LocalNonPersistentFlags(), cmd.PersistentFlags()} {
			flags.VisitAll(func(flag *pflag.Flag) {
				if startsLowercase(flag.Usage) {
					lowercase = append(lowercase, cmd.CommandPath()+" --"+flag.Name+": "+flag.Usage)
				}
			})
		}

		for _, child := range cmd.Commands() {
			visit(child)
		}
	}

	visit(newRootCommand(t.Context()))

	if len(lowercase) > 0 {
		t.Fatalf("descriptions start lowercase:\n%s", strings.Join(lowercase, "\n"))
	}
}

func startsLowercase(text string) bool {
	first, _ := utf8.DecodeRuneInString(text)

	return unicode.IsLower(first)
}

func TestFlagDescriptionsWriteDefaultsInOneForm(t *testing.T) {
	var inconsistent []string

	var visit func(cmd *cobra.Command)

	visit = func(cmd *cobra.Command) {
		for _, flags := range []*pflag.FlagSet{cmd.LocalNonPersistentFlags(), cmd.PersistentFlags()} {
			flags.VisitAll(func(flag *pflag.Flag) {
				if !defaultsUseOneForm(flag.Usage) {
					inconsistent = append(inconsistent, cmd.CommandPath()+" --"+flag.Name+": "+flag.Usage)
				}
			})
		}

		for _, child := range cmd.Commands() {
			visit(child)
		}
	}

	visit(newRootCommand(t.Context()))

	if len(inconsistent) > 0 {
		t.Fatalf("defaults must end the description as \"[default: X]\" and choices must use SetFlagChoices:\n%s", strings.Join(inconsistent, "\n"))
	}
}

func defaultsUseOneForm(usage string) bool {
	lower := strings.ToLower(usage)
	if strings.Contains(lower, "(default") || strings.Contains(lower, "(empty") || strings.Contains(lower, "[choices") {
		return false
	}

	before, after, found := strings.Cut(usage, "[default: ")

	return !found || (!strings.Contains(before, "[default") && strings.HasSuffix(after, "]") && !strings.Contains(after, "[default"))
}

func TestHelpRendersLiteralDefaultsInDescriptionForm(t *testing.T) {
	for _, args := range [][]string{{"pick", "--help"}, {"logs", "--help"}, {"autosave", "final-save", "--help"}} {
		stdout, _, err := executeCommand(t, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}

		if strings.Contains(stdout, "(default") {
			t.Fatalf("%v renders a pflag-style default:\n%s", args, stdout)
		}
	}

	for args, want := range map[string]string{
		"pick":                "Picker layout [choices: auto, popup, inline, fullscreen] [default: auto]",
		"logs":                "[default: 50]",
		"autosave final-save": "[default: 20s]",
		"setup agents":        "Tracker operation [choices: enable, disable, status] [default: enable]",
	} {
		stdout, _, err := executeCommand(t, append(strings.Fields(args), "--help")...)
		if err != nil || !strings.Contains(stdout, want) {
			t.Fatalf("%s help lacks %q: %v\n%s", args, want, err, stdout)
		}
	}
}

func TestCompletionOffersFlagChoices(t *testing.T) {
	for _, test := range []struct {
		args []string
		want []string
	}{
		{args: []string{"__complete", "logs", "--color", ""}, want: []string{"auto", "always", "never"}},
		{args: []string{"__complete", "pick", "--ui", ""}, want: []string{"auto", "popup", "inline", "fullscreen"}},
	} {
		stdout, _, err := executeCommand(t, test.args...)
		if err != nil {
			t.Fatalf("%v: %v", test.args, err)
		}

		got := strings.Split(strings.TrimSpace(stdout), "\n")
		if len(got) < len(test.want) || !slices.Equal(got[:len(test.want)], test.want) {
			t.Fatalf("%v completions = %q, want %q", test.args, got, test.want)
		}
	}
}
