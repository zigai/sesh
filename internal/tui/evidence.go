package tui

import (
	"context"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/inbox"
	"github.com/zigai/sesh/internal/session"
)

const pager = "less"

var errEvidenceUnreadable = errors.New("the latest reports of some agents could not be read")

type EvidenceSource interface {
	LastReport(ctx context.Context, agent session.AgentSession) (string, error)
}

type evidenceEntry struct {
	turnEndedAt time.Time
	items       []inbox.Evidence
}

type evidenceResultMsg struct {
	entries map[string]evidenceEntry
	err     error
}

func (m *Model) evidenceWanted(agent session.AgentSession) bool {
	key := conversationKey(agent)
	if key == "" || agent.Host != "" || agent.SessionPath == "" || agent.StatusSince.IsZero() {
		return false
	}

	switch agent.Status.DisplayStatus() {
	case session.AgentStatusIdle, session.AgentStatusInterrupted, session.AgentStatusFailed:
	case session.AgentStatusRunning, session.AgentStatusWaiting, session.AgentStatusUnknown,
		session.AgentStatusSleeping, session.AgentStatusMerged, session.AgentStatusClosed:
		return false
	}

	entry, read := m.evidence[key]

	return !read || !entry.turnEndedAt.Equal(agent.StatusSince)
}

func (m *Model) loadEvidenceCommand() tea.Cmd {
	if m.deps.Evidence == nil || m.evidenceLoading || m.displayMode != displayModeInbox {
		return nil
	}

	wanted := make(map[string]session.AgentSession)

	for _, item := range m.inboxItems {
		if m.evidenceWanted(item.agent) {
			wanted[conversationKey(item.agent)] = item.agent
		}
	}

	if len(wanted) == 0 {
		return nil
	}

	m.evidenceLoading = true
	source := m.deps.Evidence
	ctx := m.operationContext()

	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		entries, err := readReports(ctx, source, wanted)

		return evidenceResultMsg{entries: entries, err: err}
	}
}

func readReports(ctx context.Context, source EvidenceSource, wanted map[string]session.AgentSession) (map[string]evidenceEntry, error) {
	entries := make(map[string]evidenceEntry, len(wanted))
	unread := 0

	for key, agent := range wanted {
		report, err := source.LastReport(ctx, agent)
		if err != nil {
			if ctx.Err() == nil {
				unread++
				entries[key] = unreadableUntilNextTurn(agent)
			}

			continue
		}

		entries[key] = evidenceEntry{turnEndedAt: agent.StatusSince, items: inbox.ParseEvidence(report, agent.CWD)}
	}

	if unread > 0 && ctx.Err() == nil {
		return entries, errEvidenceUnreadable
	}

	return entries, nil
}

func unreadableUntilNextTurn(agent session.AgentSession) evidenceEntry {
	return evidenceEntry{turnEndedAt: agent.StatusSince, items: nil}
}

func (m *Model) handleEvidence(msg evidenceResultMsg) {
	m.evidenceLoading = false

	if msg.err != nil {
		logTUIDegradation(m.operationContext(), tuiOperationLoadEvidence, msg.err)
	}

	maps.Copy(m.evidence, msg.entries)

	if len(msg.entries) == 0 {
		return
	}

	selected := m.selectedSessionIdentity()

	m.refreshDisplaySessions()
	m.filterSessions()
	m.restoreSelectedSession(selected)
}

func (m *Model) evidenceOf(item inboxItem) []inbox.Evidence {
	entry, ok := m.evidence[conversationKey(item.agent)]
	if !ok || conversationKey(item.agent) == "" || !entry.turnEndedAt.Equal(item.agent.StatusSince) {
		return nil
	}

	return entry.items
}

func (m *Model) evidenceCount(agent session.AgentSession) string {
	if m.displayMode != displayModeInbox {
		return ""
	}

	item, ok := m.inboxItems[inboxKey(agent.Host, agent.PaneID)]
	if !ok {
		return ""
	}

	if count := len(m.evidenceOf(item)); count > 0 {
		return strconv.Itoa(count) + " ev"
	}

	return ""
}

func evidenceDetail(items []inbox.Evidence, dir string) string {
	names := make([]string, 0, len(items))

	for _, item := range items {
		names = append(names, evidenceName(item, dir))
	}

	return strings.Join(names, " · ")
}

func evidenceName(item inbox.Evidence, dir string) string {
	if item.Kind == inbox.EvidenceURL || dir == "" {
		return SanitizeTerminalText(item.Target)
	}

	if relative, err := filepath.Rel(dir, item.Target); err == nil && !strings.HasPrefix(relative, "..") {
		return SanitizeTerminalText(relative)
	}

	return SanitizeTerminalText(session.ShortenHomePath(item.Target))
}

func (m *Model) openEvidenceMenu(item inboxItem) func() (tea.Model, tea.Cmd) {
	items := m.evidenceOf(item)
	entries := make([]menuEntry, 0, len(items))

	for _, evidence := range items {
		entries = append(entries, menuEntry{
			label: evidenceName(evidence, item.agent.CWD), key: string(evidence.Kind), run: m.openEvidence(evidence, item.agent.CWD),
		})
	}

	return m.openMenu(&rowMenu{
		title: "evidence", entries: entries, cursor: 0, anchored: false, anchorX: 0, anchorY: 0,
		box: menuBox{x: 0, y: 0, width: 0, height: 0},
	})
}

func (m *Model) openEvidence(evidence inbox.Evidence, dir string) func() (tea.Model, tea.Cmd) {
	target := evidence.Target
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(target, "~/") {
		target = filepath.Join(home, target[2:])
	}

	if evidence.Kind != inbox.EvidenceText {
		return m.runOpener(dir, "xdg-open", target)
	}

	command := exec.CommandContext(context.WithoutCancel(m.operationContext()), pager, "--", target)
	command.Dir = dir

	if m.terminal != nil {
		command.Stdout = m.terminal
	}

	return func() (tea.Model, tea.Cmd) {
		return m, tea.ExecProcess(command, func(err error) tea.Msg { return openedMsg{err: err} })
	}
}
