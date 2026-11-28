//go:build !linux

package autosave

import "context"

func awaitDesktopKeyring(context.Context) bool {
	return true
}
