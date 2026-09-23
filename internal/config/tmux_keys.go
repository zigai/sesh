package config

import (
	"slices"
	"strconv"
	"strings"
)

const fKeyOffset = 12

func TmuxKeyName(key string) string {
	key = strings.TrimSpace(key)
	parts := strings.Split(key, "-")

	functionKey := parts[len(parts)-1]
	if len(functionKey) < 2 || (functionKey[0] != 'F' && functionKey[0] != 'f') {
		return key
	}

	number, err := strconv.Atoi(functionKey[1:])
	if err != nil || number < 13 || number > 24 {
		return key
	}

	modifiers := parts[:len(parts)-1]
	if !slices.Contains(modifiers, "S") {
		modifiers = append(modifiers, "S")
	}

	return strings.Join(append(modifiers, "F"+strconv.Itoa(number-fKeyOffset)), "-")
}
