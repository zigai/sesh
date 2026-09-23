//go:build darwin

package process

import (
	"context"
	"fmt"
)

func observePlatform(ctx context.Context, _ Request) (*Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("observe canceled: %w", err)
	}

	return nil, ErrUnsupported
}
