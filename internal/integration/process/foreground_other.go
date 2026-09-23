//go:build !linux

package process

import "context"

func observeForegroundPlatform(ctx context.Context, req ForegroundRequest) (*Observation, error) {
	return observePlatform(ctx, Request{
		PID:             req.PanePID,
		ExpectedBirth:   req.ExpectedPaneBirth,
		EnvironmentKeys: req.EnvironmentKeys,
	})
}
