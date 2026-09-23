package tui

func deletePathNodeBackward(value string, cursor int) (string, int) {
	runes := []rune(value)
	if cursor <= 0 || len(runes) == 0 {
		return value, 0
	}

	if cursor > len(runes) {
		cursor = len(runes)
	}

	start := cursor
	for start > 0 && isPathSeparator(runes[start-1]) {
		if start == 1 {
			break
		}

		start--
	}

	for start > 0 && !isPathSeparator(runes[start-1]) {
		start--
	}

	next := append([]rune{}, runes[:start]...)
	next = append(next, runes[cursor:]...)

	return string(next), start
}

func isPathSeparator(r rune) bool {
	return r == '/' || r == '\\'
}
