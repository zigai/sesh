//go:build !linux && !darwin

package process

import "context"

func ReadIdentity(ctx context.Context, pid int) (Identity, error) {
	if err := ctx.Err(); err != nil {
		return Identity{BootID: "", StartTime: 0}, wrapError(err, "read_identity")
	}

	if pid <= 0 {
		return Identity{BootID: "", StartTime: 0}, wrapError(ErrInvalidPID, "read_identity")
	}

	return Identity{BootID: "", StartTime: 0}, wrapError(ErrUnsupported, "read_identity")
}
