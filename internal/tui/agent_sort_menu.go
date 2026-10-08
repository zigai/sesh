package tui

import (
	"strings"
)

func agentSortChoices() []agentSort {
	return []agentSort{agentSortPosition, agentSortName, agentSortAttention}
}

func (m *Model) cycleAgentSort() bool {
	if !m.showsAgentSortIndicator() {
		return false
	}

	choices := agentSortChoices()
	current := 0

	for index, order := range choices {
		if order == m.agentSort {
			current = index
			break
		}
	}

	m.setAgentSort(choices[(current+1)%len(choices)])

	return true
}

func (m *Model) showsAgentSortIndicator() bool {
	return m.displayMode == displayModeAgents && m.config != nil && m.config.AgentSessions.Enabled
}

func (m *Model) renderAgentSortBadge() string {
	key := newActionKeyMap(m.config).agentSort.Help().Key
	desc := "sort: " + m.agentSort.label()

	if key == "" {
		return ansiFooterDesc + desc + ansiReset
	}

	var out strings.Builder
	writeFooterPart(&out, newFooterPart(key, desc), false)

	return out.String()
}

func (m *Model) showsAgentSortBadge() bool {
	return m.showsAgentSortIndicator() && (!m.showsDisplayModeIndicator() || m.width >= 110)
}

func (m *Model) renderFooterBadges() string {
	var badges []string
	if m.hasStatusBadge() {
		badges = append(badges, m.renderStatusBadge())
	}

	if m.showsAgentSortBadge() {
		badges = append(badges, m.renderAgentSortBadge())
	}

	if m.showsDisplayModeIndicator() {
		badges = append(badges, m.renderModeBadge())
	}

	return strings.Join(badges, "  ")
}
