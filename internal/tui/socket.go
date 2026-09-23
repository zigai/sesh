package tui

import (
	"path/filepath"
	"strings"
)

func normalizeServerSocket(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	return filepath.Clean(value)
}

func sameServerSocket(left, right string) bool {
	left = normalizeServerSocket(left)
	right = normalizeServerSocket(right)

	if left == "" || right == "" {
		return true
	}

	if filepath.Base(left) != left && filepath.Base(right) != right {
		return left == right
	}

	return filepath.Base(left) == filepath.Base(right)
}

func serverSocketLookupKeys(value string) []string {
	value = normalizeServerSocket(value)
	if value == "" {
		return nil
	}

	base := filepath.Base(value)
	if base == value {
		return []string{value}
	}

	return []string{value, base}
}
