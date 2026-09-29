//go:build !linux

package tmux

// serverBinary resolves tmux through PATH where procfs cannot identify the server's executable.
func serverBinary(string) string {
	return ""
}
