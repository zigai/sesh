package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zigai/sesh/internal/display"
	rowlayout "github.com/zigai/sesh/internal/display/layout"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/tui/providers"
)

const (
	agentMinimumFlexWidth    = 3
	agentMinimumContextWidth = 8
	agentMinimumGitWidth     = 6

	columnPriorityRequired      = 0
	columnPriorityLeft          = 20
	columnPriorityLeftShrink    = 25
	columnPriorityLeftSecondary = 30
	columnPriorityRight         = 40
	columnPriorityRightMeta     = 45
	columnPriorityGitBranch     = 50
	columnPriorityGitChanges    = 60
	columnPriorityGitTracking   = 70
)

type agentTableLayout struct {
	plan       rowlayout.ColumnPlan
	theme      rowlayout.Theme
	rightStart int
	valid      bool
}

type agentLayoutReflow struct {
	width       int
	displayMode displayMode
	uiMode      uiMode
	query       string
}

type agentRowMeasurements struct {
	specs      []rowlayout.ColumnSpec
	rows       [][]rowlayout.Cell
	rightStart int
}

func (m *Model) measureAgentRows(table sessionTableLayout) agentRowMeasurements {
	m.publishAHTCells(table.now)
	m.publishClockCells(table.now)

	var specs []rowlayout.ColumnSpec

	var rows [][]rowlayout.Cell

	var values *sessionFormatValues

	rightStart := 0

	configured := m.configuredColumnPriority()

	for _, row := range m.filtered {
		for index, agent := range row.Agents {
			if values == nil {
				values = new(sessionFormatValues)
			}

			left, right := m.agentLayoutCells(table, row, agent, index == len(row.Agents)-1, values)
			if len(rows) == 0 {
				leftSpecs := agentColumnSpecs(left, false, configured)
				rightSpecs := agentColumnSpecs(right, true, configured)

				specs = append(specs, leftSpecs...)
				specs = append(specs, agentSpacerSpec(rightSpecs))
				specs = append(specs, rightSpecs...)
				rightStart = len(left) + 1
			}

			cells := agentCells(*values, left)
			cells = append(cells, spacerCell(len(right) > 0))
			cells = append(cells, agentCells(*values, right)...)
			rows = append(rows, cells)
		}
	}

	return agentRowMeasurements{specs: specs, rows: rows, rightStart: rightStart}
}

func (m *Model) configuredColumnPriority() []string {
	if m == nil || m.config == nil {
		return nil
	}

	return m.config.Display.ColumnPriority
}

func spacerCell(hasRight bool) rowlayout.Cell {
	if !hasRight {
		return rowlayout.Cell{Text: "", Role: "", Variants: nil, State: rowlayout.Absent}
	}

	return rowlayout.Cell{Text: " ", Role: "", Variants: nil, State: rowlayout.Present}
}

func agentSpacerSpec(rightSpecs []rowlayout.ColumnSpec) rowlayout.ColumnSpec {
	if len(rightSpecs) == 0 {
		return rowlayout.ColumnSpec{
			ID: "right_spacer", Align: rowlayout.Left, Min: 0, FlexMin: 0, Max: 0,
			Reserve: 0, Flex: false, Grow: false,
			ShrinkPriority: columnPriorityRight, HidePriority: columnPriorityRight,
			TruncateSide: rowlayout.TruncateRight, Group: "", Separator: false,
			AttachedTo: "",
		}
	}

	priority := rightSpecs[0].HidePriority
	for _, spec := range rightSpecs[1:] {
		priority = min(priority, spec.HidePriority)
	}

	return rowlayout.ColumnSpec{
		ID: "right_spacer", Align: rowlayout.Left, Min: 1, FlexMin: 0, Max: 0,
		Reserve: 1, Flex: false, Grow: true,
		ShrinkPriority: priority, HidePriority: priority,
		TruncateSide: rowlayout.TruncateRight, Group: "", Separator: false,
		AttachedTo: "",
	}
}

func (m *Model) planAgentRows(
	table sessionTableLayout,
	measured agentRowMeasurements,
) agentTableLayout {
	if len(measured.rows) == 0 {
		var empty rowlayout.ColumnPlan
		return agentTableLayout{plan: empty, theme: nil, rightStart: 0, valid: false}
	}

	plan := rowlayout.Plan(
		measured.specs,
		rowlayout.Measure(measured.specs, measured.rows),
		max(table.width-table.prefixWidth, 0),
		&m.agentRowRatchet,
		table.now,
	)

	return agentTableLayout{
		plan:       plan,
		theme:      m.agentLayoutTheme(),
		rightStart: measured.rightStart,
		valid:      true,
	}
}

func (m *Model) agentLayoutCells(
	table sessionTableLayout,
	row session.Session,
	agent session.AgentSession,
	last bool,
	values *sessionFormatValues,
) ([]display.Part, []display.Part) {
	table.rightWidth = 0
	table.agentAgeWidth = 0

	var template display.Template

	template, *values = m.sessionRowFormatter(row, agent, true, last, table)
	line := template.Render(values)

	return line.Left, line.Right
}

func agentColumnSpecs(
	parts []display.Part,
	rightSide bool,
	configured []string,
) []rowlayout.ColumnSpec {
	specs := make([]rowlayout.ColumnSpec, len(parts))
	for index, part := range parts {
		specs[index] = agentColumnSpec(index, part, rightSide, configured)
	}

	attachLiteralSpecs(specs, parts)

	return specs
}

func agentColumnSpec(
	index int,
	part display.Part,
	rightSide bool,
	configured []string,
) rowlayout.ColumnSpec {
	info, _ := display.FieldInfo(part.Field)

	maxWidth := info.WidthBound
	if part.HasMaxWidth {
		maxWidth = part.MaxWidth
	}

	flex, flexMin := columnFlexBounds(part.Field, info)

	truncate := rowlayout.TruncateRight
	if info.TruncateLeft || part.Alignment == display.AlignmentRight {
		truncate = rowlayout.TruncateLeft
	}

	sidePrefix := "L"
	if rightSide {
		sidePrefix = "R"
	}

	hidePriority, shrinkPriority := columnPriorities(part.Field, info, rightSide, configured)

	return rowlayout.ColumnSpec{
		ID:             fmt.Sprintf("%s%d:%s", sidePrefix, index, part.Field),
		Align:          agentAlignment(part.Alignment),
		Min:            part.Width,
		FlexMin:        flexMin,
		Max:            maxWidth,
		Reserve:        info.WidthBound,
		Flex:           flex,
		Grow:           false,
		ShrinkPriority: shrinkPriority,
		HidePriority:   hidePriority,
		TruncateSide:   truncate,
		Group:          part.Group,
		Separator:      part.Separator,
		AttachedTo:     "",
	}
}

func attachLiteralSpecs(specs []rowlayout.ColumnSpec, parts []display.Part) {
	for index, part := range parts {
		if part.Field != "" || part.Group != "" {
			continue
		}

		owner := adjacentFieldIndex(parts, index)
		if owner < 0 {
			continue
		}

		specs[index].AttachedTo = specs[owner].ID
		specs[index].HidePriority = specs[owner].HidePriority
		specs[index].ShrinkPriority = specs[owner].ShrinkPriority
	}
}

func adjacentFieldIndex(parts []display.Part, index int) int {
	for next := index + 1; next < len(parts); next++ {
		if parts[next].Field != "" {
			return next
		}
	}

	for prev := index - 1; prev >= 0; prev-- {
		if parts[prev].Field != "" {
			return prev
		}
	}

	return -1
}

func columnFlexBounds(field string, info display.FieldMetadata) (bool, int) {
	if info.Flexible {
		return true, agentMinimumFlexWidth
	}

	switch field {
	case display.FieldAgentCWDName,
		display.FieldAgentProjectName,
		display.FieldAgentCWD,
		display.FieldAgentProjectRoot:
		return true, agentMinimumContextWidth
	case display.FieldAgentGitLabel,
		display.FieldAgentGitBranch,
		display.FieldAgentGitInfo,
		display.FieldAgentWorktree,
		display.FieldAgentWorktreeName:
		return true, agentMinimumGitWidth
	default:
		return false, 0
	}
}

func agentAlignment(align display.Alignment) rowlayout.Alignment {
	switch align {
	case display.AlignmentRight:
		return rowlayout.Right
	case display.AlignmentCenter:
		return rowlayout.Center
	case display.AlignmentNone, display.AlignmentLeft:
		return rowlayout.Left
	}

	return rowlayout.Left
}

func columnPriorities(
	field string,
	info display.FieldMetadata,
	rightSide bool,
	configured []string,
) (int, int) {
	if field == "" {
		base := columnPriorityLeft
		if rightSide {
			base = columnPriorityRight
		}

		priority := len(configured) + base

		return priority, priority
	}

	if idx := slices.Index(configured, field); idx >= 0 {
		return idx, idx
	}

	base := defaultColumnPriority(field, info, rightSide)
	shrink := base

	if !rightSide && isContextShrinkField(field) {
		shrink = columnPriorityLeftShrink
	}

	offset := len(configured)

	return offset + base, offset + shrink
}

func defaultColumnPriority(field string, info display.FieldMetadata, rightSide bool) int {
	if isRequiredColumnField(field) {
		return columnPriorityRequired
	}

	if !rightSide {
		if info.Provider == display.ProviderClock {
			return columnPriorityLeftSecondary
		}

		return columnPriorityLeft
	}

	if info.Provider == display.ProviderGit {
		return defaultGitColumnPriority(field)
	}

	switch field {
	case display.FieldAgentPR, display.FieldAgentCI, display.FieldEvidence, display.FieldHost:
		return columnPriorityRightMeta
	default:
		return columnPriorityRight
	}
}

func isRequiredColumnField(field string) bool {
	switch field {
	case display.FieldTree,
		display.FieldTreeBranch,
		display.FieldTreeIndent,
		display.FieldStatus,
		display.FieldStatusGlyph,
		display.FieldStatusText,
		display.FieldActiveMarker,
		display.FieldCurrentMarker,
		display.FieldAttachedMarker,
		display.FieldShortcut,
		display.FieldSessionKey:
		return true
	default:
		return false
	}
}

func isContextShrinkField(field string) bool {
	switch field {
	case display.FieldAgentCWDName,
		display.FieldAgentProjectName,
		display.FieldAgentCWD,
		display.FieldAgentProjectRoot:
		return true
	default:
		return false
	}
}

func defaultGitColumnPriority(field string) int {
	switch field {
	case display.FieldAgentGitAhead, display.FieldAgentGitBehind:
		return columnPriorityGitTracking
	case display.FieldAgentGitAdded,
		display.FieldAgentGitDeleted,
		display.FieldAgentGitChanges,
		display.FieldAgentGitDirty,
		display.FieldAgentGitConflicts,
		display.FieldAgentGitStash:
		return columnPriorityGitChanges
	default:
		return columnPriorityGitBranch
	}
}

func agentCells(values sessionFormatValues, parts []display.Part) []rowlayout.Cell {
	cells := make([]rowlayout.Cell, len(parts))
	for i, part := range parts {
		cells[i] = agentCell(values, parts, i, part)
	}

	return cells
}

func agentCell(
	values sessionFormatValues,
	parts []display.Part,
	index int,
	part display.Part,
) rowlayout.Cell {
	cell := agentBaseCell(values, parts, index, part)
	cell = agentReportedCell(values, part, cell)

	return agentCompactCell(values, part, cell)
}

func agentBaseCell(
	values sessionFormatValues,
	parts []display.Part,
	index int,
	part display.Part,
) rowlayout.Cell {
	state := rowlayout.Absent
	if part.Value != "" {
		state = rowlayout.Present
	}

	if part.StateKnown {
		state = part.State
	}

	if !agentHasGit(values.agent) &&
		(isGitPart(part) || (part.Field == "" && index+1 < len(parts) && isGitPart(parts[index+1]))) {
		state = rowlayout.Absent
	}

	return rowlayout.Cell{
		Text: agentCellText(part), Role: values.model.agentCellRole(values.row, values.agent, part),
		Variants: nil, State: state,
	}
}

func agentCellText(part display.Part) string {
	if info, ok := display.FieldInfo(part.Field); ok && info.Provider == display.ProviderCustom {
		return part.Value
	}

	return ansi.Strip(part.Value)
}

func agentReportedCell(
	values sessionFormatValues,
	part display.Part,
	cell rowlayout.Cell,
) rowlayout.Cell {
	providerID := agentProviderID(part.Field)
	if values.agent.PaneID != "" && !remoteAgentRow(values.row, values.agent) && providerID != "" &&
		values.model.providerRunner.Version(providerID) > 0 {
		reported := values.model.providerRunner.Cell(providerID, values.agent.PaneID, part.Field)

		cell.Text, cell.State = reported.Text, reported.State

		switch {
		case part.Role != "":
		case reported.Role != "":
			cell.Role = reported.Role
		default:
			cell.Role = agentGitCountRole(part.Field, cell.Text)
		}
	}

	if part.Field == display.FieldAgentPR && cell.State != rowlayout.Present {
		if recorded := values.model.recordedPR(values.agent); recorded != "" {
			cell.Text, cell.State = recorded, rowlayout.Present
		}
	}

	return cell
}

func agentCompactCell(
	values sessionFormatValues,
	part display.Part,
	cell rowlayout.Cell,
) rowlayout.Cell {
	if part.Field == "" && cell.State == rowlayout.Present {
		if compact := collapseSpaces(
			cell.Text,
		); ansi.StringWidth(
			compact,
		) < ansi.StringWidth(
			cell.Text,
		) {
			cell.Variants = []string{compact}
		}

		return cell
	}

	if info, ok := display.FieldInfo(
		part.Field,
	); ok && info.Compact != "" &&
		cell.State == rowlayout.Present {
		compact, compactState := values.ResolveSessionFormatCell(info.Compact)
		if compactState == rowlayout.Present &&
			ansi.StringWidth(compact) < ansi.StringWidth(cell.Text) {
			cell.Variants = []string{ansi.Strip(compact)}
		}
	}

	return cell
}

func collapseSpaces(value string) string {
	if !strings.Contains(value, "  ") {
		return value
	}

	var b strings.Builder
	b.Grow(len(value))

	inSpace := false

	for _, r := range value {
		if r == ' ' {
			if !inSpace {
				b.WriteRune(r)

				inSpace = true
			}

			continue
		}

		inSpace = false

		b.WriteRune(r)
	}

	return b.String()
}

func agentProviderID(field string) providers.ID {
	info, ok := display.FieldInfo(field)
	if !ok {
		return ""
	}

	switch info.Provider {
	case display.ProviderGit:
		return providers.Git
	case display.ProviderClock:
		return providers.Clock
	case display.ProviderAHT:
		return providers.AHT
	case display.ProviderResource:
		return providers.Resource
	case display.ProviderGH:
		return providers.GH
	case display.ProviderSession, display.ProviderAgent, display.ProviderCustom:
		return ""
	}

	return ""
}

func (m *Model) publishClockCells(now time.Time) {
	definition, _ := providers.DefinitionFor(providers.Clock)
	if !m.sessionFormats.agentRowsUseAny(definition.Fields...) {
		return
	}

	rows := make(providers.Rows)

	for _, row := range m.filtered {
		for _, agent := range row.Agents {
			if agent.PaneID == "" {
				continue
			}

			values := sessionFormatValues{
				model: m, row: row, agent: agent, hasAgent: true, isLastAgent: false,
				isActivePane: false, ageWidth: 0, contextWidth: 0, now: now,
			}

			fields := make(map[string]rowlayout.Cell, len(definition.Fields))
			for _, field := range definition.Fields {
				value := values.ResolveSessionFormatField(field)

				state := rowlayout.Absent
				if value != "" {
					state = rowlayout.Present
				}

				fields[field] = rowlayout.Cell{Text: value, Role: "", Variants: nil, State: state}
			}

			rows[agent.PaneID] = fields
		}
	}

	m.providerRunner.Publish(providers.Clock, rows)
}

func isGitPart(part display.Part) bool {
	info, ok := display.FieldInfo(part.Field)
	return ok && info.Provider == display.ProviderGit
}

func (m *Model) writePlannedAgentRow(
	out *strings.Builder,
	table sessionTableLayout,
	styles sessionRowStyles,
	row session.Session,
	agent session.AgentSession,
	last bool, selected bool,
	values *sessionFormatValues,
) {
	leftParts, rightParts := m.agentLayoutCells(table, row, agent, last, values)
	cells := agentCells(*values, leftParts)
	cells = append(cells, spacerCell(len(rightParts) > 0))
	cells = append(cells, agentCells(*values, rightParts)...)
	painted, _ := rowlayout.PaintColumns(table.agentRows.plan, cells, table.agentRows.theme)
	left := strings.Join(painted[:table.agentRows.rightStart], "")
	right := strings.Join(painted[table.agentRows.rightStart:], "")

	out.WriteString(rowPrefix(styles, m.rowKey(row, agent, true), selected))
	m.writeAgentText(out, left, selected, styles)

	if selected {
		writeStyledWithBackground(out, right, styles.selectedBgSeq)
	} else {
		m.writeMutedAgentText(out, right)
	}
}

func (m *Model) sessionRowOverflows(
	line display.Line,
	layout sessionTableLayout,
	available int,
) bool {
	if available <= 0 {
		return false
	}

	leftNatural := measuredPartsWidth(line.Left)
	rightNatural := measuredPartsWidth(line.Right)

	effectiveRight := max(layout.rightWidth, rightNatural)
	if len(line.Right) == 0 {
		effectiveRight = 0
	}

	gap := 0
	if effectiveRight > 0 {
		gap = 1
	}

	return leftNatural+gap+effectiveRight > available
}

func measuredPartsWidth(parts []display.Part) int {
	width := 0
	for _, part := range parts {
		width += lipgloss.Width(alignSessionFormatPart(part))
	}

	return width
}

func (m *Model) writePlannedSessionRow(
	out *strings.Builder,
	layout sessionTableLayout,
	styles sessionRowStyles,
	row session.Session,
	line display.Line,
	gutter string,
	available int,
	selected bool,
) {
	configured := m.configuredColumnPriority()
	leftSpecs := agentColumnSpecs(line.Left, false, configured)
	rightSpecs := agentColumnSpecs(line.Right, true, configured)
	specs := make([]rowlayout.ColumnSpec, 0, len(leftSpecs)+1+len(rightSpecs))
	specs = append(specs, leftSpecs...)
	specs = append(specs, agentSpacerSpec(rightSpecs))
	specs = append(specs, rightSpecs...)

	rightStart := len(leftSpecs) + 1
	cells := make([]rowlayout.Cell, 0, len(specs))
	cells = append(cells, sessionRowCells(line.Left)...)
	cells = append(cells, spacerCell(len(line.Right) > 0))
	cells = append(cells, sessionRowCells(line.Right)...)

	plan := rowlayout.Plan(
		specs,
		rowlayout.Measure(specs, [][]rowlayout.Cell{cells}),
		available,
		nil,
		layout.now,
	)
	painted, _ := rowlayout.PaintColumns(plan, cells, m.agentLayoutTheme())
	left := strings.Join(painted[:rightStart], "")
	right := strings.Join(painted[rightStart:], "")

	out.WriteString(rowPrefix(styles, m.rowKey(row, emptyAgentSession(), false), selected))

	if gutter != "" && selected {
		writeStyledWithBackground(out, gutter, styles.selectedBgSeq)
	} else {
		out.WriteString(gutter)
	}

	m.writePrimarySessionText(out, left, selected, styles)

	if right == "" {
		return
	}

	if selected {
		writeStyledWithBackground(out, right, styles.selectedBgSeq)
	} else {
		out.WriteString(right)
	}
}

func sessionRowCells(parts []display.Part) []rowlayout.Cell {
	cells := make([]rowlayout.Cell, len(parts))
	for i, part := range parts {
		state := rowlayout.Absent
		if part.Value != "" {
			state = rowlayout.Present
		}

		if part.StateKnown {
			state = part.State
		}

		var variants []string

		if part.Field == "" && state == rowlayout.Present {
			if compact := collapseSpaces(
				part.Value,
			); ansi.StringWidth(
				compact,
			) < ansi.StringWidth(
				part.Value,
			) {
				variants = []string{compact}
			}
		}

		cells[i] = rowlayout.Cell{
			Text:     part.Value,
			Role:     part.Role,
			Variants: variants,
			State:    state,
		}
	}

	return cells
}

func (m *Model) agentCellRole(
	row session.Session,
	agent session.AgentSession,
	part display.Part,
) string {
	if part.Role != "" {
		return part.Role
	}

	switch part.Field {
	case display.FieldStatusGlyph, display.FieldStatus:
		if agent.Status.DisplayStatus() == session.AgentStatusIdle &&
			m.agentIdleUnread(row, agent) {
			return "status_unread"
		}

		return "status_" + string(agent.Status.DisplayStatus())
	case display.FieldTree, display.FieldTreeBranch, display.FieldTreeIndent:
		return "tree"
	case display.FieldShortcut, display.FieldSessionKey:
		return "muted"
	case display.FieldAgentGitAdded, display.FieldAgentGitDeleted,
		display.FieldAgentGitAhead, display.FieldAgentGitBehind:
		return agentGitCountRole(part.Field, part.Value)
	}

	return ""
}

func agentGitCountRole(field string, value string) string {
	switch field {
	case display.FieldAgentGitAdded:
		if value != "+0" {
			return "success"
		}
	case display.FieldAgentGitDeleted:
		if value != "-0" {
			return "error"
		}
	case display.FieldAgentGitAhead:
		if value != "↑0" {
			return "primary"
		}
	case display.FieldAgentGitBehind:
		if value != "↓0" {
			return "secondary"
		}
	}

	return ""
}

func (m *Model) agentLayoutTheme() rowlayout.Theme {
	colors := m.config.ResolvedAgentStatusColors()
	values := map[string]string{
		"status_running": colors.Running, "status_waiting": colors.Waiting,
		"status_failed": colors.Failed, "status_interrupted": colors.Interrupted,
		"status_idle": colors.Idle, "status_unknown": colors.Idle,
		"status_unread": m.config.Theme.Primary, "muted": m.config.Theme.Muted,
		"success": m.config.Theme.Success, "error": m.config.Theme.Error,
		"warn":    m.config.Theme.Warning,
		"primary": m.config.Theme.Primary, "secondary": m.config.Theme.Secondary,
	}

	theme := make(rowlayout.Theme, len(values))
	for role, color := range values {
		prefix := extractStylePrefix(lipgloss.NewStyle().Foreground(lipgloss.Color(color)))

		code, ok := strings.CutPrefix(prefix, "\x1b[")
		if ok {
			code, _, _ = strings.Cut(code, "m")
			theme[role] = code
		}
	}

	if muted := theme["muted"]; muted != "" {
		theme["tree"] = "22m\x1b[" + muted
	}

	return theme
}
