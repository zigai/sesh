package display

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/samber/oops"
)

const (
	FormatKindSession       FormatKind = "session"
	FormatKindAgentsSession FormatKind = "agents session"
	FormatKindAgentsAgent   FormatKind = "agents agent"
)

const (
	FieldSessionName         = "session_name"
	FieldSessionID           = "session_id"
	FieldSessionKind         = "session_kind"
	FieldSource              = "source"
	FieldConnection          = "connection"
	FieldSourcePrefix        = "source_prefix"
	FieldSourceStatus        = "source_status"
	FieldWindowCount         = "window_count"
	FieldActivityAge         = "activity_age"
	FieldAttached            = "attached"
	FieldCurrent             = "current"
	FieldCurrentMarker       = "current_marker"
	FieldAttachedMarker      = "attached_marker"
	FieldActiveWindowName    = "active_window_name"
	FieldActiveWindowIndex   = "active_window_index"
	FieldActivePanePath      = "active_pane_path"
	FieldActivePanePathName  = "active_pane_path_name"
	FieldActivePaneCommand   = "active_pane_command"
	FieldActivePaneTitle     = "active_pane_title"
	FieldAgentCount          = "agent_count"
	FieldAgentTotal          = "agent_total"
	FieldAgentWorking        = "agent_working"
	FieldAgentWaiting        = "agent_waiting"
	FieldAgentIdle           = "agent_idle"
	FieldAgentFailed         = "agent_failed"
	FieldAgentInterrupted    = "agent_interrupted"
	FieldAgentUnknown        = "agent_unknown"
	FieldAgentStatus         = "agent_status"
	FieldAgentStatusGlyph    = "agent_status_glyph"
	FieldAgentStatusText     = "agent_status_text"
	FieldAgentAge            = "agent_age"
	FieldAgentDuration       = "agent_duration"
	FieldAgentClock          = "agent_clock"
	FieldSessionAgentContext = "session_agent_context"
	FieldShortcut            = "shortcut"
	FieldShortcutBadge       = "shortcut_badge"
	FieldTree                = "tree"
	FieldTreeBranch          = "tree_branch"
	FieldTreeIndent          = "tree_indent"
	FieldHarness             = "harness"
	FieldHarnessRaw          = "harness_raw"
	FieldAgentSession        = "agent_session"
	FieldRegistryID          = "registry_id"
	FieldPaneID              = "pane_id"
	FieldStatus              = "status"
	FieldStatusGlyph         = "status_glyph"
	FieldStatusText          = "status_text"
	FieldStatusAge           = "status_age"
	FieldStatusDuration      = "status_duration"
	FieldStatusClock         = "status_clock"
	FieldActiveMarker        = "active_marker"
	FieldStale               = "stale"
	FieldAgentContext        = "agent_context"
	FieldAgentCWD            = "agent_cwd"
	FieldAgentCWDName        = "agent_cwd_name"
	FieldAgentProjectRoot    = "agent_project_root"
	FieldAgentProjectName    = "agent_project_name"
	FieldAgentGitBranch      = "agent_git_branch"
	FieldAgentGitGlyph       = "agent_git_glyph"
	FieldAgentGitChanges     = "agent_git_changes"
	FieldAgentGitInfo        = "agent_git_info"
	FieldAgentGitLabel       = "agent_git_label"
	FieldAgentGitAdded       = "agent_git_added"
	FieldAgentGitDeleted     = "agent_git_deleted"
	FieldAgentGitAhead       = "agent_git_ahead"
	FieldAgentGitBehind      = "agent_git_behind"
	FieldAgentWorktree       = "agent_worktree"
	FieldAgentWorktreeName   = "agent_worktree_name"
	FieldAgentSessionPath    = "agent_session_path"
	FieldAgentSessionFile    = "agent_session_file"
	FieldAgentExecutable     = "agent_executable"
	FieldAgentPID            = "agent_pid"
)

const (
	rightField    = "right"
	maxFieldWidth = 4096
)

const (
	AlignmentNone Alignment = iota
	AlignmentLeft
	AlignmentRight
	AlignmentCenter
)

var (
	ErrEmptyFormat      = errors.New("format cannot be empty")
	ErrControlCharacter = errors.New("format cannot contain control characters")
	ErrUnclosedField    = errors.New("unclosed field")
	ErrUnexpectedBrace  = errors.New("unexpected closing brace")
	ErrEmptyField       = errors.New("field name cannot be empty")
	ErrMultipleRight    = errors.New("format can contain at most one {right} marker")
	ErrUnknownField     = errors.New("unknown field")
	ErrInvalidFieldName = errors.New("invalid field name")
	ErrInvalidFieldSpec = errors.New("invalid field format")
	ErrFieldWidthRange  = errors.New("field width must be between 1 and 4096")
	ErrFieldMaxRange    = errors.New("field maximum width must be between 0 and 4096")
	ErrRightFieldSpec   = errors.New("{right} does not accept a field format")
	ErrUnknownKind      = errors.New("unknown format kind")
)

var (
	sessionAllowedFields = map[string]struct{}{
		FieldSessionName:         {},
		FieldSessionID:           {},
		FieldSessionKind:         {},
		FieldSource:              {},
		FieldConnection:          {},
		FieldSourcePrefix:        {},
		FieldSourceStatus:        {},
		FieldWindowCount:         {},
		FieldActivityAge:         {},
		FieldAttached:            {},
		FieldCurrent:             {},
		FieldCurrentMarker:       {},
		FieldAttachedMarker:      {},
		FieldActiveWindowName:    {},
		FieldActiveWindowIndex:   {},
		FieldActivePanePath:      {},
		FieldActivePanePathName:  {},
		FieldActivePaneCommand:   {},
		FieldActivePaneTitle:     {},
		FieldAgentCount:          {},
		FieldAgentTotal:          {},
		FieldAgentWorking:        {},
		FieldAgentWaiting:        {},
		FieldAgentIdle:           {},
		FieldAgentFailed:         {},
		FieldAgentInterrupted:    {},
		FieldAgentUnknown:        {},
		FieldAgentStatus:         {},
		FieldAgentStatusGlyph:    {},
		FieldAgentStatusText:     {},
		FieldAgentAge:            {},
		FieldAgentDuration:       {},
		FieldAgentClock:          {},
		FieldSessionAgentContext: {},
		FieldShortcut:            {},
		FieldShortcutBadge:       {},
	}

	agentAllowedFields = map[string]struct{}{
		FieldSessionName:         {},
		FieldSessionID:           {},
		FieldSessionKind:         {},
		FieldSource:              {},
		FieldConnection:          {},
		FieldSourcePrefix:        {},
		FieldSourceStatus:        {},
		FieldWindowCount:         {},
		FieldActivityAge:         {},
		FieldAttached:            {},
		FieldCurrent:             {},
		FieldCurrentMarker:       {},
		FieldAttachedMarker:      {},
		FieldActiveWindowName:    {},
		FieldActiveWindowIndex:   {},
		FieldActivePanePath:      {},
		FieldActivePanePathName:  {},
		FieldActivePaneCommand:   {},
		FieldActivePaneTitle:     {},
		FieldAgentCount:          {},
		FieldAgentTotal:          {},
		FieldAgentWorking:        {},
		FieldAgentWaiting:        {},
		FieldAgentIdle:           {},
		FieldAgentFailed:         {},
		FieldAgentInterrupted:    {},
		FieldAgentUnknown:        {},
		FieldAgentStatus:         {},
		FieldAgentStatusGlyph:    {},
		FieldAgentStatusText:     {},
		FieldAgentAge:            {},
		FieldAgentDuration:       {},
		FieldAgentClock:          {},
		FieldSessionAgentContext: {},
		FieldShortcut:            {},
		FieldShortcutBadge:       {},
		FieldTree:                {},
		FieldTreeBranch:          {},
		FieldTreeIndent:          {},
		FieldHarness:             {},
		FieldHarnessRaw:          {},
		FieldAgentSession:        {},
		FieldRegistryID:          {},
		FieldPaneID:              {},
		FieldStatus:              {},
		FieldStatusGlyph:         {},
		FieldStatusText:          {},
		FieldStatusAge:           {},
		FieldStatusDuration:      {},
		FieldStatusClock:         {},
		FieldActiveMarker:        {},
		FieldStale:               {},
		FieldAgentContext:        {},
		FieldAgentCWD:            {},
		FieldAgentCWDName:        {},
		FieldAgentProjectRoot:    {},
		FieldAgentProjectName:    {},
		FieldAgentGitBranch:      {},
		FieldAgentGitGlyph:       {},
		FieldAgentGitChanges:     {},
		FieldAgentGitInfo:        {},
		FieldAgentGitLabel:       {},
		FieldAgentGitAdded:       {},
		FieldAgentGitDeleted:     {},
		FieldAgentGitAhead:       {},
		FieldAgentGitBehind:      {},
		FieldAgentWorktree:       {},
		FieldAgentWorktreeName:   {},
		FieldAgentSessionPath:    {},
		FieldAgentSessionFile:    {},
		FieldAgentExecutable:     {},
		FieldAgentPID:            {},
	}
)

type FormatKind string

type Alignment byte

type Part struct {
	Field       string
	Value       string
	Alignment   Alignment
	Width       int
	MaxWidth    int
	HasMaxWidth bool
}

type Line struct {
	Left  []Part
	Right []Part
}

type Resolver interface {
	ResolveSessionFormatField(field string) string
}

type Template struct {
	left  []token
	right []token
}

type token struct {
	field       string
	text        string
	alignment   Alignment
	width       int
	maxWidth    int
	hasMaxWidth bool
}

type fieldSpec struct {
	alignment   Alignment
	width       int
	maxWidth    int
	hasMaxWidth bool
}

func Compile(kind FormatKind, source string) (Template, error) {
	tmpl, err := compile(kind, source)
	if err != nil {
		return Template{left: nil, right: nil}, oops.In("display").
			Code("format_invalid").
			With("operation", "compile", "kind", safeKind(kind)).
			Wrap(err)
	}

	return tmpl, nil
}

func (t Template) Render(resolver Resolver) Line {
	return Line{
		Left:  renderTokens(t.left, resolver),
		Right: renderTokens(t.right, resolver),
	}
}

func (t Template) UsesAny(fields ...string) bool {
	return tokensUseAny(t.left, fields) || tokensUseAny(t.right, fields)
}

func (t Template) RightWidth(resolver Resolver, measure func(string) int) int {
	if measure == nil {
		measure = StringWidth
	}

	if resolver == nil {
		width := 0

		for _, item := range t.right {
			valueWidth := measuredTokenWidth(item, measure(item.text))
			width += max(valueWidth, item.width)
		}

		return width
	}

	return MeasureRight(t, resolver, measure)
}

func (t Template) HasRight() bool {
	return len(t.right) > 0
}

func (t Template) IsEmpty() bool {
	return len(t.left) == 0 && len(t.right) == 0
}

func MeasureRight[T Resolver](template Template, resolver T, measure func(string) int) int {
	if measure == nil {
		measure = StringWidth
	}

	width := 0

	for _, item := range template.right {
		value := item.text
		if item.field != "" {
			value = resolver.ResolveSessionFormatField(item.field)
		}

		valueWidth := measuredTokenWidth(item, measure(value))
		width += max(valueWidth, item.width)
	}

	return width
}

func compile(kind FormatKind, source string) (Template, error) {
	if strings.TrimSpace(source) == "" {
		return Template{left: nil, right: nil}, ErrEmptyFormat
	}

	if strings.IndexFunc(source, unicode.IsControl) >= 0 {
		return Template{left: nil, right: nil}, ErrControlCharacter
	}

	allowed, err := allowedFields(kind)
	if err != nil {
		return Template{left: nil, right: nil}, err
	}

	return parse(source, allowed)
}

func safeKind(kind FormatKind) string {
	switch kind {
	case FormatKindSession, FormatKindAgentsSession, FormatKindAgentsAgent:
		return string(kind)
	default:
		return "unknown"
	}
}

func tokensUseAny(tokens []token, fields []string) bool {
	for _, item := range tokens {
		if slices.Contains(fields, item.field) {
			return true
		}
	}

	return false
}

func parse(source string, allowed map[string]struct{}) (Template, error) {
	var (
		template  Template
		rightSeen bool
	)

	current := &template.left

	for len(source) > 0 {
		literal, remainder, err := nextLiteral(source)
		if err != nil {
			return Template{left: nil, right: nil}, err
		}

		appendLiteral(current, literal)

		source = remainder
		if source == "" {
			break
		}

		expression, remainder, err := nextField(source)
		if err != nil {
			return Template{left: nil, right: nil}, err
		}

		source = remainder

		field, spec, err := parseFieldExpression(expression)
		if err != nil {
			return Template{left: nil, right: nil}, err
		}

		if field == rightField {
			if spec != (fieldSpec{
				alignment:   AlignmentNone,
				width:       0,
				maxWidth:    0,
				hasMaxWidth: false,
			}) {
				return Template{left: nil, right: nil}, ErrRightFieldSpec
			}

			if rightSeen {
				return Template{left: nil, right: nil}, ErrMultipleRight
			}

			rightSeen = true
			current = &template.right

			continue
		}

		if _, ok := allowed[field]; !ok {
			return Template{left: nil, right: nil}, fmt.Errorf("%w %q for this row", ErrUnknownField, field)
		}

		*current = append(*current, token{
			field:       field,
			text:        "",
			alignment:   spec.alignment,
			width:       spec.width,
			maxWidth:    spec.maxWidth,
			hasMaxWidth: spec.hasMaxWidth,
		})
	}

	return template, nil
}

func nextLiteral(source string) (string, string, error) {
	var literal strings.Builder

	for index := 0; index < len(source); {
		switch source[index] {
		case '{':
			if index+1 < len(source) && source[index+1] == '{' {
				literal.WriteByte('{')

				index += 2

				continue
			}

			return literal.String(), source[index:], nil
		case '}':
			if index+1 < len(source) && source[index+1] == '}' {
				literal.WriteByte('}')

				index += 2

				continue
			}

			return "", "", ErrUnexpectedBrace
		default:
			literal.WriteByte(source[index])
			index++
		}
	}

	return literal.String(), "", nil
}

func nextField(source string) (string, string, error) {
	closing := strings.IndexByte(source, '}')
	if closing < 0 {
		return "", "", ErrUnclosedField
	}

	field := source[1:closing]
	if field == "" {
		return "", "", ErrEmptyField
	}

	if strings.ContainsAny(field, "{}") {
		return "", "", fmt.Errorf("%w %q", ErrInvalidFieldName, field)
	}

	return field, source[closing+1:], nil
}

func parseFieldExpression(expression string) (string, fieldSpec, error) {
	field, spec, hasSpec := strings.Cut(expression, ":")
	if field == "" {
		return "", fieldSpec{
			alignment:   AlignmentNone,
			width:       0,
			maxWidth:    0,
			hasMaxWidth: false,
		}, ErrEmptyField
	}

	if strings.ContainsAny(field, "{}") {
		return "", fieldSpec{
			alignment:   AlignmentNone,
			width:       0,
			maxWidth:    0,
			hasMaxWidth: false,
		}, fmt.Errorf("%w %q", ErrInvalidFieldName, field)
	}

	if !hasSpec {
		return field, fieldSpec{
			alignment:   AlignmentNone,
			width:       0,
			maxWidth:    0,
			hasMaxWidth: false,
		}, nil
	}

	parsed, err := parseFieldSpec(spec)
	if err != nil {
		return "", fieldSpec{
			alignment:   AlignmentNone,
			width:       0,
			maxWidth:    0,
			hasMaxWidth: false,
		}, fmt.Errorf("%w for %q: %w", ErrInvalidFieldSpec, field, err)
	}

	return field, parsed, nil
}

func parseFieldSpec(value string) (fieldSpec, error) {
	if value == "" {
		return fieldSpec{
			alignment:   AlignmentNone,
			width:       0,
			maxWidth:    0,
			hasMaxWidth: false,
		}, ErrFieldWidthRange
	}

	alignment, value := parseFieldAlignment(value)
	spec := fieldSpec{
		alignment:   alignment,
		width:       0,
		maxWidth:    0,
		hasMaxWidth: false,
	}

	widthText, maxWidthText, hasMaxWidth := strings.Cut(value, ".")

	width, err := parseFieldWidth(widthText)
	if err != nil {
		return fieldSpec{
			alignment:   AlignmentNone,
			width:       0,
			maxWidth:    0,
			hasMaxWidth: false,
		}, err
	}

	spec.width = width

	if hasMaxWidth {
		maxWidth, err := parseFieldMaxWidth(maxWidthText)
		if err != nil {
			return fieldSpec{
				alignment:   AlignmentNone,
				width:       0,
				maxWidth:    0,
				hasMaxWidth: false,
			}, err
		}

		spec.maxWidth = maxWidth
		spec.hasMaxWidth = true
	}

	if spec.width == 0 && !spec.hasMaxWidth {
		return fieldSpec{
			alignment:   AlignmentNone,
			width:       0,
			maxWidth:    0,
			hasMaxWidth: false,
		}, ErrFieldWidthRange
	}

	return spec, nil
}

func parseFieldAlignment(value string) (Alignment, string) {
	switch value[0] {
	case '<':
		return AlignmentLeft, value[1:]
	case '>':
		return AlignmentRight, value[1:]
	case '^':
		return AlignmentCenter, value[1:]
	default:
		return AlignmentLeft, value
	}
}

func parseFieldWidth(value string) (int, error) {
	if value == "" {
		return 0, nil
	}

	width, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parsing width %q: %w", value, err)
	}

	if width <= 0 || width > maxFieldWidth {
		return 0, ErrFieldWidthRange
	}

	return width, nil
}

func parseFieldMaxWidth(value string) (int, error) {
	maxWidth, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parsing maximum width %q: %w", value, err)
	}

	if maxWidth < 0 || maxWidth > maxFieldWidth {
		return 0, ErrFieldMaxRange
	}

	return maxWidth, nil
}

func measuredTokenWidth(tok token, valueWidth int) int {
	if tok.hasMaxWidth {
		return min(valueWidth, tok.maxWidth)
	}

	return valueWidth
}

func appendLiteral(tokens *[]token, literal string) {
	if literal == "" {
		return
	}

	*tokens = append(*tokens, token{
		field:       "",
		text:        literal,
		alignment:   AlignmentNone,
		width:       0,
		maxWidth:    0,
		hasMaxWidth: false,
	})
}

func renderTokens(tokens []token, resolver Resolver) []Part {
	if len(tokens) == 0 {
		return nil
	}

	parts := make([]Part, 0, len(tokens))
	for _, item := range tokens {
		value := item.text
		if item.field != "" && resolver != nil {
			value = resolver.ResolveSessionFormatField(item.field)
		}

		parts = append(parts, Part{
			Field:       item.field,
			Value:       value,
			Alignment:   item.alignment,
			Width:       item.width,
			MaxWidth:    item.maxWidth,
			HasMaxWidth: item.hasMaxWidth,
		})
	}

	return parts
}

func allowedFields(kind FormatKind) (map[string]struct{}, error) {
	switch kind {
	case FormatKindSession, FormatKindAgentsSession:
		return sessionAllowedFields, nil
	case FormatKindAgentsAgent:
		return agentAllowedFields, nil
	default:
		return nil, fmt.Errorf("%w %q", ErrUnknownKind, kind)
	}
}
