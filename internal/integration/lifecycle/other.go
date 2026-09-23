//go:build !linux && !darwin

package lifecycle

import "runtime"

func ObserveFacts(bool) Facts {
	return Facts{
		GOOS:               runtime.GOOS,
		Manager:            "none",
		InhibitorAvailable: false,
		ManagedTmuxService: false,
		SameUserManager:    false,
		StopOrderDeclared:  false,
		SaverAfterTmux:     false,
	}
}

func PlatformGaps() []string {
	return []string{"no OS lifecycle integration is available on this platform"}
}
