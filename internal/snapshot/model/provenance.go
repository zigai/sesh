package model

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
)

const EndpointScopeLength = sha256.Size * 2

func EndpointScope(socketPath string) (string, error) {
	if !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath {
		return "", ErrInvalid
	}

	digest := sha256.Sum256([]byte(socketPath))

	return hex.EncodeToString(digest[:]), nil
}

func validEndpointScope(scope string) bool {
	if scope == "" {
		return true
	}

	if len(scope) != EndpointScopeLength {
		return false
	}

	decoded, err := hex.DecodeString(scope)

	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == scope
}
