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

	"github.com/zigai/sesh/internal/cli/clibase"
	integration "github.com/zigai/sesh/internal/integration/tmux"
)

const (
	bindingMessagePrefix  = "sesh: "
	bindingMessageLimit   = 240
	bindingMessageTimeout = 2 * time.Second
)

// reportBindingFailure shows a failed action on the invoking tmux client instead of leaving
// tmux to print "'…' returned 1". tmux run-shell discards stderr, so without this the reason
// is lost. It reports whether the failure reached tmux; the caller then exits successfully.
func reportBindingFailure(cmd *cobra.Command, err error, stdout, stderr io.Writer) bool {
	if err == nil || jsonOutputEnabled(cmd) || !invokedByTmuxBinding(cmd, stdout, stderr) {
		return false
	}

	if code := ExitCode(err); code == exitCodeCancel || code == exitCodePipe {
		return false
	}

	server, openErr := integration.Open(clibase.ResolveSocket(cmd))
	if openErr != nil {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), bindingMessageTimeout)
	defer cancel()

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

func writerIsTerminal(writer io.Writer) bool {
	file, ok := writer.(fileDescriptor)

	return ok && term.IsTerminal(file.Fd())
}

func bindingFailureMessage(err error) string {
	message := []rune(strings.Join(strings.Fields(humanErrorMessage(err)), " "))
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
