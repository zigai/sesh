package display

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/samber/oops"
)

type testResolver map[string]string

func (r testResolver) ResolveSessionFormatField(field string) string {
	return r[field]
}

func byteWidth(value string) int {
	return len(value)
}

func TestCompileAgentGitFields(t *testing.T) {
	_, err := Compile(
		FormatKindAgentsAgent,
		"{agent_git_branch:<20.20} {agent_git_changes} {agent_git_info} {agent_worktree} {agent_worktree_name}",
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestTemplateGroupsFallbackPipesAndStyleRoles(t *testing.T) {
	template, err := Compile(FormatKindAgentsAgent, "[{agent_git_label|agent_cwd_name|upper} {agent_git_added:5@success}]{right}{agent_cwd|path_short|trunc_left:4|default:\"-\"}")
	if err != nil {
		t.Fatal(err)
	}

	if !template.UsesAny(FieldAgentGitLabel, FieldAgentCWDName) || !template.UsesAny(FieldAgentCWD) {
		t.Fatal("template did not report referenced fields")
	}

	line := template.Render(testResolver{FieldAgentCWDName: "workspace", FieldAgentGitAdded: "+2", FieldAgentCWD: "/work/development"})
	if line.Left[1].Value != "WORKSPACE" || line.Left[3].Role != "success" || line.Right[0].Value != "…ent" {
		t.Fatalf("rendered line = %+v", line)
	}

	if line.Left[0].Group == "" || line.Left[0].Group != line.Left[1].Group || !line.Left[2].Separator {
		t.Fatalf("group metadata = %+v", line.Left)
	}

	if got := template.Render(testResolver{}).Right[0].Value; got != "-" {
		t.Fatalf("default value = %q", got)
	}
}

func TestTemplateGroupHidesBracketsAndSeparatorsWithoutPresentFields(t *testing.T) {
	template, err := Compile(FormatKindSession, "[{source}/{connection}]")
	if err != nil {
		t.Fatal(err)
	}

	parts := template.Render(testResolver{FieldConnection: "ssh"}).Left
	if got := joinPartValues(parts); got != "[ssh]" {
		t.Fatalf("one present field = %q", got)
	}

	if got := joinPartValues(template.Render(testResolver{}).Left); got != "" {
		t.Fatalf("empty group = %q", got)
	}
}

func joinPartValues(parts []Part) string {
	var value strings.Builder
	for _, part := range parts {
		value.WriteString(part.Value)
	}

	return value.String()
}

func TestTemplateRejectsInvalidExtendedExpressions(t *testing.T) {
	for _, source := range []string{
		"{agent_git_label|missing}", "{agent_git_label|trunc_left:0}",
		"{agent_git_label|default:missing}", "{agent_git_label|upper:3}",
		"{agent_git_label@bad-role}", "{right@success}",
	} {
		if _, err := Compile(FormatKindAgentsAgent, source); err == nil {
			t.Fatalf("Compile(%q) accepted invalid expression", source)
		}
	}
}

func TestCompileRendersFieldsAlignmentAndEscapedBraces(t *testing.T) {
	template, err := Compile(
		FormatKindSession,
		"{{{session_name}}} {session_kind}{right}{window_count} windows",
	)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}

	line := template.Render(testResolver{
		FieldSessionName: "dev",
		FieldSessionKind: "live",
		FieldWindowCount: "3",
	})

	wantLeft := []Part{
		{Field: "", Value: "{", Alignment: AlignmentNone, Width: 0, MaxWidth: 0, HasMaxWidth: false},
		{Field: FieldSessionName, Value: "dev", Alignment: AlignmentNone, Width: 0, MaxWidth: 0, HasMaxWidth: false},
		{Field: "", Value: "} ", Alignment: AlignmentNone, Width: 0, MaxWidth: 0, HasMaxWidth: false},
		{Field: FieldSessionKind, Value: "live", Alignment: AlignmentNone, Width: 0, MaxWidth: 0, HasMaxWidth: false},
	}
	wantRight := []Part{
		{Field: FieldWindowCount, Value: "3", Alignment: AlignmentNone, Width: 0, MaxWidth: 0, HasMaxWidth: false},
		{Field: "", Value: " windows", Alignment: AlignmentNone, Width: 0, MaxWidth: 0, HasMaxWidth: false},
	}

	if !slices.Equal(line.Left, wantLeft) || !slices.Equal(line.Right, wantRight) {
		t.Fatalf("Render() = %#v, want left %#v and right %#v", line, wantLeft, wantRight)
	}
}

func TestCompileAllowsAgentFieldsOnlyForAgentRows(t *testing.T) {
	if _, err := Compile(FormatKindAgentsAgent, "{session_name}: {status} {agent_cwd_name}"); err != nil {
		t.Fatalf("Compile(agent row) error = %v", err)
	}

	if _, err := Compile(
		FormatKindSession,
		"{source_prefix}{session_name} [{source}/{connection}] {current_marker}{active_window_name} {active_pane_path_name}",
	); err != nil {
		t.Fatalf("Compile(session row) error = %v", err)
	}

	_, err := Compile(FormatKindAgentsSession, "{session_name}: {agent_cwd_name}")
	if err == nil {
		t.Fatal("Compile(session row) error = nil, want unknown agent field error")
	}
}

func TestTemplateUsesAnyFindsFieldsOnEitherSide(t *testing.T) {
	template, err := Compile(FormatKindSession, "{session_name}{right}{agent_total}")
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}

	if !template.UsesAny(FieldSessionName) || !template.UsesAny(FieldAgentTotal) {
		t.Fatal("UsesAny() did not find fields on both sides")
	}

	if template.UsesAny(FieldAttached, FieldAgentWaiting) {
		t.Fatal("UsesAny() found an unused field")
	}
}

func TestTemplateRightWidthMeasuresResolvedParts(t *testing.T) {
	template, err := Compile(FormatKindSession, "ignored{right}[{session_name}]")
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}

	width := template.RightWidth(testResolver{FieldSessionName: "dev"}, func(value string) int {
		return len(value)
	})
	if width != len("[dev]") {
		t.Fatalf("RightWidth() = %d, want %d", width, len("[dev]"))
	}

	defaultWidth := template.RightWidth(testResolver{FieldSessionName: "dev"}, nil)
	if defaultWidth != len("[dev]") {
		t.Fatalf("RightWidth(nil) = %d, want %d", defaultWidth, len("[dev]"))
	}

	litTemplate, err := Compile(FormatKindSession, "{right}[static]")
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}

	if w := litTemplate.RightWidth(nil, nil); w != len("[static]") {
		t.Fatalf("RightWidth(nil, nil) = %d, want %d", w, len("[static]"))
	}
}

func TestCompilePreservesPerFieldAlignment(t *testing.T) {
	template, err := Compile(
		FormatKindSession,
		"{session_name:<12.8}|{window_count:>3}|{session_kind:^8}{right}{active_window_name:20.10}",
	)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}

	line := template.Render(testResolver{
		FieldSessionName:      "dev",
		FieldWindowCount:      "4",
		FieldSessionKind:      "live",
		FieldActiveWindowName: "editor",
	})

	alignments := []struct {
		part      Part
		alignment Alignment
		width     int
		maxWidth  int
	}{
		{part: line.Left[0], alignment: AlignmentLeft, width: 12, maxWidth: 8},
		{part: line.Left[2], alignment: AlignmentRight, width: 3, maxWidth: 0},
		{part: line.Left[4], alignment: AlignmentCenter, width: 8, maxWidth: 0},
		{part: line.Right[0], alignment: AlignmentLeft, width: 20, maxWidth: 10},
	}

	for _, expected := range alignments {
		if expected.part.Alignment != expected.alignment || expected.part.Width != expected.width ||
			expected.part.MaxWidth != expected.maxWidth || expected.part.HasMaxWidth != (expected.maxWidth > 0) {
			t.Fatalf(
				"formatted part = %#v, want alignment %v, width %d, and maximum %d",
				expected.part,
				expected.alignment,
				expected.width,
				expected.maxWidth,
			)
		}
	}

	if width := template.RightWidth(testResolver{FieldActiveWindowName: "editor"}, byteWidth); width != 20 {
		t.Fatalf("RightWidth() = %d, want configured minimum 20", width)
	}
}

func TestCompileRejectsInvalidFormats(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		wantErr error
	}{
		{name: "empty", source: "  ", wantErr: ErrEmptyFormat},
		{name: "control character newline", source: "{session_name}\n", wantErr: ErrControlCharacter},
		{name: "control character tab", source: "{session_name}\t", wantErr: ErrControlCharacter},
		{name: "control character return", source: "{session_name}\r", wantErr: ErrControlCharacter},
		{name: "unclosed field", source: "{session_name", wantErr: ErrUnclosedField},
		{name: "unexpected brace", source: "{session_name}}", wantErr: ErrUnexpectedBrace},
		{name: "empty field", source: "{}", wantErr: ErrEmptyField},
		{name: "multiple right markers", source: "x{right}y{right}z", wantErr: ErrMultipleRight},
		{name: "missing width", source: "{session_name:>}", wantErr: ErrInvalidFieldSpec},
		{name: "invalid alignment", source: "{session_name:*8}", wantErr: ErrInvalidFieldSpec},
		{name: "zero width", source: "{session_name:0}", wantErr: ErrInvalidFieldSpec},
		{name: "excessive width", source: "{session_name:4097}", wantErr: ErrInvalidFieldSpec},
		{name: "invalid maximum width", source: "{session_name:20.wide}", wantErr: ErrInvalidFieldSpec},
		{name: "excessive maximum width", source: "{session_name:.4097}", wantErr: ErrInvalidFieldSpec},
		{name: "right field format", source: "{right:>8}", wantErr: ErrRightFieldSpec},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Compile(FormatKindSession, tt.source)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Compile() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestCompileRejectsUnknownField(t *testing.T) {
	_, err := Compile(FormatKindSession, "{session_path}")
	if err == nil {
		t.Fatal("Compile() error = nil, want unknown field error")
	}

	if !errors.Is(err, ErrUnknownField) {
		t.Fatalf("Compile() error = %v, want ErrUnknownField", err)
	}
}

func TestCompileAllowsStatusGlyphAndTextFields(t *testing.T) {
	for _, kind := range []FormatKind{FormatKindSession, FormatKindAgentsSession} {
		if _, err := Compile(kind, "{session_name} {agent_status_glyph} {agent_status_text}"); err != nil {
			t.Fatalf("Compile(%q) error = %v", kind, err)
		}
	}

	if _, err := Compile(
		FormatKindAgentsAgent,
		"{tree}{status_glyph} {status_text} {agent_status_glyph} {agent_status_text}",
	); err != nil {
		t.Fatalf("Compile(FormatKindAgentsAgent) error = %v", err)
	}
}

func TestCompileDiagnostics(t *testing.T) {
	_, err := Compile(FormatKindSession, "{unknown_field}")
	if err == nil {
		t.Fatal("expected compile error")
	}

	diagnostic, ok := oops.AsOops(err)
	if !ok {
		t.Fatalf("expected error to be enriched with oops, got %T", err)
	}

	if diagnostic.Domain() != "display" {
		t.Fatalf("expected domain 'display', got %q", diagnostic.Domain())
	}

	if diagnostic.Code() != "format_invalid" {
		t.Fatalf("expected code 'format_invalid', got %q", diagnostic.Code())
	}

	if op, ok := diagnostic.Context()["operation"].(string); !ok || op != "compile" {
		t.Fatalf("expected operation 'compile', got %v", diagnostic.Context()["operation"])
	}

	if k, ok := diagnostic.Context()["kind"].(string); !ok || k != "session" {
		t.Fatalf("expected kind 'session', got %v", diagnostic.Context()["kind"])
	}
}

func TestCompileScopeCases(t *testing.T) {
	agentOnlyFields := []string{
		FieldTree,
		FieldTreeBranch,
		FieldTreeIndent,
		FieldHarness,
		FieldHarnessRaw,
		FieldAgentSession,
		FieldRegistryID,
		FieldPaneID,
		FieldStatus,
		FieldStatusGlyph,
		FieldStatusText,
		FieldStatusAge,
		FieldStatusDuration,
		FieldStatusClock,
		FieldActiveMarker,
		FieldStale,
		FieldAgentContext,
		FieldAgentCWD,
		FieldAgentCWDName,
		FieldAgentProjectRoot,
		FieldAgentProjectName,
		FieldAgentGitBranch,
		FieldAgentGitGlyph,
		FieldAgentGitChanges,
		FieldAgentGitInfo,
		FieldAgentGitLabel,
		FieldAgentGitAdded,
		FieldAgentGitDeleted,
		FieldAgentGitAhead,
		FieldAgentGitBehind,
		FieldAgentWorktree,
		FieldAgentWorktreeName,
		FieldAgentSessionPath,
		FieldAgentSessionFile,
		FieldAgentExecutable,
		FieldAgentPID,
	}

	for _, field := range agentOnlyFields {
		_, err := Compile(FormatKindSession, "{"+field+"}")
		if err == nil {
			t.Fatalf("FormatKindSession should reject agent-only field %q", field)
		}

		if !errors.Is(err, ErrUnknownField) {
			t.Fatalf("expected ErrUnknownField for field %q, got %v", field, err)
		}

		_, err = Compile(FormatKindAgentsSession, "{"+field+"}")
		if err == nil {
			t.Fatalf("FormatKindAgentsSession should reject agent-only field %q", field)
		}

		if !errors.Is(err, ErrUnknownField) {
			t.Fatalf("expected ErrUnknownField for field %q, got %v", field, err)
		}

		_, err = Compile(FormatKindAgentsAgent, "{"+field+"}")
		if err != nil {
			t.Fatalf("FormatKindAgentsAgent should accept agent field %q: %v", field, err)
		}
	}

	_, err := Compile(FormatKind("invalid_kind"), "{session_name}")
	if err == nil {
		t.Fatal("expected error for invalid format kind")
	}

	if !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("expected ErrUnknownKind, got %v", err)
	}
}

func TestCompileLeftOnly(t *testing.T) {
	tLeft, err := Compile(FormatKindSession, "{session_name} live")
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}

	if tLeft.HasRight() {
		t.Fatal("tLeft should not have right side")
	}

	line := tLeft.Render(testResolver{FieldSessionName: "dev"})
	if len(line.Right) != 0 {
		t.Fatalf("expected 0 right parts, got %d", len(line.Right))
	}

	if len(line.Left) != 2 {
		t.Fatalf("expected 2 left parts, got %d", len(line.Left))
	}
}

func TestCompileRightOnly(t *testing.T) {
	tRight, err := Compile(FormatKindSession, "{right}{session_name} live")
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}

	if !tRight.HasRight() {
		t.Fatal("tRight should have right side")
	}

	lineRight := tRight.Render(testResolver{FieldSessionName: "dev"})
	if len(lineRight.Left) != 0 {
		t.Fatalf("expected 0 left parts, got %d", len(lineRight.Left))
	}

	if len(lineRight.Right) != 2 {
		t.Fatalf("expected 2 right parts, got %d", len(lineRight.Right))
	}
}

func TestCompileBothSides(t *testing.T) {
	tBoth, err := Compile(FormatKindSession, "{{left}} {session_name}{right}{{right}} {window_count}")
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}

	if !tBoth.HasRight() {
		t.Fatal("tBoth should have right side")
	}

	lineBoth := tBoth.Render(testResolver{FieldSessionName: "dev", FieldWindowCount: "2"})
	if lineBoth.Left[0].Value != "{left} " || lineBoth.Left[1].Value != "dev" {
		t.Fatalf("unexpected left parts: %#v", lineBoth.Left)
	}

	if lineBoth.Right[0].Value != "{right} " || lineBoth.Right[1].Value != "2" {
		t.Fatalf("unexpected right parts: %#v", lineBoth.Right)
	}
}

func TestCompileEmptyTemplate(t *testing.T) {
	var emptyTmpl Template
	if !emptyTmpl.IsEmpty() {
		t.Fatal("expected empty template to report IsEmpty() == true")
	}

	if emptyTmpl.HasRight() {
		t.Fatal("expected empty template to report HasRight() == false")
	}
}

func TestCompileMalformedAndExcessiveWidthRegressions(t *testing.T) {
	valid := []string{
		"{session_name:4096}",
		"{session_name:.4096}",
		"{session_name:4096.4096}",
	}
	for _, src := range valid {
		if _, err := Compile(FormatKindSession, src); err != nil {
			t.Fatalf("expected valid for %q: %v", src, err)
		}
	}

	invalid := []struct {
		name    string
		source  string
		wantErr error
	}{
		{name: "excessive width", source: "{session_name:4097}", wantErr: ErrFieldWidthRange},
		{name: "excessive max width", source: "{session_name:.4097}", wantErr: ErrFieldMaxRange},
		{name: "negative width", source: "{session_name:-1}", wantErr: ErrInvalidFieldSpec},
		{name: "negative max width", source: "{session_name:.-1}", wantErr: ErrInvalidFieldSpec},
		{name: "nested braces", source: "{session_{name}}", wantErr: ErrInvalidFieldName},
		{name: "closing brace", source: "just a closing brace}", wantErr: ErrUnexpectedBrace},
		{name: "unclosed field", source: "prefix {session_name", wantErr: ErrUnclosedField},
		{name: "multiple right markers", source: "{session_name}{right}mid{right}end", wantErr: ErrMultipleRight},
		{name: "right marker with spec", source: "{right:<20}", wantErr: ErrRightFieldSpec},
		{name: "non-numeric width", source: "{session_name:abc}", wantErr: ErrInvalidFieldSpec},
		{name: "non-numeric max width", source: "{session_name:10.abc}", wantErr: ErrInvalidFieldSpec},
		{name: "empty field with spec", source: "{:10}", wantErr: ErrEmptyField},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Compile(FormatKindSession, tt.source); !errors.Is(err, tt.wantErr) {
				t.Fatalf("Compile(%q) error = %v, want %v", tt.source, err, tt.wantErr)
			}
		})
	}
}

func TestCompileRejectsAllControlCharacters(t *testing.T) {
	controlChars := []string{
		"\x00",
		"\x01",
		"\x07",
		"\x08",
		"\x09",
		"\x0a",
		"\x0b",
		"\x0c",
		"\x0d",
		"\x1b",
		"\x7f",
		"\u0085",
	}

	for _, char := range controlChars {
		_, err := Compile(FormatKindSession, "test"+char+"{session_name}")
		if err == nil {
			t.Fatalf("expected control character error for %q", char)
		}

		if !errors.Is(err, ErrControlCharacter) {
			t.Fatalf("expected ErrControlCharacter for %q, got %v", char, err)
		}
	}
}

func FuzzCompile(f *testing.F) {
	for _, seed := range []string{
		"{session_name}",
		"{tree}{status_glyph} {agent_session}{right}{harness:<6.6}",
		"{{literal}} {agent_git_label:<6.22}",
		"[{agent_git_glyph} {agent_git_label}]",
		"{agent_git_label|agent_cwd_name}",
		"{agent_git_added:5@success}",
		"{bar:context_pct:8}",
		"{right}{right}",
		"{unclosed",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, source string) {
		for _, kind := range []FormatKind{FormatKindSession, FormatKindAgentsSession, FormatKindAgentsAgent} {
			template, err := Compile(kind, source)
			if err != nil {
				continue
			}

			line := template.Render(nil)
			if len(line.Left)+len(line.Right) == 0 {
				t.Fatalf("accepted source produced no parts: %q", source)
			}
		}
	})
}
