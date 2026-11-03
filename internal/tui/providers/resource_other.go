//go:build !linux

package providers

import (
	"context"
	"time"
)

const ResourceSupported = false

func ReadResourceSample(_ context.Context, _ string, _ time.Time) (ResourceSample, error) {
	return ResourceSample{}, ErrResourceUnsupported
}
