package display

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/display/layout"
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
	FieldSessionKey          = "session_key"
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
	FieldAgentModel          = "agent_model"
	FieldAgentWaitingReason  = "agent_waiting_reason"
	FieldAgentFailureReason  = "agent_failure_reason"
	FieldAgentLastTool       = "agent_last_tool"
	FieldAgentTask           = "agent_task"
	FieldAgentContextPct     = "agent_context_pct"
	FieldAgentTokens         = "agent_tokens"
	FieldAgentCost           = "agent_cost"
	FieldAgentTodoDone       = "agent_todo_done"
	FieldAgentTodoTotal      = "agent_todo_total"
	FieldAgentSubagents      = "agent_subagents"
	FieldSubagentCount       = "subagent_count"
	FieldAgentPromptAge      = "agent_prompt_age"
	FieldAgentPR             = "agent_pr"
	FieldHost                = "host"
	FieldEvidence            = "evidence"
	FieldAgentCI             = "agent_ci"
	FieldAgentGitStash       = "agent_git_stash"
	FieldAgentGitConflicts   = "agent_git_conflicts"
	FieldAgentGitDirty       = "agent_git_dirty"
	FieldPaneCPU             = "pane_cpu"
	FieldPaneMem             = "pane_mem"
	FieldWindowCPU           = "window_cpu"
	FieldWindowMem           = "window_mem"
	FieldSessionCPU          = "session_cpu"
	FieldSessionMem          = "session_mem"
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

type FormatKind string

type Alignment byte

type Part struct {
	Field       string
	Value       string
	Alignment   Alignment
	Width       int
	MaxWidth    int
	HasMaxWidth bool
	Group       string
	Separator   bool
	Role        string
	State       layout.State
	StateKnown  bool
}

type Line struct {
	Left  []Part
	Right []Part
}

type Resolver interface {
	ResolveSessionFormatField(field string) string
}

type CellResolver interface {
	ResolveSessionFormatCell(field string) (string, layout.State)
}

type Template struct {
	left  []token
	right []token
}

type token struct {
	field       string
	fallback    string
	pipes       []pipe
	role        string
	group       string
	separator   bool
	widget      string
	secondary   string
	threshold   int
	text        string
	alignment   Alignment
	width       int
	maxWidth    int
	hasMaxWidth bool
}

type pipe struct {
	name  string
	value string
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

func (t Template) CustomFields() []string {
	var names []string

	for _, item := range slices.Concat(t.left, t.right) {
		for _, field := range []string{item.field, item.fallback} {
			if name, ok := CustomFieldName(field); ok && !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}

	slices.Sort(names)

	return names
}

func (t Template) UsesWidget(widget string) bool {
	for _, item := range t.left {
		if item.widget == widget {
			return true
		}
	}

	for _, item := range t.right {
		if item.widget == widget {
			return true
		}
	}

	return false
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

func (t Template) Empty() bool {
	return len(t.left) == 0 && len(t.right) == 0
}

func MeasureRight[T Resolver](template Template, resolver T, measure func(string) int) int {
	if measure == nil {
		measure = StringWidth
	}

	width := 0

	for _, part := range renderTokens(template.right, resolver) {
		valueWidth := measure(part.Value)
		if part.HasMaxWidth {
			valueWidth = min(valueWidth, part.MaxWidth)
		}

		width += max(valueWidth, part.Width)
	}

	return width
}

func newToken() token {
	return token{
		field: "", fallback: "", pipes: nil, role: "", group: "", separator: false,
		widget: "", secondary: "", threshold: 0, text: "", alignment: AlignmentNone,
		width: 0, maxWidth: 0, hasMaxWidth: false,
	}
}

func literalToken(value string) token {
	item := newToken()
	item.text = value

	return item
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
		if slices.Contains(fields, item.field) || slices.Contains(fields, item.fallback) || slices.Contains(fields, item.secondary) {
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

		item, err := parseExtendedExpression(expression, allowed)
		if err != nil {
			return Template{left: nil, right: nil}, err
		}

		if item.field == rightField {
			if item.fallback != "" || len(item.pipes) != 0 || item.role != "" || (fieldSpec{
				alignment: item.alignment, width: item.width, maxWidth: item.maxWidth, hasMaxWidth: item.hasMaxWidth,
			}) != (fieldSpec{
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

		*current = append(*current, item)
	}

	template.left = assignGroups(template.left, "left")
	template.right = assignGroups(template.right, "right")

	return template, nil
}

func parseExtendedExpression(expression string, allowed map[string]struct{}) (token, error) {
	if item, handled, err := parseWidgetExpression(expression, allowed); handled {
		return item, err
	}

	role := ""

	if before, after, ok := strings.Cut(expression, "@"); ok {
		if !validIdentifier(after) {
			return token{}, fmt.Errorf("%w %q", ErrInvalidFieldName, after)
		}

		expression, role = before, after
	}

	segments := strings.Split(expression, "|")

	field, spec, err := parseFieldExpression(segments[0])
	if err != nil {
		return token{}, err
	}

	item := newToken()
	item.field, item.role = field, role
	item.alignment, item.width, item.maxWidth, item.hasMaxWidth = spec.alignment, spec.width, spec.maxWidth, spec.hasMaxWidth

	if field != rightField && !fieldAllowed(allowed, field) {
		return token{}, fmt.Errorf("%w %q for this row", ErrUnknownField, field)
	}

	for _, segment := range segments[1:] {
		if err := applyTokenPipe(&item, segment, allowed); err != nil {
			return token{}, err
		}
	}

	return item, nil
}

func applyTokenPipe(item *token, segment string, allowed map[string]struct{}) error {
	name, value, hasValue := strings.Cut(segment, ":")
	if name == "trunc_left" || name == "default" || name == "upper" || name == "path_short" {
		parsed, err := parseNamedPipe(name, value, hasValue)
		if err != nil {
			return fmt.Errorf("%w for %q", err, item.field)
		}

		item.pipes = append(item.pipes, parsed)

		return nil
	}

	if item.fallback != "" || hasValue {
		return fmt.Errorf("%w for %q", ErrInvalidFieldSpec, item.field)
	}

	if !fieldAllowed(allowed, segment) {
		return fmt.Errorf("%w %q for this row", ErrUnknownField, segment)
	}

	item.fallback = segment

	return nil
}

func parseNamedPipe(name, value string, hasValue bool) (pipe, error) {
	switch name {
	case "trunc_left":
		width, err := parseFieldWidth(value)
		if !hasValue || err != nil || width == 0 {
			return pipe{}, ErrInvalidFieldSpec
		}
	case "default":
		fallback, err := strconv.Unquote(value)
		if !hasValue || err != nil || strings.IndexFunc(fallback, unicode.IsControl) >= 0 {
			return pipe{}, ErrInvalidFieldSpec
		}

		value = fallback
	case "upper", "path_short":
		if hasValue {
			return pipe{}, ErrInvalidFieldSpec
		}
	}

	return pipe{name: name, value: value}, nil
}

func parseWidgetExpression(expression string, allowed map[string]struct{}) (token, bool, error) {
	name, rest, _ := strings.Cut(expression, ":")

	item, known, err := parseWidgetToken(name, rest)
	if !known || err != nil {
		return item, known, err
	}

	if _, ok := allowed[item.field]; !ok {
		return token{}, true, fmt.Errorf("%w %q for this row", ErrUnknownField, item.field)
	}

	if item.secondary != "" {
		if _, ok := allowed[item.secondary]; !ok {
			return token{}, true, fmt.Errorf("%w %q for this row", ErrUnknownField, item.secondary)
		}
	}

	return item, true, nil
}

func parseWidgetToken(name, rest string) (token, bool, error) {
	item := newToken()
	item.alignment = AlignmentLeft

	switch name {
	case "bar":
		parsed, err := parseBarWidget(rest)
		return parsed, true, err
	case "todo_progress":
		width, err := parseFieldWidth(rest)
		if err != nil || width == 0 {
			return token{}, true, ErrInvalidFieldSpec
		}

		item.field, item.secondary, item.widget = FieldAgentTodoDone, FieldAgentTodoTotal, name
		item.width, item.maxWidth, item.hasMaxWidth = width+fractionPrefixWidth, width+fractionPrefixWidth, true
	case "spinner":
		if rest != "" {
			return token{}, true, ErrInvalidFieldSpec
		}

		item.field, item.widget, item.width, item.maxWidth, item.hasMaxWidth = FieldStatus, name, 1, 1, true
	case "badge":
		parsed, err := parseBadgeWidget(rest)
		return parsed, true, err
	default:
		return newToken(), false, nil
	}

	return item, true, nil
}

func parseBarWidget(rest string) (token, error) {
	input, widthText, ok := strings.Cut(rest, ":")
	if input == "context_pct" {
		input = FieldAgentContextPct
	}

	width, err := parseFieldWidth(widthText)
	if !ok || err != nil || width == 0 || input != FieldAgentContextPct {
		return token{}, ErrInvalidFieldSpec
	}

	item := newToken()
	item.alignment = AlignmentLeft
	item.field, item.widget, item.width, item.maxWidth, item.hasMaxWidth = input, "bar", width, width, true

	return item, nil
}

func parseBadgeWidget(rest string) (token, error) {
	input, roleSpec, hasRole := strings.Cut(rest, "@")
	if input != FieldSubagentCount && input != FieldAgentSubagents {
		return token{}, ErrInvalidFieldSpec
	}

	item := newToken()
	item.field, item.widget, item.width, item.maxWidth, item.hasMaxWidth = input, "badge", 3, 3, true

	item.alignment = AlignmentLeft
	if !hasRole {
		return item, nil
	}

	role, threshold, ok := strings.Cut(roleSpec, ">")

	parsed, err := strconv.Atoi(threshold)
	if !ok || !validIdentifier(role) || err != nil || parsed < 0 {
		return token{}, ErrInvalidFieldSpec
	}

	item.role, item.threshold = role, parsed

	return item, nil
}

func validIdentifier(value string) bool {
	if value == "" {
		return false
	}

	for _, r := range value {
		if r != '_' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}

	return true
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

	*tokens = append(*tokens, literalToken(literal))
}

func assignGroups(tokens []token, side string) []token {
	split := splitGroupLiterals(tokens)
	stack := make([]int, 0)

	for i := range split {
		switch split[i].text {
		case "[":
			stack = append(stack, i)
		case "]":
			if len(stack) == 0 {
				continue
			}

			open := stack[len(stack)-1]
			stack = stack[:len(stack)-1]

			markGroupRange(split, open, i, side)
		}
	}

	return split
}

func splitGroupLiterals(tokens []token) []token {
	var split []token

	for _, item := range tokens {
		if item.field != "" || !strings.ContainsAny(item.text, "[]") {
			split = append(split, item)
			continue
		}

		start := 0

		for index, r := range item.text {
			if r != '[' && r != ']' {
				continue
			}

			if index > start {
				split = append(split, literalToken(item.text[start:index]))
			}

			split = append(split, literalToken(string(r)))
			start = index + 1
		}

		if start < len(item.text) {
			split = append(split, literalToken(item.text[start:]))
		}
	}

	return split
}

func markGroupRange(tokens []token, open, closeIndex int, side string) {
	hasField := false
	for i := open + 1; i < closeIndex; i++ {
		hasField = hasField || tokens[i].field != ""
	}

	if !hasField {
		return
	}

	group := side + strconv.Itoa(open)
	for i := open; i <= closeIndex; i++ {
		tokens[i].group = group
		tokens[i].separator = i > open && i < closeIndex && tokens[i].field == ""
	}
}

func renderTokens(tokens []token, resolver Resolver) []Part {
	if len(tokens) == 0 {
		return nil
	}

	parts := make([]Part, 0, len(tokens))
	for _, item := range tokens {
		parts = append(parts, renderToken(item, resolver))
	}

	hideAbsentGroups(parts)

	return parts
}

func renderToken(item token, resolver Resolver) Part {
	value, role := item.text, item.role

	state, stateKnown := layout.Absent, false
	if item.field != "" && resolver != nil {
		value, state, stateKnown = resolveCell(resolver, item.field)
		if value == "" && item.fallback != "" {
			value, state, stateKnown = resolveCell(resolver, item.fallback)
		}

		value = applyTokenTransforms(value, item.pipes)
		if item.widget != "" {
			value, role, state = renderWidget(item, resolver, value, state)
			stateKnown = true
		}
	}

	if !stateKnown {
		state = layout.Absent
	}

	return Part{
		Field: item.field, Value: value, Alignment: item.alignment, Width: item.width,
		MaxWidth: item.maxWidth, HasMaxWidth: item.hasMaxWidth, Group: item.group,
		Separator: item.separator, Role: role, State: state, StateKnown: stateKnown,
	}
}

func applyTokenTransforms(value string, transforms []pipe) string {
	for _, transform := range transforms {
		switch transform.name {
		case "trunc_left":
			width, _ := strconv.Atoi(transform.value)
			value = truncateLeft(value, width)
		case "default":
			if value == "" {
				value = transform.value
			}
		case "upper":
			value = upperText(value)
		case "path_short":
			if value != "" {
				value = path.Base(value)
			}
		}
	}

	return value
}

func hideAbsentGroups(parts []Part) {
	visible := make(map[string]bool)

	for _, part := range parts {
		if part.Group != "" && part.Field != "" && part.Value != "" {
			visible[part.Group] = true
		}
	}

	for i := range parts {
		part := &parts[i]
		if part.Group == "" {
			continue
		}

		if !visible[part.Group] {
			part.Value = ""
			continue
		}

		if !part.Separator {
			continue
		}

		if !separatorHasNeighbors(parts, i) {
			part.Value = ""
		}
	}
}

func separatorHasNeighbors(parts []Part, index int) bool {
	before, after := false, false

	for i, part := range parts {
		if part.Group != parts[index].Group || part.Field == "" || part.Value == "" {
			continue
		}

		if i < index {
			before = true
		} else if i > index {
			after = true
		}
	}

	return before && after
}

func resolveCell(resolver Resolver, field string) (string, layout.State, bool) {
	if cells, ok := resolver.(CellResolver); ok {
		value, state := cells.ResolveSessionFormatCell(field)
		return value, state, true
	}

	value := resolver.ResolveSessionFormatField(field)

	state := layout.Absent
	if value != "" {
		state = layout.Present
	}

	return value, state, false
}

func truncateLeft(value string, width int) string {
	if ansi.StringWidth(value) <= width {
		return value
	}

	return layout.TruncateLeftWithMark(value, width, "…")
}

func upperText(value string) string {
	if !strings.Contains(value, "\x1b") {
		return strings.ToUpper(value)
	}

	var (
		b     strings.Builder
		state byte
	)

	for value != "" {
		seq, _, n, newState := ansi.DecodeSequence(value, state, nil)
		state = newState
		value = value[n:]

		if strings.HasPrefix(seq, "\x1b") {
			b.WriteString(seq)
		} else {
			b.WriteString(strings.ToUpper(seq))
		}
	}

	return b.String()
}

func fieldAllowed(allowed map[string]struct{}, field string) bool {
	if _, ok := allowed[field]; ok {
		return true
	}

	_, ok := CustomFieldName(field)

	return ok
}

func allowedFields(kind FormatKind) (map[string]struct{}, error) {
	if kind != FormatKindSession && kind != FormatKindAgentsSession && kind != FormatKindAgentsAgent {
		return nil, fmt.Errorf("%w %q", ErrUnknownKind, kind)
	}

	allowed := make(map[string]struct{})

	for field, info := range fieldRegistry {
		if (kind == FormatKindAgentsAgent && info.AgentRows) || (kind != FormatKindAgentsAgent && info.SessionRows) {
			allowed[field] = struct{}{}
		}
	}

	return allowed, nil
}
