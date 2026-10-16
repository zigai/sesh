//go:build darwin

package doctor

import "context"

func serviceUnitOf(int) (string, error) {
	return "", errNoServiceUnit
}

func serviceOOMPolicyOf(context.Context, string) (string, error) {
	return "", errNoServiceUnit
}
