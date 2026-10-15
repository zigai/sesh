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

// KeyIdentity drops the tmux socket and snapshot repository, which differ between the live and
// saved forms of one session.
func (i Identity) KeyIdentity() Identity {
	return Identity{Origin: i.Origin, ConnectionID: i.ConnectionID, Socket: "", RepositorySource: "", Name: i.Name}
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
