package cli

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var flagMetavars = map[string]string{
	"config": "<path>", "socket": "<path>", "repository": "<path>", "history": "<path>",
	"lease-root": "<path>", "status-file": "<path>", "state": "<path>", "dir": "<path>",
	"tmux-config": "<path>", "file": "<path>", "output": "<path>",
	"generation": "<id>", "operation": "<id>", "target-pane": "<id>", "current-pane": "<id>", "pane": "<id>",
	"color": "<auto|always|never>", "level": "<trace|debug|info|warn|error>",
	"max-bytes": "<bytes>", "max-generations": "<count>", "max-count": "<count>", "retention-days": "<days>",
}

func configureCommandHelp(cmd *cobra.Command) {
	cmd.Flags().Bool("help", false, "Show help for this command")
	cmd.Flags().VisitAll(configureFlagMetavar)
	cmd.PersistentFlags().VisitAll(configureFlagMetavar)

	for _, child := range cmd.Commands() {
		configureCommandHelp(child)
	}
}

func configureFlagMetavar(flag *pflag.Flag) {
	if flag.Value.Type() == "bool" || flag.Value.Type() == "boolfunc" {
		return
	}

	metavar := flagMetavar(flag)
	flag.Usage = strings.TrimSpace(flag.Usage) + " (`" + metavar + "`)"
}

func flagMetavar(flag *pflag.Flag) string {
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
