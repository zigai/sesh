//go:build !linux

package tmuxserver

func serverBinary(string) string {
	return ""
}
