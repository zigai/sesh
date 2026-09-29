package tui

import (
	"fmt"
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
	agentMinimumFlexWidth = 3
	agentRequiredPriority = 0
	agentDefaultPriority  = 10
	agentFlexiblePriority = 20
	agentLiteralPriority  = 25
	agentGitPriority      = 30
	agentShrinkPriority   = 20
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

	rightStart := 0

	for _, row := range m.filtered {
		for index, agent := range row.Agents {
			left, right, values := m.agentLayoutCells(table, row, agent, index == len(row.Agents)-1)
			if len(rows) == 0 {
				specs = append(specs, agentColumnSpecs(left)...)
				specs = append(specs, agentSpacerSpec())
				specs = append(specs, agentColumnSpecs(right)...)
				rightStart = len(left) + 1
			}

			cells := agentCells(values, left)
			cells = append(cells, rowlayout.Cell{Text: " ", Role: "", Variants: nil, State: rowlayout.Present})
			cells = append(cells, agentCells(values, right)...)
			rows = append(rows, cells)
		}
	}

	return agentRowMeasurements{specs: specs, rows: rows, rightStart: rightStart}
}

func agentSpacerSpec() rowlayout.ColumnSpec {
	return rowlayout.ColumnSpec{
		ID: "right_spacer", Align: rowlayout.Left, Min: 1, Max: 0,
		Reserve: 1, Flex: false, Grow: true,
		ShrinkPriority: agentShrinkPriority, HidePriority: agentRequiredPriority,
		TruncateSide: rowlayout.TruncateRight, Group: "", Separator: false,
	}
}

func (m *Model) planAgentRows(table sessionTableLayout, measured agentRowMeasurements) agentTableLayout {
	if len(measured.rows) == 0 {
		var empty rowlayout.ColumnPlan
		return agentTableLayout{plan: empty, theme: nil, rightStart: 0, valid: false}
	}

	plan := rowlayout.Plan(measured.specs, rowlayout.Measure(measured.specs, measured.rows), max(table.width-table.prefixWidth, 0), &m.agentRowRatchet, table.now)

	return agentTableLayout{plan: plan, theme: m.agentLayoutTheme(), rightStart: measured.rightStart, valid: true}
}

func (m *Model) agentLayoutCells(table sessionTableLayout, row session.Session, agent session.AgentSession, last bool) ([]display.Part, []display.Part, sessionFormatValues) {
	table.rightWidth = 0
	table.agentAgeWidth = 0
	template, values := m.sessionRowFormatter(row, agent, true, last, table)
	line := template.Render(values)

	return line.Left, line.Right, values
}

func agentColumnSpecs(parts []display.Part) []rowlayout.ColumnSpec {
	specs := make([]rowlayout.ColumnSpec, len(parts))
	for index, part := range parts {
		specs[index] = agentColumnSpec(index, part)
	}

	return specs
}

func agentColumnSpec(index int, part display.Part) rowlayout.ColumnSpec {
	info, _ := display.FieldInfo(part.Field)

	maxWidth := info.WidthBound
	if part.HasMaxWidth {
		maxWidth = part.MaxWidth
	}

	minWidth := part.Width
	if info.Flexible {
		minWidth = max(minWidth, agentMinimumFlexWidth)
	}

	truncate := rowlayout.TruncateRight
	if info.TruncateLeft || part.Alignment == display.AlignmentRight {
		truncate = rowlayout.TruncateLeft
	}

	return rowlayout.ColumnSpec{
		ID: fmt.Sprintf("%d:%s", index, part.Field), Align: agentAlignment(part.Alignment),
		Min: minWidth, Max: maxWidth, Reserve: info.WidthBound,
		Flex: info.Flexible, Grow: false, ShrinkPriority: agentShrinkPriority, HidePriority: agentHidePriority(part.Field, info),
		TruncateSide: truncate, Group: part.Group, Separator: part.Separator,
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

func agentHidePriority(field string, info display.FieldMetadata) int {
	switch {
	case field == display.FieldTree || field == display.FieldTreeBranch || field == display.FieldTreeIndent,
		field == display.FieldStatus || field == display.FieldStatusGlyph || field == display.FieldStatusText,
		field == display.FieldActiveMarker:
		return agentRequiredPriority
	case info.Provider == display.ProviderGit:
		return agentGitPriority
	case info.Flexible:
		return agentFlexiblePriority
	case field == "":
		return agentLiteralPriority
	default:
		return agentDefaultPriority
	}
}

func agentCells(values sessionFormatValues, parts []display.Part) []rowlayout.Cell {
	cells := make([]rowlayout.Cell, len(parts))
	for i, part := range parts {
		cells[i] = agentCell(values, parts, i, part)
	}

	return cells
}

func agentCell(values sessionFormatValues, parts []display.Part, index int, part display.Part) rowlayout.Cell {
	cell := agentBaseCell(values, parts, index, part)
	cell = agentReportedCell(values, part, cell)

	return agentCompactCell(values, part, cell)
}

func agentBaseCell(values sessionFormatValues, parts []display.Part, index int, part display.Part) rowlayout.Cell {
	state := rowlayout.Absent
	if part.Value != "" {
		state = rowlayout.Present
	}

	if part.StateKnown {
		state = part.State
	}

	if !agentHasGit(values.agent) && (isGitPart(part) || (part.Field == "" && index+1 < len(parts) && isGitPart(parts[index+1]))) {
		state = rowlayout.Absent
	}

	return rowlayout.Cell{
		Text: ansi.Strip(part.Value), Role: values.model.agentCellRole(values.row, values.agent, part),
		Variants: nil, State: state,
	}
}

func agentReportedCell(values sessionFormatValues, part display.Part, cell rowlayout.Cell) rowlayout.Cell {
	providerID := agentProviderID(part.Field)
	if values.agent.PaneID != "" && providerID != "" && values.model.providerRunner.Version(providerID) > 0 {
		reported := values.model.providerRunner.Cell(providerID, values.agent.PaneID, part.Field)

		cell.Text, cell.State = reported.Text, reported.State
		if part.Role == "" && reported.Role != "" {
			cell.Role = reported.Role
		}
	}

	return cell
}

func agentCompactCell(values sessionFormatValues, part display.Part, cell rowlayout.Cell) rowlayout.Cell {
	if info, ok := display.FieldInfo(part.Field); ok && info.Compact != "" && cell.State == rowlayout.Present {
		compact, compactState := values.ResolveSessionFormatCell(info.Compact)
		if compactState == rowlayout.Present && ansi.StringWidth(compact) < ansi.StringWidth(cell.Text) {
			cell.Variants = []string{ansi.Strip(compact)}
		}
	}

	return cell
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
	case display.ProviderSession, display.ProviderAgent:
		return ""
	}

	return ""
}

func (m *Model) publishClockCells(now time.Time) {
	definition, _ := providers.DefinitionFor(providers.Clock)
	if !m.sessionFormats.agentsAgent.UsesAny(definition.Fields...) {
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

func (m *Model) writePlannedAgentRow(out *strings.Builder, table sessionTableLayout, styles sessionRowStyles, row session.Session, agent session.AgentSession, last, selected bool) {
	leftParts, rightParts, values := m.agentLayoutCells(table, row, agent, last)
	cells := agentCells(values, leftParts)
	cells = append(cells, rowlayout.Cell{Text: " ", Role: "", Variants: nil, State: rowlayout.Present})
	cells = append(cells, agentCells(values, rightParts)...)
	painted, _ := rowlayout.PaintColumns(table.agentRows.plan, cells, table.agentRows.theme)
	left := strings.Join(painted[:table.agentRows.rightStart], "")
	right := strings.Join(painted[table.agentRows.rightStart:], "")

	prefix := styles.blankPrefix
	if selected {
		prefix = styles.selectedBar
	}

	out.WriteString(prefix)
	m.writeAgentText(out, left, selected, styles)

	if selected {
		writeStyledWithBackground(out, right, styles.selectedBgSeq)
	} else {
		m.writeMutedAgentText(out, right)
	}
}

func (m *Model) agentCellRole(row session.Session, agent session.AgentSession, part display.Part) string {
	if part.Role != "" {
		return part.Role
	}

	switch part.Field {
	case display.FieldStatusGlyph, display.FieldStatus:
		if agent.Status.DisplayStatus() == session.AgentStatusIdle && m.agentIdleUnread(row, agent) {
			return "status_unread"
		}

		return "status_" + string(agent.Status.DisplayStatus())
	case display.FieldTree, display.FieldTreeBranch, display.FieldTreeIndent:
		return "tree"
	case display.FieldShortcut, display.FieldShortcutBadge:
		return "muted"
	case display.FieldAgentGitAdded, display.FieldAgentGitDeleted,
		display.FieldAgentGitAhead, display.FieldAgentGitBehind:
		return agentGitCountRole(part.Field, part.Value)
	}

	return ""
}

func agentGitCountRole(field, value string) string {
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
