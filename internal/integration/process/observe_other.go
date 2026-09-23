//go:build !linux && !darwin

package process

import "context"

func observePlatform(ctx context.Context, req Request) (*Observation, error) {
	return nil, ErrUnsupported
}
