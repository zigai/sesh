package config_test

import "testing"

func assertEqual[T comparable](t *testing.T, name string, got, want T) {
	t.Helper()

	if got != want {
		t.Errorf("%s mismatch: got %v, want %v", name, got, want)
	}
}
