//go:build darwin

package doctor

import (
	"context"
	"errors"
)

var errNoServiceUnit = errors.New("service units are not available on this platform")

func serviceUnitOf(int) (string, error) {
	return "", errNoServiceUnit
}

func serviceOOMPolicyOf(context.Context, string) (string, error) {
	return "", errNoServiceUnit
}
