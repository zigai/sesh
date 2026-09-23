package session

import (
	"errors"
	"unicode/utf8"
)

var ErrInvalidIdentity = errors.New("invalid session identity")

type Identity struct {
	Origin           string `json:"origin"`
	ConnectionID     string `json:"connectionId"`
	Socket           string `json:"socket"`
	RepositorySource string `json:"repositorySource"`
	Name             string `json:"name"`
}

func (i Identity) Validate() error {
	if i.Name == "" {
		return ErrInvalidIdentity
	}

	if !utf8.ValidString(i.Name) ||
		!utf8.ValidString(i.Origin) ||
		!utf8.ValidString(i.ConnectionID) ||
		!utf8.ValidString(i.Socket) ||
		!utf8.ValidString(i.RepositorySource) {
		return ErrInvalidIdentity
	}

	return nil
}
