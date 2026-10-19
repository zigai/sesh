package cli

import (
	"context"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/integration/tmuxserver"
)

const (
	bindingMessagePrefix  = "sesh: "
	bindingMessageLimit   = 240
	bindingMessageTimeout = 2 * time.Second
)

func showBindingFailureMessage(cmd *cobra.Command, err error, stdout, stderr io.Writer) bool {
	if err == nil || clibase.JSONOutputEnabled(cmd) || !invokedByTmuxBinding(cmd, stdout, stderr) {
		return false
	}

	if code := ExitCode(err); code == action.ExitCodeCanceled || code == exitCodePipe {
		return false
	}

	server, openErr := tmuxserver.Open(clibase.ResolveSocket(cmd))
	if openErr != nil {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), bindingMessageTimeout)
	defer cancel()

	// run-shell would print "'…' returned 1" for the failure or, with -E, cover the pane in view
	// mode.
	return showBindingMessage(ctx, cmd, server, bindingFailureMessage(err)) == nil
}

func invokedByTmuxBinding(cmd *cobra.Command, stdout, stderr io.Writer) bool {
	if cmd == nil || cmd.Parent() == nil {
		return false
	}

	bindable := cmd.Annotations[clibase.AnnotationAction] != "" ||
		(cmd.Parent().Name() == "hooks" && cmd.Name() == "run")
	if !bindable {
		return false
	}

	if os.Getenv("TMUX") == "" {
		return false
	}

	return !writerIsTerminal(stdout) && !writerIsTerminal(stderr)
}

func bindingDiscardsReport(cmd *cobra.Command, output *commandBuffer, stdout, stderr io.Writer) bool {
	return !output.streaming && !clibase.JSONOutputEnabled(cmd) && !helpRequested(cmd) && invokedByTmuxBinding(cmd, stdout, stderr)
}

func helpRequested(cmd *cobra.Command) bool {
	help, err := cmd.Flags().GetBool("help")

	return err == nil && help
}

func writerIsTerminal(writer io.Writer) bool {
	file, ok := writer.(fileDescriptor)

	return ok && term.IsTerminal(file.Fd())
}

func bindingFailureMessage(err error) string {
	message := []rune(strings.Join(strings.Fields(clibase.HumanErrorMessage(err)), " "))
	if len(message) > bindingMessageLimit {
		message = append(message[:bindingMessageLimit-1], '…')
	}

	return bindingMessagePrefix + string(message)
}

func showBindingMessage(ctx context.Context, cmd *cobra.Command, server *gotmux.Server, message string) error {
	if flag := cmd.Flags().Lookup("client"); flag != nil && flag.Value.String() != "" {
		client, err := server.ClientHandle(gotmux.ClientName(flag.Value.String()))
		if err == nil {
			if err := client.Message(ctx, message); err == nil {
				return nil
			}
		}
	}

	return server.Message(ctx, message)
}
