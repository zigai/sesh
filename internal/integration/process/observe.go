package process

import (
	"context"
	"strings"

	"github.com/zigai/sesh/internal/snapshot/model"
)

const (
	MaxEnvironmentKeys = 256

	MaxEnvironmentKeyLength = 256

	MaxEnvironmentTotalKeyBytes = 4096
)

type Request struct {
	PID int

	ExpectedBirth Identity

	EnvironmentKeys []string
}

type Observation struct {
	PID int

	Birth Identity

	Executable []byte

	// ExecutableDeleted is true only if positive evidence (nlink==0) confirms the
	// executable inode was unlinked from disk.
	ExecutableDeleted bool

	Arguments [][]byte

	Directory []byte

	DirectoryDeleted bool

	Environment []model.Variable
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
		if !isValidEnvironmentKey(key) {
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

func isValidEnvironmentKey(key string) bool {
	return len(key) > 0 && !strings.ContainsAny(key, "=\x00")
}
