package tui

import (
	"slices"
	"strings"
	"unicode"

	"github.com/sahilm/fuzzy"

	"github.com/zigai/sesh/internal/session"
)

const minFuzzyQueryLength = 5

type sessionNameSource []session.Session

func (m *Model) filterSessions() {
	query := m.textInput.Value()
	if query == "" {
		m.filtered = m.displaySessions
		m.normalizeFilteredCursor()

		return
	}

	m.initialAgentSelectionPending = false

	m.filtered = m.filterSessionsWithQuery(query, m.displaySessions)
	m.normalizeFilteredCursor()
}

func (m *Model) normalizeFilteredCursor() {
	if len(m.filtered) == 0 {
		m.cursor = 0
		m.agentCursor = parentAgentIndex
		m.listTop = 0

		return
	}

	if m.cursor >= len(m.filtered) {
		m.cursor = 0
	}

	m.normalizeFilteredAgentCursor()

	if m.cursor == 0 && m.listViewportPolicy == listViewportFollowSelection {
		m.listTop = 0
	}
}

func (m *Model) normalizeFilteredAgentCursor() {
	if !m.displayMode.tree() {
		m.agentCursor = parentAgentIndex
		return
	}

	agents := m.filtered[m.cursor].Agents
	if len(agents) == 0 {
		m.agentCursor = parentAgentIndex
		return
	}

	if m.agentCursor != parentAgentIndex && (m.agentCursor < 0 || m.agentCursor >= len(agents)) {
		m.agentCursor = 0
	}
}

func (m *Model) filterSessionsWithQuery(query string, base []session.Session) []session.Session {
	query = strings.TrimSpace(query)
	if query == "" {
		return base
	}

	next := make([]session.Session, 0, len(base))
	for _, row := range base {
		if filtered, matches := m.filterSessionWithQuery(query, row); matches {
			next = append(next, filtered)
		}
	}

	return m.rankFilteredSessions(query, next)
}

func (m *Model) filterSessionWithQuery(query string, row session.Session) (session.Session, bool) {
	if len(row.Agents) == 0 {
		return row, sessionMatches(query, row)
	}

	if !m.displayMode.tree() {
		return row, sessionMatches(query, row) || anyAgentMatches(query, row.Agents)
	}

	matchingAgents := filterMatchingAgents(query, row.Agents)
	if len(matchingAgents) == 0 {
		return row, sessionMatches(query, row)
	}

	row.Agents = matchingAgents
	row.AgentTotal = len(matchingAgents)

	return row, true
}

func filterMatchingAgents(query string, agents []session.AgentSession) []session.AgentSession {
	var matches []session.AgentSession

	for _, agent := range agents {
		if agentMatches(query, agent) {
			matches = append(matches, agent)
		}
	}

	return matches
}

func (m *Model) rankFilteredSessions(query string, filtered []session.Session) []session.Session {
	if m.displayMode.tree() || len(filtered) <= 1 {
		return filtered
	}

	ranked := make([]session.Session, 0, len(filtered))
	for _, match := range fuzzy.FindFrom(query, sessionNameSource(filtered)) {
		if match.Index < len(filtered) {
			ranked = append(ranked, filtered[match.Index])
		}
	}

	for _, row := range filtered {
		if !slices.ContainsFunc(ranked, func(rankedRow session.Session) bool {
			return rankedRow.Name == row.Name
		}) {
			ranked = append(ranked, row)
		}
	}

	return ranked
}

func isWordSeparator(r rune) bool {
	return r == '-' || r == '_' || r == '.' || r == '/' || r == ' ' || r == ':' || r == '@'
}

func acronymMatch(query string, target string) bool {
	var (
		initials strings.Builder
		current  strings.Builder
	)

	for i, r := range target {
		if isWordSeparator(r) {
			if current.Len() > 0 {
				initials.WriteRune(unicode.ToLower([]rune(current.String())[0]))
				current.Reset()
			}

			continue
		}

		if unicode.IsUpper(r) && i > 0 && current.Len() > 0 {
			initials.WriteRune(unicode.ToLower([]rune(current.String())[0]))
			current.Reset()
		}

		current.WriteRune(r)
	}

	if current.Len() > 0 {
		initials.WriteRune(unicode.ToLower([]rune(current.String())[0]))
	}

	return strings.Contains(initials.String(), strings.ToLower(query))
}

func fuzzyMatch(query string, target string) bool {
	if query == "" {
		return true
	}

	targetLower := strings.ToLower(strings.TrimSpace(target))
	queryLower := strings.ToLower(strings.TrimSpace(query))

	if targetLower == "" {
		return false
	}

	if strings.Contains(targetLower, queryLower) {
		return true
	}

	if acronymMatch(queryLower, target) {
		return true
	}

	for _, word := range strings.FieldsFunc(targetLower, isWordSeparator) {
		if strings.HasPrefix(word, queryLower) {
			return true
		}
	}

	if len(queryLower) >= minFuzzyQueryLength {
		return len(fuzzy.Find(queryLower, []string{targetLower})) > 0
	}

	return false
}

func agentMatches(query string, agent session.AgentSession) bool {
	query = strings.TrimSpace(query)
	if query == "" {
		return true
	}

	if fuzzyMatchAny(query, agent.Harness, agent.HarnessSession) || agentStatusMatches(query, agent.Status) {
		return true
	}

	if pathMatches(query, agent.ProjectRoot) || pathMatches(query, agent.CWD) {
		return true
	}

	return agent.RegistryID != "" && fuzzyMatch(query, agent.RegistryID)
}

func fuzzyMatchAny(query string, values ...string) bool {
	for _, value := range values {
		if fuzzyMatch(query, value) {
			return true
		}
	}

	return false
}

func pathMatches(query string, path string) bool {
	if path == "" {
		return false
	}

	return fuzzyMatch(query, pathName(path)) ||
		(strings.Contains(query, "/") && fuzzyMatch(query, path))
}

func agentStatusMatches(query string, status session.AgentStatus) bool {
	if fuzzyMatchAny(query, string(status), string(status.DisplayStatus()), agentStatusLabel(status)) {
		return true
	}

	switch status {
	case session.AgentStatusRunning:
		return fuzzyMatchAny(query, "live", "active", "busy", "working")
	case session.AgentStatusWaiting:
		return fuzzyMatchAny(query, "blocked", "approval", "prompt")
	case session.AgentStatusFailed:
		return fuzzyMatchAny(query, "error", "errored", "crash")
	case session.AgentStatusInterrupted:
		return fuzzyMatchAny(query, "paused", "stopped", "cancelled", "canceled") //nolint:misspell // British spelling is an intentional search alias.
	case session.AgentStatusIdle:
		return fuzzyMatch(query, "offline")
	case session.AgentStatusSleeping:
		return fuzzyMatchAny(query, "asleep", "parked")
	case session.AgentStatusDone:
		return fuzzyMatchAny(query, "merged", "closed", "finished")
	case session.AgentStatusUnknown:
		return false
	}

	return false
}

func anyAgentMatches(query string, agents []session.AgentSession) bool {
	for _, a := range agents {
		if agentMatches(query, a) {
			return true
		}
	}

	return false
}

func sessionMatches(query string, row session.Session) bool {
	query = strings.TrimSpace(query)
	if query == "" {
		return true
	}

	if len(fuzzy.Find(query, []string{row.Name})) > 0 {
		return true
	}

	if fuzzyMatchAny(query, row.ActiveWindowName, row.ActivePaneTitle) {
		return true
	}

	if pathMatches(query, row.ActivePanePath) {
		return true
	}

	if row.Origin != session.OriginSSH && row.Origin != session.OriginBridge {
		return false
	}

	return fuzzyMatchAny(
		query,
		string(row.Origin),
		row.RuntimeIdentity.ConnectionID,
		row.RuntimeIdentity.ConnectionLabel,
		string(row.RemoteStatus),
	)
}

func (s sessionNameSource) String(i int) string {
	row := s[i]
	if row.Origin != session.OriginSSH && row.Origin != session.OriginBridge {
		return row.Name
	}

	return strings.Join([]string{
		row.Name,
		string(row.Origin),
		row.RuntimeIdentity.ConnectionID,
		row.RuntimeIdentity.ConnectionLabel,
		string(row.RemoteStatus),
	}, " ")
}

func (s sessionNameSource) Len() int { return len(s) }
