//go:build linux || darwin

package engine

const (
	RecoveryExact     RecoveryProfile = ""
	RecoveryPractical RecoveryProfile = "practical"
)

type RecoveryProfile string
