//go:build linux || darwin

package agents

var DefaultPalette = map[string]string{
	string(StateWaiting): "fg=#1e1e2e,bg=#ffdd57,bold",
	string(StateFailed):  "fg=#1e1e2e,bg=#ff4da6,bold",
	string(StateUnread):  "fg=#1e1e2e,bg=#6c7086,bold",
}

func StyleFor(palette map[string]string, state State) string {
	if style, ok := palette[string(state)]; ok && style != "" {
		return style
	}

	return DefaultPalette[string(state)]
}
