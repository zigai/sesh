package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/zigai/sesh/internal/cli/clibase"
)

// metavarMarker ends the placeholder flagUsages prepends to a flag's usage.
const metavarMarker = "\x01"

var flagMetavars = map[string]string{
	"config": "<path>", "socket": "<path>", "repository": "<path>", "history": "<path>",
	"lease-root": "<path>", "status-file": "<path>", "state": "<path>", "dir": "<path>",
	"tmux-config": "<path>", "file": "<path>", "output": "<path>", "agent-state": "<path>",
	"generation": "<id>", "operation": "<id>", "target-pane": "<id>", "current-pane": "<id>", "pane": "<id>",
	"max-bytes": "<bytes>", "max-generations": "<count>", "max-count": "<count>", "retention-days": "<days>",
}

func configureCommandHelp(root *cobra.Command) {
	root.SetUsageFunc(writeCommandUsage)
	addHelpFlags(root)
}

// configureCompletionHelp capitalizes the flag description cobra generates for its
// completion commands to match the rest of the command tree.
func configureCompletionHelp(root *cobra.Command) {
	completion, _, err := root.Find([]string{"completion"})
	if err != nil || completion == root {
		return
	}

	for _, shell := range completion.Commands() {
		if flag := shell.Flags().Lookup("no-descriptions"); flag != nil {
			flag.Usage = "Disable completion descriptions"
		}
	}
}

func addHelpFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("help", false, "Show help for this command")

	for _, child := range cmd.Commands() {
		addHelpFlags(child)
	}
}

// writeCommandUsage matches cobra's default usage output except that flag placeholders
// appear only beside the flag name, not repeated in its description.
func writeCommandUsage(cmd *cobra.Command) error {
	var out strings.Builder

	writeUsageHeader(&out, cmd)
	writeUsageCommands(&out, cmd)
	writeUsageFlags(&out, cmd)
	writeUsageHelpTopics(&out, cmd)

	if cmd.HasAvailableSubCommands() {
		fmt.Fprintf(&out, "\n\nUse \"%s [command] --help\" for more information about a command.", cmd.CommandPath())
	}

	out.WriteString("\n")

	_, err := io.WriteString(cmd.OutOrStderr(), out.String())

	return err
}

func writeUsageHeader(out *strings.Builder, cmd *cobra.Command) {
	out.WriteString("Usage:")

	if cmd.Runnable() {
		out.WriteString("\n  ")
		out.WriteString(cmd.UseLine())
	}

	if cmd.HasAvailableSubCommands() {
		fmt.Fprintf(out, "\n  %s [command]", cmd.CommandPath())
	}

	if len(cmd.Aliases) > 0 {
		out.WriteString("\n\nAliases:\n  ")
		out.WriteString(cmd.NameAndAliases())
	}

	if cmd.HasExample() {
		out.WriteString("\n\nExamples:\n")
		out.WriteString(cmd.Example)
	}
}

func writeUsageCommands(out *strings.Builder, cmd *cobra.Command) {
	if !cmd.HasAvailableSubCommands() {
		return
	}

	out.WriteString("\n\nAvailable Commands:")

	for _, child := range cmd.Commands() {
		if child.IsAvailableCommand() || child.Name() == "help" {
			fmt.Fprintf(out, "\n  %-*s %s", child.NamePadding(), child.Name(), child.Short)
		}
	}
}

func writeUsageFlags(out *strings.Builder, cmd *cobra.Command) {
	if cmd.HasAvailableLocalFlags() {
		out.WriteString("\n\nFlags:\n")
		out.WriteString(strings.TrimRight(flagUsages(cmd.LocalFlags()), " \t\n"))
	}

	if cmd.HasAvailableInheritedFlags() {
		out.WriteString("\n\nGlobal Flags:\n")
		out.WriteString(strings.TrimRight(flagUsages(cmd.InheritedFlags()), " \t\n"))
	}
}

func writeUsageHelpTopics(out *strings.Builder, cmd *cobra.Command) {
	if !cmd.HasHelpSubCommands() {
		return
	}

	out.WriteString("\n\nAdditional help topics:")

	for _, child := range cmd.Commands() {
		if child.IsAdditionalHelpTopicCommand() {
			fmt.Fprintf(out, "\n  %-*s %s", child.CommandPathPadding(), child.CommandPath(), child.Short)
		}
	}
}

// flagUsages renders flags with pflag's layout. pflag also keeps a back-quoted placeholder in the
// description, so each copy is marked with metavarMarker and the inline copy is removed.
func flagUsages(flags *pflag.FlagSet) string {
	rendered := pflag.NewFlagSet(flags.Name(), pflag.ContinueOnError)
	rendered.SortFlags = flags.SortFlags

	var inline []string

	flags.VisitAll(func(flag *pflag.Flag) {
		display := *flag
		display.Value = defaultlessValue{Value: flag.Value}
		display.DefValue = ""

		if choices := clibase.FlagChoices(flag); len(choices) > 0 {
			display.Usage += " [choices: " + strings.Join(choices, ", ") + "]"
		}

		if value, ok := flagDefault(flag); ok {
			display.Usage += " [default: " + value + "]"
		}

		if metavar := flagMetavar(flag); metavar != "" {
			display.Usage = "`" + metavar + "`" + metavarMarker + display.Usage
			inline = append(inline, metavar+metavarMarker)
		}

		rendered.AddFlag(&display)
	})

	usages := rendered.FlagUsages()
	for _, duplicate := range inline {
		usages = strings.ReplaceAll(usages, duplicate, "")
	}

	return usages
}

// defaultlessValue hides a flag's concrete value type from pflag so that pflag appends no
// default of its own; flagUsages writes every default as "[default: X]" instead.
type defaultlessValue struct {
	pflag.Value
}

// flagDefault returns the flag's default for help when it differs from the zero value of
// its type.
func flagDefault(flag *pflag.Flag) (string, bool) {
	var zero []string

	switch kind := flag.Value.Type(); {
	case kind == "bool" || kind == "boolfunc":
		zero = []string{"false", ""}
	case kind == "duration":
		zero = []string{"0", "0s", ""}
	case strings.HasPrefix(kind, "int") || strings.HasPrefix(kind, "uint") || strings.HasPrefix(kind, "float") || kind == "count":
		zero = []string{"0", ""}
	case strings.HasSuffix(kind, "Slice") || strings.HasSuffix(kind, "Array"):
		zero = []string{"[]", ""}
	default:
		zero = []string{""}
	}

	if slices.Contains(zero, flag.DefValue) {
		return "", false
	}

	return flag.DefValue, true
}

func flagMetavar(flag *pflag.Flag) string {
	if flag.Value.Type() == "bool" || flag.Value.Type() == "boolfunc" {
		return ""
	}

	if len(clibase.FlagChoices(flag)) > 0 {
		return "<choice>"
	}

	if flag.Name == "session" || flag.Name == "client" {
		if flag.Value.Type() == "stringSlice" {
			return "<id...>"
		}

		return "<name>"
	}

	if metavar, ok := flagMetavars[flag.Name]; ok {
		return metavar
	}

	switch flag.Value.Type() {
	case "duration":
		return "<duration>"
	case "int", "int64", "uint", "uint64":
		return "<number>"
	case "stringSlice":
		return "<string...>"
	default:
		return "<string>"
	}
}
