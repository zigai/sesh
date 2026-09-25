package process

import "context"

type ForegroundRequest struct {
	PanePID           int
	ExpectedPaneBirth Identity
	EnvironmentKeys   []string
}

func ObserveForeground(ctx context.Context, req ForegroundRequest) (*Observation, error) {
	request := Request{
		PID:             req.PanePID,
		ExpectedBirth:   req.ExpectedPaneBirth,
		EnvironmentKeys: req.EnvironmentKeys,
	}
	if err := validateRequest(request); err != nil {
		return nil, wrapError(err, "observe_foreground")
	}

	observation, err := observeForegroundPlatform(ctx, req)
	if err != nil {
		return nil, wrapError(err, "observe_foreground")
	}

	return observation, nil
}
