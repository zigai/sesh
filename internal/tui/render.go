package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/session"
)

const (
	defaultSelectionBar    = "▌ "
	shortcutKeyColor       = "12"
	minSearchWidth         = 18
	popupViewportInset     = 0
	minFrameWidth          = 5
	minFrameHeight         = 4
	statusWidthInset       = 4
	frameBorderPadding     = 2
	bufferLineHeadroom     = 64
	bufferLineMargin       = 2
	popupLineMargin        = 2
	popupBodyWidthMargin   = 4
	popupBodyHeightMargin  = 2
	minFooterRows          = 3
	listBufferPadding      = 48
	separatorDashDivisor   = 2
	minSeparatorDashes     = 2
	minFullFooterWidth     = 42
	compactFooterMaxWidth  = 70
	initialFooterPartsCap  = 10
	defaultSessionMinWidth = 40
	sessionPartEndIndex    = 2
)

const (
	ansiReset                   = "\x1b[0m"
	ansiBold                    = "\x1b[1m"
	ansiDefaultName             = "\x1b[38;5;252m"
	ansiDefaultSelected         = "\x1b[1;38;5;255;48;2;42;42;42m"
	ansiDefaultSelectedBg       = "\x1b[48;2;42;42;42m"
	ansiDefaultBorder           = "\x1b[38;2;58;61;69m"
	ansiDefaultVerticalBorder   = ansiDefaultBorder + "│" + ansiReset
	ansiDefaultMuted            = "\x1b[38;2;108;112;134m"
	ansiDefaultPrompt           = "\x1b[1;38;5;252m"
	ansiDefaultSelectedBar      = "\x1b[1;94;48;2;42;42;42m" + defaultSelectionBar + "\x1b[m"
	ansiDefaultSelectedBarGlyph = "\x1b[1;94;48;2;42;42;42m▌\x1b[m"
)

const (
	ansiFooterKey       = "\x1b[1;94m"
	ansiFooterKeyReset  = "\x1b[m"
	ansiFooterDesc      = "\x1b[38;5;245m"
	ansiFooterHoverKey  = "\x1b[48;5;236;1;94m"
	ansiFooterHoverDesc = "\x1b[48;5;236;38;5;245m"
)

type popupLayout struct {
	bodyWidth       int
	bodyHeight      int
	listHeight      int
	detailRows      int
	showFooter      bool
	showFooterHints bool
}

type sessionTableLayout struct {
	width         int
	prefixWidth   int
	leftWidth     int
	rightWidth    int
	agentAgeWidth int
	agentRows     agentTableLayout
	now           time.Time
}

type sessionRowStyles struct {
	fastDefaultTheme bool
	name             lipgloss.Style
	nameSeq          string
	selectedName     lipgloss.Style
	selectedNameSeq  string
	selectedBar      string
	selectedBarGlyph string
	selectedPad      lipgloss.Style
	selectedBgSeq    string
	blankPrefix      string
	blankLine        string
	mutedStyle       lipgloss.Style
	selectedMuted    lipgloss.Style
	sessionKeySeq    string
	keyChipSeq       string
	fadedSeq         string
}

type footerPart struct {
	key     string
	desc    string
	width   int
	binding action.Binding
}

func (m *Model) viewContent() string {
	if m.width == 0 || m.height == 0 {
		return "Loading..."
	}

	if m.contentWidth() < minFrameWidth || m.contentHeight() < minFrameHeight {
		return truncateStyled(m.displayMode.label(), m.contentWidth())
	}

	frame := m.renderFrame()

	if m.showHelp {
		return m.insetPopupContent(overlayCentered(frame, m.renderHelpOverlay(), m.contentWidth(), m.contentHeight()))
	}

	if dialogOverlay := m.renderDialog(); dialogOverlay != "" {
		return m.insetPopupContent(overlayCentered(frame, dialogOverlay, m.contentWidth(), m.contentHeight()))
	}

	if m.menu != nil {
		return m.insetPopupContent(m.renderMenu(frame))
	}

	return m.insetPopupContent(frame)
}

func (m *Model) renderFrame() string {
	layout := m.calcPopupLayout(false)

	horizontal := strings.Repeat("─", layout.bodyWidth+frameBorderPadding)
	topBorder, bottomBorder, leftBorder, rightBorder := m.renderFrameBorders(horizontal)

	var out strings.Builder
	out.Grow((layout.bodyWidth + bufferLineHeadroom) * (layout.bodyHeight + bufferLineMargin))
	out.WriteString(topBorder)

	writeExactFramedSection(&out, m.renderInputBar(layout.bodyWidth), leftBorder, rightBorder)

	m.writeListFramedSection(&out, layout.bodyWidth, layout.listHeight, leftBorder, rightBorder)

	for _, line := range m.renderInboxDetail(layout.bodyWidth, layout.detailRows) {
		writeExactFramedLine(&out, line, leftBorder, rightBorder)
	}

	if layout.showFooter {
		writeExactFramedSection(&out, m.renderModeFooter(layout.bodyWidth, layout.showFooterHints), leftBorder, rightBorder)
	}

	out.WriteByte('\n')
	out.WriteString(bottomBorder)

	return out.String()
}

func (m *Model) renderFrameBorders(horizontal string) (string, string, string, string) {
	return frameBorders(m.config, horizontal)
}

func frameBorders(cfg *config.Config, horizontal string) (string, string, string, string) {
	if defaultBorderTheme(cfg) {
		return ansiDefaultBorder + "┌" + horizontal + "┐" + ansiReset,
			ansiDefaultBorder + "└" + horizontal + "┘" + ansiReset,
			ansiDefaultVerticalBorder,
			ansiDefaultVerticalBorder
	}

	borderStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(cfg.Theme.Border))

	return borderStyle.Render("┌" + horizontal + "┐"),
		borderStyle.Render("└" + horizontal + "┘"),
		borderStyle.Render("│"),
		borderStyle.Render("│")
}

func writeExactFramedSection(out *strings.Builder, section string, leftBorder string, rightBorder string) {
	for line := range strings.SplitSeq(section, "\n") {
		writeExactFramedLine(out, line, leftBorder, rightBorder)
	}
}

func writeExactFramedLine(out *strings.Builder, line string, leftBorder string, rightBorder string) {
	out.WriteByte('\n')
	out.WriteString(leftBorder)
	out.WriteByte(' ')
	out.WriteString(line)
	out.WriteByte(' ')
	out.WriteString(rightBorder)
}

func (m *Model) contentWidth() int {
	if m.mode == modePopup {
		return max(m.width-popupViewportInset, 1)
	}

	return m.width
}

func (m *Model) contentHeight() int {
	if m.mode == modePopup {
		return max(m.height-popupViewportInset, 1)
	}

	return m.height
}

func (m *Model) insetPopupContent(content string) string {
	if m.mode != modePopup {
		return content
	}

	prefix := strings.Repeat(" ", popupViewportInset)

	var out strings.Builder
	out.Grow(len(content) + popupViewportInset*(strings.Count(content, "\n")+popupLineMargin))
	out.WriteString(strings.Repeat("\n", popupViewportInset))

	first := true
	for line := range strings.SplitSeq(content, "\n") {
		if !first {
			out.WriteByte('\n')
		}

		first = false

		out.WriteString(prefix)
		out.WriteString(line)
	}

	return out.String()
}

func (m *Model) calcPopupLayout(_ bool) popupLayout {
	bodyWidth := max(m.contentWidth()-popupBodyWidthMargin, 1)
	bodyHeight := max(m.contentHeight()-popupBodyHeightMargin, 1)

	reservedRows := 1

	wantsFooter := m.config.Settings.ShowFooter || m.showsFooterBadges()

	showFooter := wantsFooter && bodyHeight-reservedRows >= minFooterRows
	if showFooter {
		reservedRows++
	}

	detailRows := 0
	if m.displayMode == displayModeInbox && bodyHeight-reservedRows-inboxDetailRows >= minInboxListRows {
		detailRows = inboxDetailRows
		reservedRows += detailRows
	}

	listHeight := max(bodyHeight-reservedRows, 1)

	return popupLayout{
		bodyWidth:       bodyWidth,
		bodyHeight:      bodyHeight,
		listHeight:      listHeight,
		detailRows:      detailRows,
		showFooter:      showFooter,
		showFooterHints: showFooter && m.config.Settings.ShowFooter,
	}
}

func (m *Model) renderInputBar(width int) string {
	return inputBar(&m.textInput, width)
}

func inputBar(input *textinput.Model, width int) string {
	promptWidth := inputGutterWidth()
	prompt := ansiDefaultPrompt + ">" + ansiReset + strings.Repeat(" ", promptWidth-1)
	inputWidth := max(width-promptWidth-1, 0)

	originalWidth := input.Width()
	input.SetWidth(inputWidth)
	view := input.View()
	input.SetWidth(originalWidth)

	line := prompt + view

	lineWidth := promptWidth + lipgloss.Width(view)
	if lineWidth > width {
		return truncateStyled(line, width)
	}

	if lineWidth < width {
		line += strings.Repeat(" ", width-lineWidth)
	}

	return line
}

func (m *Model) renderStatusBadge() string {
	if !m.hasStatusBadge() {
		return ""
	}

	glyph := "⚠"
	color := ""

	if m.config != nil {
		color = m.config.Theme.Warning
	}

	if m.statusLevel == statusError {
		glyph = "✖"

		if m.config != nil {
			color = m.config.Theme.Error
		}
	}

	if color == "" {
		return glyph
	}

	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(color)).
		Bold(true).
		Render(glyph)
}

func (m *Model) renderLoadingState(width int) string {
	line := display.Truncate("Loading sessions...", width)
	if lineWidth := lipgloss.Width(line); lineWidth < width {
		line += strings.Repeat(" ", width-lineWidth)
	}

	if m.usesDefaultMutedTheme() {
		return ansiDefaultMuted + line + ansiReset
	}

	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.config.Theme.Muted)).
		Render(line)
}

func (m *Model) renderList(width, height int) string {
	layout := m.calcSessionLayout(width)

	if height <= 0 {
		return ""
	}

	styles := m.sessionRowStyles(layout)

	var out strings.Builder
	out.Grow((width + listBufferPadding) * height)

	rowCount := 0

	if m.popupStartup == popupStartupLoading || len(m.filtered) == 0 {
		if m.loadingSessions || m.popupStartup == popupStartupLoading {
			out.WriteString(m.renderLoadingState(width))
		} else {
			out.WriteString(m.renderEmptyState(width))
		}

		rowCount = 1
	} else {
		var values sessionFormatValues

		visible := m.visibleListRows(height)

		for _, listRow := range visible {
			if rowCount > 0 {
				out.WriteByte('\n')
			}

			m.writePopupListRow(&out, layout, styles, listRow, &values)

			rowCount++
		}
	}

	for ; rowCount < height; rowCount++ {
		if out.Len() > 0 {
			out.WriteByte('\n')
		}

		out.WriteString(styles.blankLine)
	}

	return out.String()
}

func (m *Model) writeListFramedSection(
	out *strings.Builder,
	width int,
	height int,
	leftBorder string,
	rightBorder string,
) {
	layout := m.calcSessionLayout(width)

	if height <= 0 {
		return
	}

	styles := m.sessionRowStyles(layout)
	rowCount := 0

	if m.popupStartup == popupStartupLoading || len(m.filtered) == 0 {
		if m.loadingSessions || m.popupStartup == popupStartupLoading {
			writeExactFramedLine(out, m.renderLoadingState(width), leftBorder, rightBorder)
		} else {
			writeExactFramedLine(out, m.renderEmptyState(width), leftBorder, rightBorder)
		}

		rowCount = 1
	} else {
		var values sessionFormatValues

		for _, listRow := range m.visibleListRows(height) {
			out.WriteByte('\n')
			out.WriteString(leftBorder)
			out.WriteByte(' ')

			m.writePopupListRow(out, layout, styles, listRow, &values)

			out.WriteByte(' ')
			out.WriteString(rightBorder)

			rowCount++
		}
	}

	for ; rowCount < height; rowCount++ {
		writeExactFramedLine(out, styles.blankLine, leftBorder, rightBorder)
	}
}

func (m *Model) writePopupListRow(
	out *strings.Builder,
	layout sessionTableLayout,
	styles sessionRowStyles,
	listRow popupListRow,
	values *sessionFormatValues,
) {
	selected := !listRow.isSeparator() && listRow.equal(m.selectedListRow())

	switch {
	case selected:
		m.writeListRowContent(out, layout, styles, listRow, true, values)
	case m.inFocusedSession(listRow):
		m.writeListRowContent(out, layout, focusedRowStyles(styles, listRow.isAgent()), listRow, true, values)
	case m.fadesListRow(listRow):
		var rendered strings.Builder

		m.writeListRowContent(&rendered, layout, styles, listRow, false, values)
		writeStyledWithBackground(out, ansi.Strip(rendered.String()), styles.fadedSeq)
	default:
		m.writeListRowContent(out, layout, styles, listRow, false, values)
	}
}

func (m *Model) writeListRowContent(
	out *strings.Builder,
	layout sessionTableLayout,
	styles sessionRowStyles,
	listRow popupListRow,
	selected bool,
	values *sessionFormatValues,
) {
	if listRow.isSeparator() {
		m.writeSeparatorRow(out, layout, styles)
		return
	}

	row := m.filtered[listRow.sessionIndex]

	if !listRow.isAgent() {
		m.writeSessionRowWithStyles(out, layout, styles, row, selected, values)
		return
	}

	m.writeFormattedSessionRow(
		out,
		layout,
		styles,
		row,
		row.Agents[listRow.agentIndex],
		true,
		listRow.agentIndex == len(row.Agents)-1,
		selected,
		values,
	)
}

func (m *Model) writeSeparatorRow(
	out *strings.Builder,
	layout sessionTableLayout,
	styles sessionRowStyles,
) {
	label := " Sessions "
	labelWidth := lipgloss.Width(label)
	totalWidth := layout.width

	var line string
	if totalWidth <= labelWidth {
		line = display.Truncate(label, totalWidth)
	} else {
		leftDashes := max((totalWidth-labelWidth)/separatorDashDivisor, minSeparatorDashes)
		rightDashes := max(totalWidth-labelWidth-leftDashes, 0)
		line = strings.Repeat("─", leftDashes) + label + strings.Repeat("─", rightDashes)
	}

	if styles.fastDefaultTheme {
		out.WriteString(ansiDefaultMuted)
		out.WriteString(line)
		out.WriteString(ansiReset)

		return
	}

	separatorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.config.Theme.Border))
	out.WriteString(separatorStyle.Render(line))
}

func (m *Model) calcSessionLayout(width int) sessionTableLayout {
	prefixWidth := lipgloss.Width(defaultSelectionBar)
	now := time.Now()

	reflow := agentLayoutReflow{width: width, displayMode: m.displayMode, uiMode: m.mode, query: m.textInput.Value()}
	if reflow != m.agentLayoutKey {
		m.agentRowRatchet.Reset()
		m.agentLayoutKey = reflow
	}

	var emptyAgentRows agentTableLayout

	layout := sessionTableLayout{
		width:         width,
		leftWidth:     0,
		rightWidth:    0,
		prefixWidth:   prefixWidth,
		agentAgeWidth: 0,
		agentRows:     emptyAgentRows,
		now:           now,
	}

	agents := m.measureAgentRows(layout)
	rightWidth := m.sessionFormatRightColumnWidth(layout)
	rightWidth = min(rightWidth, max(width-prefixWidth-frameBorderPadding, 0))

	gapWidth := 0
	if rightWidth > 0 {
		gapWidth = 1
	}

	layout.rightWidth = rightWidth
	layout.leftWidth = max(width-prefixWidth-gapWidth-rightWidth, 1)
	layout.agentRows = m.planAgentRows(layout, agents)

	return layout
}

func (m *Model) renderSessionRow(layout sessionTableLayout, s session.Session, selected bool) string {
	return m.renderSessionRowWithStyles(layout, m.sessionRowStyles(layout), s, selected)
}

func (m *Model) sessionRowStyles(layout sessionTableLayout) sessionRowStyles {
	return rowStyles(m.config, layout.prefixWidth, layout.width)
}

func rowStyles(cfg *config.Config, prefixWidth, width int) sessionRowStyles {
	blankPrefix := strings.Repeat(" ", prefixWidth)

	blankLine := strings.Repeat(" ", width)
	keys := sessionKeyStyles(cfg)

	if defaultRowTheme(cfg) {
		return sessionRowStyles{
			name:             lipgloss.Style{},
			nameSeq:          ansiDefaultName,
			selectedName:     lipgloss.Style{},
			selectedNameSeq:  ansiDefaultSelected,
			selectedPad:      lipgloss.Style{},
			selectedBar:      ansiDefaultSelectedBar,
			selectedBarGlyph: ansiDefaultSelectedBarGlyph,
			selectedBgSeq:    ansiDefaultSelectedBg,
			mutedStyle:       lipgloss.Style{},
			selectedMuted:    lipgloss.Style{},
			blankPrefix:      blankPrefix,
			blankLine:        blankLine,
			fastDefaultTheme: true,
			sessionKeySeq:    keys.sessionKeySeq,
			keyChipSeq:       keys.keyChipSeq,
			fadedSeq:         keys.fadedSeq,
		}
	}

	selectedBg := lipgloss.Color(cfg.Theme.Selected)

	fg := cfg.Theme.Foreground
	if fg == "" || strings.EqualFold(fg, "default") {
		fg = "252"
	}

	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(fg))
	selectedNameStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("255")).
		Background(selectedBg).
		Bold(true)
	selectedPad := lipgloss.NewStyle().Background(selectedBg)
	mutedColor := lipgloss.Color(cfg.Theme.Muted)

	return sessionRowStyles{
		fastDefaultTheme: false,
		name:             nameStyle,
		nameSeq:          extractStylePrefix(nameStyle),
		selectedName:     selectedNameStyle,
		selectedNameSeq:  extractStylePrefix(selectedNameStyle),
		selectedBar: lipgloss.NewStyle().
			Foreground(lipgloss.Color(shortcutKeyColor)).
			Background(selectedBg).
			Bold(true).
			Render(defaultSelectionBar),
		selectedBarGlyph: lipgloss.NewStyle().
			Foreground(lipgloss.Color(shortcutKeyColor)).
			Background(selectedBg).
			Bold(true).
			Render("▌"),
		selectedPad:   selectedPad,
		selectedBgSeq: extractStylePrefix(selectedPad),
		blankPrefix:   blankPrefix,
		blankLine:     blankLine,
		mutedStyle:    lipgloss.NewStyle().Foreground(mutedColor),
		selectedMuted: lipgloss.NewStyle().Foreground(mutedColor).Background(selectedBg),
		sessionKeySeq: keys.sessionKeySeq,
		keyChipSeq:    keys.keyChipSeq,
		fadedSeq:      keys.fadedSeq,
	}
}

func (m *Model) usesDefaultMutedTheme() bool {
	return defaultMutedTheme(m.config)
}

func defaultRowTheme(cfg *config.Config) bool {
	if cfg == nil {
		return true
	}

	fg := cfg.Theme.Foreground
	if fg != "" && !strings.EqualFold(fg, "default") && fg != "252" {
		return false
	}

	return cfg.Theme.Secondary == "#ffdd57" && cfg.Theme.Selected == "#2a2a2a"
}

func defaultBorderTheme(cfg *config.Config) bool {
	return strings.EqualFold(cfg.Theme.Border, "#3a3d45")
}

func defaultMutedTheme(cfg *config.Config) bool {
	return strings.EqualFold(cfg.Theme.Muted, "#6c7086")
}

func (m *Model) renderSessionRowWithStyles(
	layout sessionTableLayout,
	styles sessionRowStyles,
	s session.Session,
	selected bool,
) string {
	var values sessionFormatValues

	var out strings.Builder
	out.Grow(layout.width)
	m.writeSessionRowWithStyles(&out, layout, styles, s, selected, &values)

	return out.String()
}

func (m *Model) writeSessionRowWithStyles(
	out *strings.Builder,
	layout sessionTableLayout,
	styles sessionRowStyles,
	s session.Session,
	selected bool,
	values *sessionFormatValues,
) {
	m.writeFormattedSessionRow(
		out,
		layout,
		styles,
		s,
		emptyAgentSession(),
		false,
		false,
		selected,
		values,
	)
}

func (m *Model) FormatSessionRow(row session.Session) string {
	layout := m.calcSessionLayout(max(m.contentWidth()-statusWidthInset, defaultSessionMinWidth))
	return m.renderSessionRow(layout, row, false)
}

func (m *Model) FormatAgentRow(row session.Session, agent session.AgentSession) string {
	layout := m.calcSessionLayout(max(m.contentWidth()-statusWidthInset, defaultSessionMinWidth))

	var values sessionFormatValues

	var out strings.Builder
	out.Grow(layout.width)
	m.writeFormattedSessionRow(
		&out,
		layout,
		m.sessionRowStyles(layout),
		row,
		agent,
		true,
		false,
		false,
		&values,
	)

	return out.String()
}

func (m *Model) RenderView() string {
	return m.viewContent()
}

func (m *Model) writeFormattedSessionRow(
	out *strings.Builder,
	layout sessionTableLayout,
	styles sessionRowStyles,
	row session.Session,
	agent session.AgentSession,
	hasAgent bool,
	isLastAgent bool,
	selected bool,
	values *sessionFormatValues,
) {
	if hasAgent && layout.agentRows.valid {
		m.writePlannedAgentRow(out, layout, styles, row, agent, isLastAgent, selected, values)
		return
	}

	line := m.formattedSessionLine(row, agent, hasAgent, isLastAgent, layout, values)

	gutter := ""
	if !hasAgent && layout.leftWidth > sessionKeyGutterWidth {
		gutter = m.sessionKeyGutter()
	}

	leftWidth := max(layout.leftWidth-lipgloss.Width(gutter), 0)

	left := fitSessionFormatParts(line.Left, leftWidth)
	if width := lipgloss.Width(left); width < leftWidth {
		left += strings.Repeat(" ", leftWidth-width)
	}

	out.WriteString(rowPrefix(styles, m.rowKey(row, agent, hasAgent), selected))

	if gutter != "" && selected {
		writeStyledWithBackground(out, gutter, styles.selectedBgSeq)
	} else {
		out.WriteString(gutter)
	}

	if hasAgent {
		m.writeAgentText(out, left, selected, styles)
	} else {
		m.writePrimarySessionText(out, left, selected, styles)
	}

	m.writeSessionFormatRight(out, line, layout, hasAgent, selected, styles)
}

func (m *Model) writePrimarySessionText(
	out *strings.Builder,
	value string,
	selected bool,
	styles sessionRowStyles,
) {
	if styles.fastDefaultTheme {
		style := ansiDefaultName
		if selected {
			style = ansiDefaultSelected
		}

		writeStyledWithBackground(out, value, style)

		return
	}

	styleSeq := styles.nameSeq
	if selected {
		styleSeq = styles.selectedNameSeq
	}

	writeStyledWithBackground(out, value, styleSeq)
}

func (m *Model) writeSessionFormatRight(
	out *strings.Builder,
	line display.Line,
	layout sessionTableLayout,
	isAgent bool,
	selected bool,
	styles sessionRowStyles,
) {
	if layout.rightWidth <= 0 {
		return
	}

	right := fitSessionFormatParts(line.Right, layout.rightWidth)
	if rightWidth := lipgloss.Width(right); rightWidth < layout.rightWidth {
		right = strings.Repeat(" ", layout.rightWidth-rightWidth) + right
	}

	value := " " + right

	if selected {
		writeStyledWithBackground(out, value, styles.selectedBgSeq)

		return
	}

	if isAgent {
		out.WriteByte(' ')
		m.writeMutedAgentText(out, right)

		return
	}

	out.WriteString(value)
}

func agentCountLabel(s session.Session) string {
	return fmt.Sprintf("%d/%d", s.AgentActive, s.AgentTotal)
}

func (m *Model) agentStatusStyle() string {
	if m.config != nil && strings.EqualFold(strings.TrimSpace(m.config.AgentSessions.StatusStyle), "text") {
		return "text"
	}

	return "glyph"
}

func (m *Model) agentStatusTimeFormat() string {
	if m.config == nil {
		return "relative-seconds"
	}

	return m.config.AgentSessions.NormalizedStatusTimeFormat()
}

func (m *Model) agentIdleUnread(row session.Session, agent session.AgentSession) bool {
	if agent.Status.DisplayStatus() != session.AgentStatusIdle || agent.InitialIdle || agent.StatusSince.IsZero() {
		return false
	}

	if m.hasActiveAgentView(row, agent) {
		return false
	}

	lastViewed := int64(0)
	if m.state != nil {
		lastViewed = m.state.GetLastAgentView(row.SemanticIdentity(), agent.PaneID)
	}

	return lastViewed < agent.StatusSince.UnixNano()
}

func (m *Model) hasActiveAgentView(row session.Session, agent session.AgentSession) bool {
	if row.ActivePaneID == "" || row.ActivePaneID != agent.PaneID {
		return false
	}

	if row.Current {
		return true
	}

	if !row.Activity.IsZero() && !row.Activity.Before(agent.StatusSince) {
		return true
	}

	if m.state != nil {
		var lastSwitch int64
		if row.Origin == session.OriginSSH {
			lastSwitch = m.state.GetLastRemoteSwitch(row.SemanticIdentity())
		} else {
			lastSwitch = m.state.GetLastSwitchIdentity(row.SemanticIdentity())
		}

		if lastSwitch >= agent.StatusSince.Unix() {
			return true
		}
	}

	return false
}

func displayedAgentStatusLabel(status session.AgentStatus, unreadIdle bool) string {
	if status.DisplayStatus() == session.AgentStatusIdle && unreadIdle {
		return "IDLE*"
	}

	return agentStatusLabel(status)
}

func (m *Model) displayedAgentStatusGlyph(status session.AgentStatus, unreadIdle bool) string {
	if status.DisplayStatus() != session.AgentStatusIdle || !unreadIdle {
		return m.agentStatusGlyph(status)
	}

	const glyph = "◎"
	if m.config == nil || m.config.Theme.Primary == "" {
		return glyph
	}

	return lipgloss.NewStyle().Foreground(lipgloss.Color(m.config.Theme.Primary)).Render(glyph)
}

func (m *Model) agentStatusGlyph(status session.AgentStatus) string {
	glyph := agentStatusGlyphChar(status)

	color := m.agentStatusColor(status)
	if color == "" {
		return glyph
	}

	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(glyph)
}

func agentStatusGlyphChar(status session.AgentStatus) string {
	switch status.DisplayStatus() {
	case session.AgentStatusRunning:
		return "●"
	case session.AgentStatusWaiting:
		return "●"
	case session.AgentStatusFailed:
		return "●"
	case session.AgentStatusIdle, session.AgentStatusInterrupted:
		return "○"
	case session.AgentStatusUnknown:
		return "○"
	case session.AgentStatusSleeping:
		return "☾"
	case session.AgentStatusClosed:
		return "◌"
	case session.AgentStatusMerged:
		return "✓"
	default:
		return "○"
	}
}

func (m *Model) agentStatusColor(status session.AgentStatus) string {
	if m.config == nil {
		return ""
	}

	colors := m.config.ResolvedAgentStatusColors()

	switch status.DisplayStatus() {
	case session.AgentStatusRunning:
		return colors.Running
	case session.AgentStatusWaiting:
		return colors.Waiting
	case session.AgentStatusFailed:
		return colors.Failed
	case session.AgentStatusIdle, session.AgentStatusInterrupted, session.AgentStatusUnknown, session.AgentStatusSleeping,
		session.AgentStatusClosed:
		return colors.Idle
	case session.AgentStatusMerged:
		return m.config.Theme.Success
	default:
		return colors.Idle
	}
}

func (m *Model) agentContextLabelAt(s session.Session) string {
	if m.displayMode == displayModeSesh {
		return agentCountLabel(s)
	}

	return ""
}

func (m *Model) formattedAgentContextAt(s session.Session, now time.Time, ageWidth int) string {
	status, statusEnum, age := m.agentStatusAndAgeAt(s, now)

	if m.agentStatusStyle() == "text" {
		if ageWidth > 0 {
			return fmt.Sprintf("%-7s  %*s", status, ageWidth, age)
		}

		if age != "" {
			return status + " " + age
		}

		return status
	}

	glyph := m.agentStatusGlyph(statusEnum)

	label := glyph
	if ageWidth > 0 {
		label += fmt.Sprintf("  %*s", ageWidth, age)
	} else if age != "" {
		label += " " + age
	}

	return label
}

func formattedAgentContextAt(s session.Session, now time.Time, ageWidth int) string {
	m := emptyModelForRendering()
	return m.formattedAgentContextAt(s, now, ageWidth)
}

func (m *Model) agentStatusAndAgeAt(s session.Session, now time.Time) (string, session.AgentStatus, string) {
	status := "IDLE"
	statusEnum := session.AgentStatusIdle
	since := s.AgentIdleSince

	switch {
	case s.AgentFailed > 0:
		status = "ERROR"
		statusEnum = session.AgentStatusFailed
		since = time.Time{}
	case s.AgentWaiting > 0:
		status = "WAITING"
		statusEnum = session.AgentStatusWaiting
		since = s.AgentWaitingSince
	case s.AgentActive > 0:
		status = "RUNNING"
		statusEnum = session.AgentStatusRunning
		since = time.Time{}
	case s.AgentUnknown > 0:
		status = "UNKNOWN"
		statusEnum = session.AgentStatusUnknown
		since = time.Time{}
	}

	age := ""
	if !since.IsZero() {
		age = m.formatAgentStatusAgeAt(since, now)
	}

	return status, statusEnum, age
}

func capitalizedHarness(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return value
	}

	runes := []rune(value)
	runes[0] = unicode.ToUpper(runes[0])

	return string(runes)
}

func (m *Model) writeAgentText(
	out *strings.Builder,
	value string,
	selected bool,
	styles sessionRowStyles,
) {
	if !selected {
		m.writeMutedAgentText(out, value)
		return
	}

	if styles.fastDefaultTheme {
		writeStyledWithBackground(out, value, ansiDefaultSelected)

		return
	}

	writeStyledWithBackground(out, value, styles.selectedNameSeq)
}

func (m *Model) writeMutedAgentText(out *strings.Builder, value string) {
	if m.usesDefaultMutedTheme() {
		writeStyledWithBackground(out, value, ansiDefaultMuted)

		return
	}

	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.config.Theme.Muted))
	writeStyledWithBackground(out, value, extractStylePrefix(mutedStyle))
}

func (m *Model) formattedAgentSessionContextAt(
	agent session.AgentSession,
	now time.Time,
	ageWidth int,
	isActivePane bool,
	unreadIdle bool,
) string {
	marker := "  "
	if isActivePane {
		marker = "* "
	}

	age := m.agentStatusAgeAt(agent, now)
	if m.agentStatusStyle() == "text" {
		status := displayedAgentStatusLabel(agent.Status, unreadIdle)

		label := marker + fmt.Sprintf("%-7s", status)
		if ageWidth > 0 {
			label += fmt.Sprintf("  %*s", ageWidth, age)
		}

		return label
	}

	glyph := m.displayedAgentStatusGlyph(agent.Status, unreadIdle)

	label := marker + glyph
	if ageWidth > 0 {
		label += fmt.Sprintf("  %*s", ageWidth, age)
	} else if age != "" {
		label += " " + age
	}

	return label
}

func formattedAgentSessionContextAt(
	agent session.AgentSession,
	now time.Time,
	ageWidth int,
	isActivePane bool,
) string {
	m := emptyModelForRendering()
	return m.formattedAgentSessionContextAt(agent, now, ageWidth, isActivePane, false)
}

func (m *Model) compactAgentSessionContextAt(
	agent session.AgentSession,
	now time.Time,
	isActivePane bool,
	unreadIdle bool,
) string {
	marker := "  "
	if isActivePane {
		marker = "* "
	}

	var label string
	if m.agentStatusStyle() == "text" {
		label = marker + displayedAgentStatusLabel(agent.Status, unreadIdle)
	} else {
		label = marker + m.displayedAgentStatusGlyph(agent.Status, unreadIdle)
	}

	if age := m.agentStatusAgeAt(agent, now); age != "" {
		label += " " + age
	}

	return label
}

func agentStatusLabel(status session.AgentStatus) string {
	switch status.DisplayStatus() {
	case session.AgentStatusRunning:
		return "RUNNING"
	case session.AgentStatusWaiting:
		return "WAITING"
	case session.AgentStatusFailed:
		return "ERROR"
	case session.AgentStatusIdle, session.AgentStatusInterrupted:
		return "IDLE"
	case session.AgentStatusUnknown:
		return "UNKNOWN"
	case session.AgentStatusSleeping:
		return "SLEEPING"
	case session.AgentStatusClosed:
		return "CLOSED"
	case session.AgentStatusMerged:
		return "MERGED"
	default:
		return "UNKNOWN"
	}
}

func (m *Model) agentStatusAgeAt(agent session.AgentSession, now time.Time) string {
	if agent.StatusSince.IsZero() {
		return ""
	}

	return m.formatAgentStatusAgeAt(agent.StatusSince, now)
}

func (m *Model) formatAgentStatusAgeAt(t time.Time, now time.Time) string {
	return formatAgentStatusAgeAt(t, now, m.agentStatusTimeFormat())
}

func extractStylePrefix(style lipgloss.Style) string {
	prefix, _, ok := strings.Cut(style.Render("X"), "X")
	if !ok {
		return ""
	}

	return prefix
}

func writeStyledWithBackground(out *strings.Builder, text, styleSeq string) {
	if styleSeq == "" {
		out.WriteString(text)

		return
	}

	out.WriteString(styleSeq)

	if !strings.Contains(text, "\x1b") {
		out.WriteString(text)
		out.WriteString(ansiReset)

		return
	}

	for {
		idx := strings.Index(text, "\x1b[")
		if idx < 0 {
			out.WriteString(text)

			break
		}

		out.WriteString(text[:idx])
		text = text[idx:]

		end := strings.IndexByte(text, 'm')
		if end < 0 {
			out.WriteString(text)

			break
		}

		seq := text[:end+1]
		text = text[end+1:]

		out.WriteString(seq)

		if seq == "\x1b[m" || seq == "\x1b[0m" {
			out.WriteString(styleSeq)
		}
	}

	out.WriteString(ansiReset)
}

func inputGutterWidth() int {
	return lipgloss.Width(defaultSelectionBar)
}

func (m *Model) renderEmptyState(width int) string {
	emptyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.config.Theme.Muted))

	message := display.Truncate(m.emptyStateMessage(), width)
	if messageWidth := lipgloss.Width(message); messageWidth < width {
		message += strings.Repeat(" ", width-messageWidth)
	}

	return emptyStyle.Render(message)
}

func (m *Model) emptyStateMessage() string {
	if query := strings.TrimSpace(m.textInput.Value()); query != "" {
		return m.noMatchesMessage(query)
	}

	if m.sessionLoadErr != nil && len(m.sessions) == 0 {
		return fmt.Sprintf("Failed to load sessions: %v", m.sessionLoadErr)
	}

	switch m.displayMode {
	case displayModeInbox:
		return m.inboxEmptyMessage()
	case displayModeAgents:
		switch {
		case !m.config.AgentSessions.Enabled:
			return "Agent tracking is disabled"
		case m.agentSummaryErr != nil:
			return fmt.Sprintf("Unable to load agent sessions: %v", m.agentSummaryErr)
		default:
			return "No live agent sessions found"
		}
	case displayModeSesh:
		return "No sessions found"
	default:
		return "No sessions found"
	}
}

func (m *Model) inboxEmptyMessage() string {
	switch {
	case m.tasksErr != nil:
		return fmt.Sprintf("Unable to load tasks: %v", m.tasksErr)
	case m.inboxErr != nil:
		return fmt.Sprintf("Unable to load inbox state: %v", m.inboxErr)
	case !m.config.AgentSessions.Enabled:
		return "Agent tracking is disabled"
	default:
		return "Inbox is empty"
	}
}

func (m *Model) noMatchesMessage(query string) string {
	switch m.displayMode {
	case displayModeInbox:
		return fmt.Sprintf("No inbox rows match %q", query)
	case displayModeAgents:
		return fmt.Sprintf("No agent sessions match %q", query)
	case displayModeSesh:
	}

	if key := newActionKeyMap(m.config).actionKey(action.SessionCreate.ID); key != "" {
		return fmt.Sprintf("No matches. Press %s to create %q", key, query)
	}

	return fmt.Sprintf("No matches for %q", query)
}

func (m *Model) renderFooter(width int) string {
	footerWidth := max(width, 1)

	return renderFooterLine(m.footerParts(footerWidth), footerWidth, m.footerHoverDesc)
}

func (m *Model) renderModeFooter(width int, showHints bool) string {
	if width <= 0 {
		return ""
	}

	if !m.showsFooterBadges() {
		if showHints {
			return m.renderFooter(width)
		}

		return strings.Repeat(" ", width)
	}

	badge := m.renderFooterBadges()

	badgeWidth := lipgloss.Width(badge)
	if badgeWidth >= width {
		return truncateStyled(badge, width)
	}

	prefixWidth := width - badgeWidth
	prefix := strings.Repeat(" ", prefixWidth)

	if m.hasStatusBadge() && m.statusBadgeHovered {
		prefix = m.renderStatusHoverPrefix(prefixWidth)
	} else if showHints {
		hintWidth := m.modeFooterHintWidth(width)
		if hintWidth > 0 {
			prefix = m.renderFooter(hintWidth) + strings.Repeat(" ", prefixWidth-hintWidth)
		}
	}

	return prefix + badge
}

func (m *Model) renderStatusHoverPrefix(width int) string {
	color := ""

	if m.config != nil {
		color = m.config.Theme.Warning
		if m.statusLevel == statusError {
			color = m.config.Theme.Error
		}
	}

	statusStyle := lipgloss.NewStyle().Bold(true)
	if color != "" {
		statusStyle = statusStyle.Foreground(lipgloss.Color(color))
	}

	statusLine := display.Truncate(SanitizeTerminalText(m.statusMessage), max(width-frameBorderPadding, 1))
	prefix := statusStyle.Render(statusLine)

	if prefixLen := lipgloss.Width(prefix); prefixLen < width {
		prefix += strings.Repeat(" ", width-prefixLen)
	}

	return prefix
}

func (m *Model) renderModeBadge() string {
	return modeBadge(m.config, m.displayMode.label())
}

func modeBadge(cfg *config.Config, label string) string {
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color("0")).
		Background(lipgloss.Color(cfg.Theme.Primary)).
		Bold(true).
		Render(" " + label + " ")
}

func (m *Model) modeFooterHintWidth(width int) int {
	if !m.showsFooterBadges() {
		return max(width, 0)
	}

	return max(width-lipgloss.Width(m.renderFooterBadges())-frameBorderPadding, 0)
}

func (m *Model) footerParts(width int) []footerPart {
	footerWidth := max(width, 1)
	keys := newActionKeyMap(m.config)

	if footerWidth < minFullFooterWidth {
		return []footerPart{newFooterPart(keys.help.Help().Key, "help")}
	}

	if parts := m.sessionKeyStepFooterParts(keys); parts != nil {
		for len(parts) > 0 && footerPartsWidth(parts) > footerWidth {
			parts = parts[:len(parts)-1]
		}

		return parts
	}

	if footerWidth < compactFooterMaxWidth && m.displayMode.tree() {
		return m.compactAgentFooterParts(keys, footerWidth)
	}

	parts := m.baseFooterParts()
	parts = append(parts, newFooterPart(keys.enter.Help().Key, "select"))

	if m.showsSessionKeys() {
		parts = append(parts, m.sessionKeyFooterPart())
	}

	for _, bound := range m.footerActions(keys) {
		parts = append(parts, newActionFooterPart(bound))
	}

	parts = append(parts, newFooterPart("esc", "cancel"))
	parts = append(parts, newFooterPart(keys.help.Help().Key, "help"))
	parts = m.withoutSessionKeyHintWhenTooWide(parts, footerWidth)

	for len(parts) > 0 && footerPartsWidth(parts) > footerWidth {
		parts = parts[:len(parts)-1]
	}

	return parts
}

func (m *Model) baseFooterParts() []footerPart {
	parts := make([]footerPart, 0, initialFooterPartsCap)
	if m.displayMode == displayModeAgents {
		return append(parts,
			newFooterPart("↑/↓", "agents"),
			newFooterPart("ctrl+↑/↓", "sessions"),
		)
	}

	if m.displayMode == displayModeInbox {
		return append(parts,
			newFooterPart("↑/↓", "agents"),
			newFooterPart(newActionKeyMap(m.config).menu.Help().Key, "menu"),
		)
	}

	return append(parts, newFooterPart("↑/↓", "sessions"))
}

func (m *Model) compactAgentFooterParts(keys actionKeyMap, width int) []footerPart {
	parts := []footerPart{
		newFooterPart("↑/↓", "agent"),
		newFooterPart("⌃↑/↓", "session"),
		newFooterPart(keys.enter.Help().Key, "select"),
	}

	if m.showsSessionKeys() {
		parts = append(parts, m.sessionKeyFooterPart())
	}

	parts = append(parts,
		newFooterPart(keys.help.Help().Key, "help"),
	)

	parts = m.withoutSessionKeyHintWhenTooWide(parts, width)

	if footerPartsWidth(parts) > width {
		parts = slices.Delete(parts, 1, sessionPartEndIndex)
	}

	return parts
}

func newFooterPart(key string, desc string) footerPart {
	return footerPart{
		key:     key,
		desc:    desc,
		width:   lipgloss.Width(key) + 1 + lipgloss.Width(desc),
		binding: action.Binding{Key: "", Action: "", In: "", Table: "", Values: nil},
	}
}

func newActionFooterPart(bound boundAction) footerPart {
	label := string(bound.binding.Action)
	if entry, ok := action.Lookup(bound.binding.Action); ok {
		label, _, _ = strings.Cut(strings.ToLower(entry.Describe().Title), " ")
	}

	if name := bound.binding.Values.String("action"); bound.binding.Action == action.TaskRun.ID && name != "" {
		label = name
	}

	part := newFooterPart(bound.key.Help().Key, label)
	part.binding = bound.binding

	return part
}

func footerPartsWidth(parts []footerPart) int {
	width := 0

	for i, part := range parts {
		if i > 0 {
			width += 2
		}

		width += part.width
	}

	return width
}

func renderFooterLine(parts []footerPart, width int, hoverDesc string) string {
	if width <= 0 {
		return ""
	}

	var out strings.Builder

	for i, part := range parts {
		if i > 0 {
			out.WriteString("  ")
		}

		writeFooterPart(&out, part, part.desc == hoverDesc)
	}

	line := out.String()

	lineWidth := footerPartsWidth(parts)
	if lineWidth >= width {
		return truncateStyled(line, width)
	}

	return line + strings.Repeat(" ", width-lineWidth)
}

func writeFooterPart(out *strings.Builder, part footerPart, hovered bool) {
	keyStyle := ansiFooterKey
	descStyle := ansiFooterDesc

	if hovered {
		keyStyle = ansiFooterHoverKey
		descStyle = ansiFooterHoverDesc
	}

	out.WriteString(keyStyle)
	out.WriteString(part.key)
	out.WriteString(ansiFooterKeyReset)

	if hovered {
		out.WriteString(descStyle)
		out.WriteByte(' ')
	} else {
		out.WriteByte(' ')
		out.WriteString(descStyle)
	}

	out.WriteString(part.desc)
	out.WriteString(ansiReset)
}

func emptyModelForRendering() *Model {
	var m Model

	return &m
}
