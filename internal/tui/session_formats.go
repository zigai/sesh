package tui

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/session"
)

const (
	gitChangePartsCount  = 2
	centerPaddingDivisor = 2
)

type sessionRowFormats struct {
	seshSession   display.Template
	agentsSession display.Template
	agentsAgent   display.Template
}

type sessionFormatValues struct {
	model        *Model
	row          session.Session
	agent        session.AgentSession
	hasAgent     bool
	isLastAgent  bool
	isActivePane bool
	ageWidth     int
	contextWidth int
	now          time.Time
}

type nonGitCandidate struct {
	part        display.Part
	isSeparator bool
}

func emptyAgentSession() session.AgentSession {
	var agent session.AgentSession

	return agent
}

func compileSessionRowFormats(displayCfg config.DisplayConfig, settings config.SettingsConfig) (sessionRowFormats, error) {
	seshFormat := displayCfg.SeshFormat
	agentsSessionFormat := displayCfg.AgentsSessionFormat
	agentsAgentFormat := displayCfg.AgentsAgentFormat

	if settings.NormalizedShortcutStyle() == "right" {
		if seshFormat == config.DefaultSeshFormat || seshFormat == "" {
			seshFormat += "  {shortcut_badge:>3}"
		}

		if agentsAgentFormat == config.DefaultAgentsAgentFormat || agentsAgentFormat == "" {
			agentsAgentFormat += "  {shortcut_badge:>3}"
		}
	}

	seshSession, err := compileSessionRowFormat(
		"display.sesh_format",
		display.FormatKindSession,
		seshFormat,
	)
	if err != nil {
		return sessionRowFormats{}, err
	}

	agentsSession, err := compileSessionRowFormat(
		"display.agents_session_format",
		display.FormatKindAgentsSession,
		agentsSessionFormat,
	)
	if err != nil {
		return sessionRowFormats{}, err
	}

	agentsAgent, err := compileSessionRowFormat(
		"display.agents_agent_format",
		display.FormatKindAgentsAgent,
		agentsAgentFormat,
	)
	if err != nil {
		return sessionRowFormats{}, err
	}

	return sessionRowFormats{
		seshSession:   seshSession,
		agentsSession: agentsSession,
		agentsAgent:   agentsAgent,
	}, nil
}

func compileSessionRowFormat(
	name string,
	kind display.FormatKind,
	format string,
) (display.Template, error) {
	template, err := display.Compile(kind, format)
	if err != nil {
		return display.Template{}, oops.In("tui").Code("format_invalid").With("operation", "compile_format", "name", name).Wrapf(err, "%s", name)
	}

	return template, nil
}

func (m *Model) formattedSessionLine(
	row session.Session,
	agent session.AgentSession,
	hasAgent bool,
	isLastAgent bool,
	layout sessionTableLayout,
) display.Line {
	template, values := m.sessionRowFormatter(row, agent, hasAgent, isLastAgent, layout)

	line := template.Render(values)

	if hasAgent {
		if !agentHasGit(agent) {
			hadGit := partsHaveGit(line.Right)
			line = filterNonGitLine(line)

			if hadGit && len(line.Right) > 0 {
				line.Right = padNonGitRightLine(line.Right, layout.rightWidth)
			}
		} else {
			for index := range line.Right {
				part := &line.Right[index]
				part.Width = max(part.Width, layout.agentGitWidths[part.Field])
			}
		}
	}

	return line
}

func (m *Model) sessionRowFormatter(
	row session.Session,
	agent session.AgentSession,
	hasAgent bool,
	isLastAgent bool,
	layout sessionTableLayout,
) (display.Template, sessionFormatValues) {
	template := m.sessionFormats.seshSession
	if m.displayMode == displayModeAgents {
		template = m.sessionFormats.agentsSession
		if hasAgent {
			template = m.sessionFormats.agentsAgent
		}
	}

	return template, sessionFormatValues{
		model:        m,
		row:          row,
		agent:        agent,
		hasAgent:     hasAgent,
		isLastAgent:  isLastAgent,
		isActivePane: hasAgent && row.ActivePaneID != "" && row.ActivePaneID == agent.PaneID,
		ageWidth:     layout.agentAgeWidth,
		contextWidth: layout.rightWidth,
		now:          layout.now,
	}
}

func (m *Model) sessionFormatRightColumnWidth(layout sessionTableLayout) int {
	width := 0

	for _, row := range m.filtered {
		template, values := m.sessionRowFormatter(row, emptyAgentSession(), false, false, layout)
		width = max(width, display.MeasureRight(template, values, lipgloss.Width))

		if m.displayMode != displayModeAgents {
			continue
		}

		for index, agent := range row.Agents {
			line := m.formattedSessionLine(row, agent, true, index == len(row.Agents)-1, layout)

			agentWidth := 0
			for _, part := range line.Right {
				agentWidth += lipgloss.Width(alignSessionFormatPart(part))
			}

			width = max(width, agentWidth)
		}
	}

	return width
}

func (m *Model) agentGitColumnWidths(layout sessionTableLayout) map[string]int {
	widths := make(map[string]int)
	if m.displayMode != displayModeAgents {
		return widths
	}

	for _, row := range m.filtered {
		for _, agent := range row.Agents {
			if !agentHasGit(agent) {
				continue
			}

			template, values := m.sessionRowFormatter(row, agent, true, false, layout)
			for _, part := range template.Render(values).Right {
				if isGitField(part.Field) {
					widths[part.Field] = max(widths[part.Field], lipgloss.Width(alignSessionFormatPart(part)))
				}
			}
		}
	}

	return widths
}

func (v sessionFormatValues) ResolveSessionFormatField(field string) string {
	if value, ok := v.resolveSessionIdentityField(field); ok {
		return value
	}

	if value, ok := v.resolveSessionTmuxField(field); ok {
		return value
	}

	if value, ok := v.resolveAgentSummaryField(field); ok {
		return value
	}

	return v.resolveAgentDetailField(field)
}

func (v sessionFormatValues) resolveSessionTmuxField(field string) (string, bool) {
	switch field {
	case display.FieldCurrentMarker:
		if v.row.IsCurrent {
			return "* ", true
		}

		return "  ", true
	case display.FieldAttachedMarker:
		if v.row.Attached {
			return "@ ", true
		}

		return "  ", true
	case display.FieldActiveWindowName:
		return SanitizeTerminalText(v.row.ActiveWindowName), true
	case display.FieldActiveWindowIndex:
		return SanitizeTerminalText(v.row.ActiveWindowIndex), true
	case display.FieldActivePanePath:
		return SanitizeTerminalText(v.row.ActivePanePath), true
	case display.FieldActivePanePathName:
		return SanitizeTerminalText(pathName(v.row.ActivePanePath)), true
	case display.FieldActivePaneCommand:
		return SanitizeTerminalText(v.row.ActivePaneCommand), true
	case display.FieldActivePaneTitle:
		return SanitizeTerminalText(v.row.ActivePaneTitle), true
	default:
		return "", false
	}
}

func (v sessionFormatValues) resolveSessionIdentityField(field string) (string, bool) {
	if value, ok := v.resolveSessionSourceField(field); ok {
		return value, true
	}

	if value, ok := v.resolveShortcutField(field); ok {
		return value, true
	}

	switch field {
	case display.FieldSessionName:
		return SanitizeTerminalText(v.row.Name), true
	case display.FieldSessionID:
		return SanitizeTerminalText(v.row.ID), true
	case display.FieldSessionKind:
		return string(v.row.Kind), true
	case display.FieldWindowCount:
		return strconv.Itoa(v.row.WindowCount), true
	case display.FieldActivityAge:
		if v.row.Activity.IsZero() {
			return "", true
		}

		return FormatActivityAt(v.row.Activity, v.now), true
	case display.FieldAttached:
		return formatPresence(v.row.Attached, "attached"), true
	case display.FieldCurrent:
		return formatPresence(v.row.IsCurrent, "current"), true
	default:
		return "", false
	}
}

func (v sessionFormatValues) resolveSessionSourceField(field string) (string, bool) {
	switch field {
	case display.FieldSource:
		if isRemoteOrBridge(v.row) {
			return string(v.row.Origin), true
		}

		return string(session.OriginLocal), true
	case display.FieldConnection:
		if !isRemoteOrBridge(v.row) {
			return "", true
		}

		return remoteConnectionDisplayName(v.row), true
	case display.FieldSourcePrefix:
		return resolveSourcePrefix(v.row), true
	case display.FieldSourceStatus:
		return string(v.row.RemoteStatus), true
	default:
		return "", false
	}
}

func isRemoteOrBridge(row session.Session) bool {
	return row.Origin == session.OriginSSH || row.Origin == session.OriginBridge
}

func resolveSourcePrefix(row session.Session) string {
	if !isRemoteOrBridge(row) || row.RuntimeIdentity.ConnectionID == "" {
		return ""
	}

	prefix := remoteConnectionDisplayName(row)
	if row.RemoteStatus != session.RemoteStatusAvailable {
		prefix += "[" + string(row.RemoteStatus) + "]"
	}

	return prefix + ":"
}

func remoteConnectionDisplayName(row session.Session) string {
	if row.RuntimeIdentity.ConnectionLabel != "" {
		return SanitizeTerminalText(row.RuntimeIdentity.ConnectionLabel)
	}

	return SanitizeTerminalText(row.RuntimeIdentity.ConnectionID)
}

func (v sessionFormatValues) resolveAgentSummaryField(field string) (string, bool) {
	if value, ok := v.resolveAgentCountField(field); ok {
		return value, true
	}

	switch field {
	case display.FieldAgentStatus,
		display.FieldAgentStatusText,
		display.FieldAgentStatusGlyph,
		display.FieldAgentAge,
		display.FieldAgentDuration,
		display.FieldAgentClock:
		return v.resolveSessionAgentStateField(field), true
	case display.FieldSessionAgentContext:
		return v.sessionAgentContext(), true
	default:
		return "", false
	}
}

func (v sessionFormatValues) resolveSessionAgentStateField(field string) string {
	if v.row.AgentTotal <= 0 {
		return ""
	}

	status, statusEnum, age := v.model.agentStatusAndAgeAt(v.row, v.now)

	switch field {
	case display.FieldAgentStatus:
		if v.model.agentStatusStyle() == "text" {
			return status
		}

		return v.model.agentStatusGlyph(statusEnum)
	case display.FieldAgentStatusText:
		return status
	case display.FieldAgentStatusGlyph:
		return v.model.agentStatusGlyph(statusEnum)
	case display.FieldAgentAge:
		return age
	case display.FieldAgentDuration, display.FieldAgentClock:
		since := v.row.AgentIdleSince
		if v.row.AgentWaiting > 0 {
			since = v.row.AgentWaitingSince
		}

		if since.IsZero() {
			return ""
		}

		return formatAgentClockAt(since, v.now)
	default:
		return ""
	}
}

func (v sessionFormatValues) resolveAgentCountField(field string) (string, bool) {
	switch field {
	case display.FieldAgentCount:
		return v.agentCount(), true
	case display.FieldAgentTotal:
		return strconv.Itoa(v.row.AgentTotal), true
	case display.FieldAgentWorking:
		return strconv.Itoa(v.row.AgentActive), true
	case display.FieldAgentWaiting:
		return strconv.Itoa(v.row.AgentWaiting), true
	case display.FieldAgentIdle:
		return strconv.Itoa(agentIdleCount(v.row)), true
	case display.FieldAgentFailed:
		return strconv.Itoa(v.row.AgentFailed), true
	case display.FieldAgentInterrupted:
		return strconv.Itoa(v.row.AgentInterrupted), true
	case display.FieldAgentUnknown:
		return strconv.Itoa(v.row.AgentUnknown), true
	default:
		return "", false
	}
}

func (v sessionFormatValues) resolveAgentDetailField(field string) string {
	if !v.hasAgent {
		return ""
	}

	if value, ok := v.resolveAgentIdentityField(field); ok {
		return value
	}

	if value, ok := v.resolveAgentLocationField(field); ok {
		return value
	}

	if value, ok := v.resolveAgentGitField(field); ok {
		return value
	}

	return v.resolveAgentStateField(field)
}

func (v sessionFormatValues) resolveAgentIdentityField(field string) (string, bool) {
	switch field {
	case display.FieldTree:
		if v.model != nil && v.model.shortcutStyle() == "indent" {
			return v.treeIndent(), true
		}

		return v.treeBranch(), true
	case display.FieldTreeBranch:
		return v.treeBranch(), true
	case display.FieldTreeIndent:
		return v.treeIndent(), true
	case display.FieldHarness:
		return SanitizeTerminalText(capitalizedHarness(v.agent.Harness)), true
	case display.FieldHarnessRaw:
		return SanitizeTerminalText(v.agent.Harness), true
	case display.FieldAgentSession:
		return SanitizeTerminalText(v.agent.HarnessSession), true
	case display.FieldRegistryID:
		return SanitizeTerminalText(v.agent.RegistryID), true
	case display.FieldPaneID:
		return SanitizeTerminalText(v.agent.PaneID), true
	default:
		return "", false
	}
}

func (v sessionFormatValues) resolveAgentLocationField(field string) (string, bool) {
	switch field {
	case display.FieldAgentCWD:
		return SanitizeTerminalText(v.agent.CWD), true
	case display.FieldAgentCWDName:
		return SanitizeTerminalText(pathName(v.agent.CWD)), true
	case display.FieldAgentProjectRoot:
		return SanitizeTerminalText(v.agent.ProjectRoot), true
	case display.FieldAgentProjectName:
		return SanitizeTerminalText(pathName(v.agent.ProjectRoot)), true
	case display.FieldAgentSessionPath:
		return SanitizeTerminalText(v.agent.SessionPath), true
	case display.FieldAgentSessionFile:
		return SanitizeTerminalText(pathName(v.agent.SessionPath)), true
	case display.FieldAgentExecutable:
		return SanitizeTerminalText(v.agent.Executable), true
	case display.FieldAgentPID:
		if v.agent.PID <= 0 {
			return "", true
		}

		return strconv.Itoa(v.agent.PID), true
	default:
		return "", false
	}
}

func (v sessionFormatValues) resolveAgentGitField(field string) (string, bool) {
	if value, ok := v.resolveAgentGitCountField(field); ok {
		return v.styleAgentGitCount(field, value), true
	}

	switch field {
	case display.FieldAgentGitBranch:
		return SanitizeTerminalText(v.agent.GitBranch), true
	case display.FieldAgentGitGlyph:
		if !agentHasGit(v.agent) {
			return "", true
		}

		return "", true
	case display.FieldAgentGitChanges:
		return SanitizeTerminalText(v.agent.GitChanges), true
	case display.FieldAgentGitInfo:
		return agentGitInfo(v.agent), true
	case display.FieldAgentGitLabel:
		return agentGitLabel(v.agent), true
	case display.FieldAgentWorktree:
		return SanitizeTerminalText(v.agent.Worktree), true
	case display.FieldAgentWorktreeName:
		return SanitizeTerminalText(pathName(v.agent.Worktree)), true
	default:
		return "", false
	}
}

func (v sessionFormatValues) resolveAgentGitCountField(field string) (string, bool) {
	switch field {
	case display.FieldAgentGitAdded, display.FieldAgentGitDeleted:
		parts := strings.Fields(v.agent.GitChanges)
		if len(parts) != gitChangePartsCount {
			return "", true
		}

		if field == display.FieldAgentGitAdded {
			return SanitizeTerminalText(parts[0]), true
		}

		return SanitizeTerminalText(parts[1]), true
	case display.FieldAgentGitAhead:
		if !v.agent.GitTrackingKnown {
			return "", true
		}

		return "↑" + strconv.Itoa(v.agent.GitAhead), true
	case display.FieldAgentGitBehind:
		if !v.agent.GitTrackingKnown {
			return "", true
		}

		return "↓" + strconv.Itoa(v.agent.GitBehind), true
	default:
		return "", false
	}
}

func (v sessionFormatValues) styleAgentGitCount(field, value string) string {
	if value == "" {
		return ""
	}

	theme := v.model.config.Theme
	color := theme.Muted
	dim := false

	if value != "+0" && value != "-0" && value != "↑0" && value != "↓0" {
		switch field {
		case display.FieldAgentGitAdded:
			color = theme.Success
			dim = true
		case display.FieldAgentGitDeleted:
			color = theme.Error
			dim = true
		case display.FieldAgentGitAhead:
			color = theme.Primary
		case display.FieldAgentGitBehind:
			color = theme.Secondary
		}
	}

	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Faint(dim).Render(value)
}

func agentGitLabel(agent session.AgentSession) string {
	if agent.Worktree == "" {
		return ""
	}

	branch := agent.GitBranch
	if branch == "" {
		branch = "detached"
	}

	if agent.LinkedWorktree {
		branch += " / " + pathName(agent.Worktree)
	}

	return SanitizeTerminalText(branch)
}

func agentGitInfo(agent session.AgentSession) string {
	branch := agentGitLabel(agent)
	if branch == "" {
		return ""
	}

	if agent.GitChanges != "" {
		branch += " " + agent.GitChanges
	}

	return SanitizeTerminalText(branch)
}

func (v sessionFormatValues) resolveAgentStateField(field string) string {
	unreadIdle := v.model.agentIdleUnread(v.row, v.agent)

	switch field {
	case display.FieldStatus:
		if v.model.agentStatusStyle() == "text" {
			return displayedAgentStatusLabel(v.agent.Status, unreadIdle)
		}

		return v.model.displayedAgentStatusGlyph(v.agent.Status, unreadIdle)
	case display.FieldStatusText:
		return displayedAgentStatusLabel(v.agent.Status, unreadIdle)
	case display.FieldStatusGlyph:
		return v.model.displayedAgentStatusGlyph(v.agent.Status, unreadIdle)
	case display.FieldStatusAge:
		return v.model.agentStatusAgeAt(v.agent, v.now)
	case display.FieldStatusDuration, display.FieldStatusClock:
		return formatAgentClockAt(v.agent.StatusSince, v.now)
	case display.FieldActiveMarker:
		return activePaneMarker(v.isActivePane)
	case display.FieldStale:
		return ""
	case display.FieldAgentContext:
		return v.agentContext()
	default:
		return ""
	}
}

func (v sessionFormatValues) agentCount() string {
	if v.row.AgentTotal <= 0 {
		return ""
	}

	if v.model.displayMode == displayModeSesh && !v.model.config.AgentSessions.PopupCounts {
		return ""
	}

	return agentCountLabel(v.row)
}

func (v sessionFormatValues) sessionAgentContext() string {
	if v.model.displayMode == displayModeSesh {
		return v.agentCount()
	}

	return ""
}

func (v sessionFormatValues) agentContext() string {
	unreadIdle := v.model.agentIdleUnread(v.row, v.agent)
	context := v.model.formattedAgentSessionContextAt(v.agent, v.now, v.ageWidth, v.isActivePane, unreadIdle)

	if v.contextWidth > 0 && lipgloss.Width(context) > v.contextWidth {
		return v.model.compactAgentSessionContextAt(v.agent, v.now, v.isActivePane, unreadIdle)
	}

	return context
}

func agentIdleCount(row session.Session) int {
	return max(row.AgentTotal-row.AgentActive-row.AgentWaiting-row.AgentFailed-row.AgentInterrupted-row.AgentUnknown, 0)
}

func formatPresence(present bool, label string) string {
	if !present {
		return ""
	}

	return label
}

func pathName(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}

	name := filepath.Base(filepath.Clean(path))
	if name == "." || name == string(filepath.Separator) {
		return ""
	}

	return name
}

func agentTreeConnector(isLast bool) string {
	if isLast {
		return "└─ "
	}

	return "├─ "
}

func (m *Model) shortcutStyle() string {
	if m.config == nil {
		return "none"
	}

	return m.config.Settings.NormalizedShortcutStyle()
}

func (v sessionFormatValues) shortcutDigit() string {
	if v.model == nil {
		return ""
	}

	shortcuts := v.model.visibleShortcutRows()

	sessionIndex := slices.IndexFunc(v.model.filtered, func(s session.Session) bool {
		return s.RuntimeKey() == v.row.RuntimeKey()
	})
	if sessionIndex < 0 {
		return ""
	}

	agentIndex := parentAgentIndex
	if v.hasAgent {
		agentIndex = slices.IndexFunc(v.row.Agents, func(a session.AgentSession) bool {
			return a.RegistryID == v.agent.RegistryID && a.PaneID == v.agent.PaneID
		})
		if agentIndex < 0 {
			return ""
		}
	}

	listRow := popupListRow{sessionIndex: sessionIndex, agentIndex: agentIndex}

	return v.model.shortcutDigitForRow(listRow, shortcuts)
}

func (v sessionFormatValues) treeBranch() string {
	muted := "#6c7086"
	if v.model != nil && v.model.config != nil && v.model.config.Theme.Muted != "" {
		muted = v.model.config.Theme.Muted
	}

	style := lipgloss.NewStyle().Foreground(lipgloss.Color(muted))

	return "\x1b[22m" + style.Render(agentTreeConnector(v.isLastAgent))
}

func (v sessionFormatValues) treeIndent() string {
	digit := v.shortcutDigit()
	if digit != "" {
		muted := "#6c7086"
		if v.model != nil && v.model.config != nil && v.model.config.Theme.Muted != "" {
			muted = v.model.config.Theme.Muted
		}

		style := lipgloss.NewStyle().Foreground(lipgloss.Color(muted))

		return "\x1b[22m" + style.Render(" "+digit+" ")
	}

	return "   "
}

func (v sessionFormatValues) formatShortcut(text string) string {
	muted := "#6c7086"
	if v.model != nil && v.model.config != nil && v.model.config.Theme.Muted != "" {
		muted = v.model.config.Theme.Muted
	}

	return lipgloss.NewStyle().Foreground(lipgloss.Color(muted)).Render(text)
}

func (v sessionFormatValues) resolveShortcutField(field string) (string, bool) {
	switch field {
	case display.FieldShortcut:
		digit := v.shortcutDigit()
		if digit == "" {
			return "", true
		}

		return v.formatShortcut(digit), true
	case display.FieldShortcutBadge:
		digit := v.shortcutDigit()
		if digit == "" {
			return "", true
		}

		return v.formatShortcut("^" + digit), true
	default:
		return "", false
	}
}

func activePaneMarker(active bool) string {
	if active {
		return "* "
	}

	return "  "
}

func fitSessionFormatParts(parts []display.Part, width int) string {
	if width <= 0 {
		return ""
	}

	values := make([]string, len(parts))
	lineWidth := 0

	for index, part := range parts {
		values[index] = alignSessionFormatPart(part)
		lineWidth += lipgloss.Width(values[index])
	}

	if overflow := lineWidth - width; overflow > 0 {
		for index, part := range parts {
			if overflow <= 0 || !flexibleSessionFormatField(part.Field) {
				continue
			}

			partWidth := lipgloss.Width(values[index])

			targetWidth := max(partWidth-overflow, 0)
			values[index] = fitFlexibleSessionFormatPart(part, targetWidth)

			overflow -= partWidth - lipgloss.Width(values[index])
		}
	} else if extra := width - lineWidth; extra > 0 {
		expandFlexibleSessionFormatParts(parts, values, extra)
	}

	line := strings.Join(values, "")
	if lipgloss.Width(line) > width {
		return display.Truncate(line, width)
	}

	return line
}

func expandFlexibleSessionFormatParts(parts []display.Part, values []string, extra int) {
	for index, part := range parts {
		if extra <= 0 || !flexibleSessionFormatField(part.Field) {
			continue
		}

		currentWidth := lipgloss.Width(values[index])
		fullWidth := lipgloss.Width(part.Value)

		if fullWidth <= currentWidth {
			continue
		}

		expandBy := min(fullWidth-currentWidth, extra)
		targetWidth := currentWidth + expandBy

		switch {
		case targetWidth >= fullWidth:
			values[index] = part.Value
		case part.Alignment == display.AlignmentRight:
			values[index] = truncateLeftWidth(part.Value, targetWidth)
		default:
			values[index] = display.Truncate(part.Value, targetWidth)
		}

		extra -= expandBy
	}
}

func padNonGitRightLine(parts []display.Part, targetWidth int) []display.Part {
	if len(parts) == 0 || targetWidth <= 0 {
		return parts
	}

	currentRightWidth := 0
	for _, part := range parts {
		currentRightWidth += lipgloss.Width(alignSessionFormatPart(part))
	}

	if currentRightWidth >= targetWidth {
		return parts
	}

	pad := targetWidth - currentRightWidth
	if len(parts) > 1 && parts[len(parts)-1].Field == display.FieldShortcutBadge {
		return slices.Insert(parts, len(parts)-1, display.Part{
			Field:       "",
			Value:       strings.Repeat(" ", pad),
			Alignment:   display.AlignmentNone,
			Width:       0,
			MaxWidth:    0,
			HasMaxWidth: false,
		})
	}

	return append(parts, display.Part{
		Field:       "",
		Value:       strings.Repeat(" ", pad),
		Alignment:   display.AlignmentNone,
		Width:       0,
		MaxWidth:    0,
		HasMaxWidth: false,
	})
}

func fitFlexibleSessionFormatPart(part display.Part, width int) string {
	value := truncateSessionFormatPart(part)
	if lipgloss.Width(value) > width {
		if part.Field == display.FieldAgentSession || part.Alignment == display.AlignmentRight {
			return truncateLeftWidth(value, width)
		}

		return display.Truncate(value, width)
	}

	part.Value = value
	part.Width = min(part.Width, width)
	part.HasMaxWidth = false

	return alignSessionFormatPart(part)
}

func alignSessionFormatPart(part display.Part) string {
	value := truncateSessionFormatPart(part)

	paddingWidth := max(part.Width-lipgloss.Width(value), 0)
	if paddingWidth == 0 {
		return value
	}

	switch part.Alignment {
	case display.AlignmentRight:
		return strings.Repeat(" ", paddingWidth) + value
	case display.AlignmentCenter:
		leftWidth := paddingWidth / centerPaddingDivisor
		return strings.Repeat(" ", leftWidth) + value + strings.Repeat(" ", paddingWidth-leftWidth)
	case display.AlignmentNone, display.AlignmentLeft:
		return value + strings.Repeat(" ", paddingWidth)
	default:
		return value
	}
}

func truncateSessionFormatPart(part display.Part) string {
	if !part.HasMaxWidth || lipgloss.Width(part.Value) <= part.MaxWidth {
		return part.Value
	}

	if part.Alignment == display.AlignmentRight {
		return truncateLeftWidth(part.Value, part.MaxWidth)
	}

	return display.Truncate(part.Value, part.MaxWidth)
}

func flexibleSessionFormatField(field string) bool {
	return field == display.FieldSessionName || field == display.FieldAgentSession
}

func agentHasGit(agent session.AgentSession) bool {
	return agent.Worktree != "" || agent.GitBranch != "" || agent.GitChanges != ""
}

func partsHaveGit(parts []display.Part) bool {
	for _, p := range parts {
		if isGitField(p.Field) || containsGitGlyph(p.Value) {
			return true
		}
	}

	return false
}

func isGitField(field string) bool {
	return strings.HasPrefix(field, "agent_git_") || strings.HasPrefix(field, "agent_worktree")
}

func isGitGlyph(r rune) bool {
	switch r {
	case '', '', '', '', '󰊢':
		return true
	default:
		return false
	}
}

func containsGitGlyph(s string) bool {
	return strings.IndexFunc(s, isGitGlyph) >= 0
}

func stripGitGlyphs(s string) string {
	return strings.Map(func(r rune) rune {
		if isGitGlyph(r) {
			return -1
		}

		return r
	}, s)
}

func isSeparatorLiteral(s string) bool {
	return strings.Trim(s, " \t\r\n|·-•/:") == ""
}

func filterNonGitLine(line display.Line) display.Line {
	return display.Line{
		Left:  filterNonGitParts(line.Left),
		Right: filterNonGitParts(line.Right),
	}
}

func filterNonGitParts(parts []display.Part) []display.Part {
	hasGitContent := false
	hasNonGitField := false

	for _, p := range parts {
		if isGitField(p.Field) || containsGitGlyph(p.Value) {
			hasGitContent = true
		} else if p.Field != "" {
			hasNonGitField = true
		}
	}

	if !hasGitContent {
		return parts
	}

	if !hasNonGitField {
		return nil
	}

	candidates, hasSubstantial := collectNonGitCandidates(parts)
	if !hasSubstantial {
		return nil
	}

	return collapseSeparators(candidates)
}

func collectNonGitCandidates(parts []display.Part) ([]nonGitCandidate, bool) {
	var candidates []nonGitCandidate

	hasSubstantialNonGit := false

	for _, p := range parts {
		if isGitField(p.Field) {
			continue
		}

		if containsGitGlyph(p.Value) {
			p.Value = stripGitGlyphs(p.Value)
			if strings.TrimSpace(p.Value) == "" {
				continue
			}
		}

		if p.Field == "" && isSeparatorLiteral(p.Value) {
			candidates = append(candidates, nonGitCandidate{part: p, isSeparator: true})

			continue
		}

		candidates = append(candidates, nonGitCandidate{part: p, isSeparator: false})
		hasSubstantialNonGit = true
	}

	return candidates, hasSubstantialNonGit
}

func collapseSeparators(candidates []nonGitCandidate) []display.Part {
	var result []display.Part

	prevWasSep := true

	for _, c := range candidates {
		if c.isSeparator {
			if !prevWasSep {
				result = append(result, c.part)
				prevWasSep = true
			}
		} else {
			result = append(result, c.part)
			prevWasSep = false
		}
	}

	for len(result) > 0 && result[len(result)-1].Field == "" && isSeparatorLiteral(result[len(result)-1].Value) {
		result = result[:len(result)-1]
	}

	return result
}
