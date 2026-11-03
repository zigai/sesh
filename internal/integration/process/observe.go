package process

import (
	"context"
	"strings"

	"github.com/zigai/sesh/internal/snapshot/model"
)

const (
	MaxEnvironmentKeys          = 256
	MaxEnvironmentKeyLength     = 256
	MaxEnvironmentTotalKeyBytes = 4096
)

type Request struct {
	PID             int
	ExpectedBirth   Identity
	EnvironmentKeys []string
}

type Observation struct {
	PID        int
	Birth      Identity
	Executable []byte

	// ExecutableDeleted is true only when the executable has zero links (Linux) or no longer
	// resolves at its path (macOS); false is not proof that it exists.
	ExecutableDeleted bool

	Arguments        [][]byte
	Directory        []byte
	DirectoryDeleted bool
	Environment      []model.Variable
}

func Observe(ctx context.Context, req Request) (*Observation, error) {
	if err := validateRequest(req); err != nil {
		return nil, wrapError(err, "observe")
	}

	obs, err := observePlatform(ctx, req)
	if err != nil {
		return nil, wrapError(err, "observe")
	}

	return obs, nil
}

func ValidateEnvironmentKeys(keys []string) error {
	if len(keys) > MaxEnvironmentKeys {
		return ErrEnvironmentLimitExceeded
	}

	totalKeyBytes := 0

	for _, key := range keys {
		if !validEnvironmentKey(key) {
			return ErrInvalidVariableName
		}

		if len(key) > MaxEnvironmentKeyLength {
			return ErrEnvironmentLimitExceeded
		}

		totalKeyBytes += len(key)
		if totalKeyBytes > MaxEnvironmentTotalKeyBytes {
			return ErrEnvironmentLimitExceeded
		}
	}

	return nil
}

func validateRequest(req Request) error {
	if req.PID <= 0 {
		return ErrInvalidPID
	}

	if req.ExpectedBirth.IsZero() || req.ExpectedBirth.BootID == "" {
		return ErrBirthRequired
	}

	if !isCanonicalUUID(req.ExpectedBirth.BootID) {
		return ErrMalformedIdentity
	}

	return ValidateEnvironmentKeys(req.EnvironmentKeys)
}

func validEnvironmentKey(key string) bool {
	return len(key) > 0 && !strings.ContainsAny(key, "=\x00")
}
