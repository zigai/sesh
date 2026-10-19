package tui

import (
	"strings"
)

type footerBadge struct {
	text  string
	click func()
}

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

func (m *Model) showsFooterBadges() bool {
	return m.showsDisplayModeIndicator() || m.showsAgentSortIndicator() || m.displayMode == displayModeInbox || m.hasStatusBadge()
}

func (m *Model) showsInboxGroupBadge() bool {
	return m.displayMode == displayModeInbox && (!m.showsDisplayModeIndicator() || m.width >= 110)
}

func (m *Model) renderInboxGroupBadge() string {
	key := newActionKeyMap(m.config).inboxGroup.Help().Key
	desc := "group: " + m.inboxGroup

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
	badges := m.footerBadges()
	texts := make([]string, 0, len(badges))

	for _, badge := range badges {
		texts = append(texts, badge.text)
	}

	return strings.Join(texts, strings.Repeat(" ", footerPartSpacing))
}

func (m *Model) footerBadges() []footerBadge {
	var badges []footerBadge
	if m.hasStatusBadge() {
		badges = append(badges, footerBadge{text: m.renderStatusBadge(), click: m.dismissStatus})
	}

	if m.showsAgentSortBadge() {
		badges = append(badges, footerBadge{text: m.renderAgentSortBadge(), click: func() {
			if m.cycleAgentSort() {
				m.clearStatus()
			}
		}})
	}

	if m.showsInboxGroupBadge() {
		badges = append(badges, footerBadge{text: m.renderInboxGroupBadge(), click: func() {
			if m.cycleInboxGroup() {
				m.clearStatus()
			}
		}})
	}

	if m.showsDisplayModeIndicator() {
		badges = append(badges, footerBadge{text: m.renderModeBadge(), click: nil})
	}

	return badges
}
