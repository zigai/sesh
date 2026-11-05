package config

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

const (
	SessionOrderFirst  = "first"
	SessionOrderNormal = "normal"
	SessionOrderLast   = "last"
)

var (
	errSessionOrderMatchEmpty      = errors.New("session order match cannot be blank")
	errSessionOrderPositionInvalid = errors.New(
		"session order position must be first, normal, or last",
	)
)

// SessionOrderRule places matching tmux session groups in the Agents popup.
// Match uses case-sensitive [path.Match] syntax; the first matching rule wins.
type SessionOrderRule struct {
	Match    string `toml:"match"`
	Position string `toml:"position"`
}

func validateSessionOrder(rules []SessionOrderRule) error {
	for index, rule := range rules {
		if strings.TrimSpace(rule.Match) == "" {
			return fmt.Errorf(
				"agent_sessions.session_order[%d].match: %w",
				index,
				errSessionOrderMatchEmpty,
			)
		}

		if _, err := path.Match(rule.Match, ""); err != nil {
			return fmt.Errorf(
				"agent_sessions.session_order[%d].match %q: %w",
				index,
				rule.Match,
				err,
			)
		}

		switch rule.Position {
		case SessionOrderFirst, SessionOrderNormal, SessionOrderLast:
		default:
			return fmt.Errorf(
				"agent_sessions.session_order[%d].position %q: %w",
				index,
				rule.Position,
				errSessionOrderPositionInvalid,
			)
		}
	}

	return nil
}
