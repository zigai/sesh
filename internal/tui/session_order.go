package tui

import (
	"cmp"
	"path"
	"slices"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

const (
	sessionOrderFirst sessionOrderPlacement = iota
	sessionOrderNormal
	sessionOrderLast
)

type sessionOrderPlacement uint8

type sessionOrderKey struct {
	placement sessionOrderPlacement
	rule      int
}

// Keep agent-bearing sessions above empty sessions. Apply placement rules within
// each section, preserving complete agent groups and the fallback ordering.
func sortAgentsModeSessions(rows []session.Session, rules []config.SessionOrderRule) {
	if len(rules) == 0 {
		slices.SortStableFunc(rows, compareAgentsModeSessions)
		return
	}

	keys := make(map[string]sessionOrderKey, len(rows))
	for _, row := range rows {
		if _, found := keys[row.Name]; !found {
			keys[row.Name] = sessionOrderForName(row.Name, rules)
		}
	}

	slices.SortStableFunc(rows, func(left session.Session, right session.Session) int {
		if (len(left.Agents) > 0) != (len(right.Agents) > 0) {
			return compareAgentsModeSessions(left, right)
		}

		leftKey, rightKey := keys[left.Name], keys[right.Name]
		if diff := cmp.Compare(leftKey.placement, rightKey.placement); diff != 0 {
			return diff
		}

		if diff := cmp.Compare(leftKey.rule, rightKey.rule); diff != 0 {
			return diff
		}

		return compareAgentsModeSessions(left, right)
	})
}

func sessionOrderForName(name string, rules []config.SessionOrderRule) sessionOrderKey {
	for index, rule := range rules {
		matched, err := path.Match(rule.Match, name)
		if err != nil || !matched {
			// Malformed patterns are rejected when configuration is loaded.
			continue
		}

		switch rule.Position {
		case config.SessionOrderFirst:
			return sessionOrderKey{placement: sessionOrderFirst, rule: index}
		case config.SessionOrderNormal:
			return sessionOrderKey{placement: sessionOrderNormal, rule: index}
		case config.SessionOrderLast:
			return sessionOrderKey{placement: sessionOrderLast, rule: index}
		default:
			continue
		}
	}

	return sessionOrderKey{placement: sessionOrderNormal, rule: len(rules)}
}
