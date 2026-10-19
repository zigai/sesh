package config

import (
	"strconv"
	"strings"
	"unicode"
)

const (
	fKeyOffset        = 12
	firstExtendedFKey = 13
	lastExtendedFKey  = 24
	tmuxModifierOrder = "CMS"
)

// TmuxKeyName writes a key the way tmux lists it: modifiers in C-M-S order, ^x as C-x, and F13 to
// F24 as shifted F1 to F12.
func TmuxKeyName(key string) string {
	held, base := splitTmuxModifiers(strings.TrimSpace(key))

	if number, ok := extendedFunctionKey(base); ok {
		held['S'] = true
		base = "F" + strconv.Itoa(number-fKeyOffset)
	}

	var name strings.Builder

	for _, modifier := range tmuxModifierOrder {
		if held[modifier] {
			name.WriteRune(modifier)
			name.WriteString("-")
		}
	}

	name.WriteString(base)

	return name.String()
}

func splitTmuxModifiers(key string) (map[rune]bool, string) {
	held := make(map[rune]bool, len(tmuxModifierOrder))

	for {
		switch {
		case len(key) > 2 && key[1] == '-' && strings.ContainsRune(tmuxModifierOrder, unicode.ToUpper(rune(key[0]))):
			held[unicode.ToUpper(rune(key[0]))] = true
			key = key[2:]
		case len(key) > 1 && key[0] == '^':
			held['C'] = true
			key = key[1:]
		default:
			return held, key
		}
	}
}

func extendedFunctionKey(key string) (int, bool) {
	if len(key) < 2 || (key[0] != 'F' && key[0] != 'f') {
		return 0, false
	}

	number, err := strconv.Atoi(key[1:])

	return number, err == nil && number >= firstExtendedFKey && number <= lastExtendedFKey
}
