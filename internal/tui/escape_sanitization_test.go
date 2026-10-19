package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func TestRenderSanitizesTerminalEscapes(t *testing.T) {
	t.Run("session_name_osc52_clipboard", testSanitizeOSC52Clipboard)
	t.Run("window_name_osc8_hyperlink", testSanitizeOSC8Hyperlink)
	t.Run("agent_session_csi_clear_and_cursor", testSanitizeAgentCSI)
	t.Run("pane_title_osc0_window_title", testSanitizePaneTitleOSC0)
	t.Run("bidi_override_controls", testSanitizeBidiOverride)
	t.Run("c0_control_characters", testSanitizeC0Controls)
	t.Run("cjk_and_emoji_display_width", testPreserveCJKAndEmojiWidth)
	t.Run("raw_hostile_osc_stripped", testSanitizeRawHostileOSC)
}

func testSanitizeOSC52Clipboard(t *testing.T) {
	t.Helper()

	model := newTestModel()
	model.width = 80
	model.height = 20

	hostilePayload := "project\x1b]52;c;aGFja2Vk\a"
	row := session.Session{
		Name: hostilePayload,
		Kind: session.KindLive,
	}

	renderedRow := model.FormatSessionRow(row)
	if strings.Contains(renderedRow, "\x1b]52") || strings.Contains(renderedRow, "\x1b]") {
		t.Fatalf("FormatSessionRow contains unescaped OSC 52 sequence: %q", renderedRow)
	}

	if !strings.Contains(renderedRow, testSupportSessionProjectName) {
		t.Fatalf("FormatSessionRow missing sanitized session name 'project': %q", renderedRow)
	}

	model.filtered = []session.Session{row}
	fullView := model.RenderView()

	if strings.Contains(fullView, "\x1b]52") || strings.Contains(fullView, "\x1b]") {
		t.Fatalf("RenderView contains unescaped OSC 52 sequence: %q", fullView)
	}

	if !strings.Contains(fullView, testSupportSessionProjectName) {
		t.Fatalf("RenderView missing sanitized session name: %q", fullView)
	}
}

func testSanitizeOSC8Hyperlink(t *testing.T) {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.Display.SeshFormat = "{session_name} [{active_window_name}]"
	model := newTestModel()
	model.config = cfg

	formats, err := compileSessionRowFormats(cfg.Display, cfg.Settings)
	if err != nil {
		t.Fatalf("compileSessionRowFormats failed: %v", err)
	}

	model.sessionFormats = formats
	model.width = 80
	model.height = 20

	hostileWindow := "editor\x1b]8;;https://malicious.example.com\x1b\\hidden\x1b]8;;\x1b\\"
	row := session.Session{
		Name:             testSupportSessionWorkName,
		Kind:             session.KindLive,
		ActiveWindowName: hostileWindow,
	}

	renderedRow := model.FormatSessionRow(row)
	if strings.Contains(renderedRow, "\x1b]8") || strings.Contains(renderedRow, "\x1b]") {
		t.Fatalf("FormatSessionRow contains unescaped OSC 8 hyperlink sequence: %q", renderedRow)
	}

	if !strings.Contains(renderedRow, "editor") || !strings.Contains(renderedRow, "hidden") {
		t.Fatalf("FormatSessionRow missing sanitized window text: %q", renderedRow)
	}

	model.filtered = []session.Session{row}
	fullView := model.RenderView()

	if strings.Contains(fullView, "\x1b]8") || strings.Contains(fullView, "\x1b]") {
		t.Fatalf("RenderView contains unescaped OSC 8 sequence: %q", fullView)
	}
}

func testSanitizeAgentCSI(t *testing.T) {
	t.Helper()

	model := newTestModel()
	model.displayMode = displayModeAgents
	model.width = 90
	model.height = 20

	hostileAgentSession := "agent\x1b[2J\x1b[0;0H\x1b[?25l-task"
	parentSession := session.Session{
		Name:       "agents-work",
		Kind:       session.KindLive,
		AgentTotal: 1,
	}
	agent := session.AgentSession{
		Harness:        testSupportHarnessCodex,
		HarnessSession: hostileAgentSession,
		Status:         session.AgentStatusRunning,
	}
	parentSession.Agents = []session.AgentSession{agent}

	renderedAgent := model.FormatAgentRow(parentSession, agent)
	if strings.Contains(renderedAgent, "\x1b[2J") || strings.Contains(renderedAgent, "\x1b[0;0H") || strings.Contains(renderedAgent, "\x1b[?25l") {
		t.Fatalf("FormatAgentRow contains raw CSI control sequence: %q", renderedAgent)
	}

	if !strings.Contains(renderedAgent, "agent") || !strings.Contains(renderedAgent, "-task") {
		t.Fatalf("FormatAgentRow missing sanitized agent label text: %q", renderedAgent)
	}

	model.filtered = []session.Session{parentSession}
	fullView := model.RenderView()

	if strings.Contains(fullView, "\x1b[2J") || strings.Contains(fullView, "\x1b[0;0H") || strings.Contains(fullView, "\x1b[?25l") {
		t.Fatalf("RenderView contains raw CSI control sequence: %q", fullView)
	}
}

func testSanitizePaneTitleOSC0(t *testing.T) {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.Display.SeshFormat = "{session_name} ({active_pane_title})"
	model := newTestModel()
	model.config = cfg

	formats, err := compileSessionRowFormats(cfg.Display, cfg.Settings)
	if err != nil {
		t.Fatalf("compileSessionRowFormats failed: %v", err)
	}

	model.sessionFormats = formats

	hostileTitle := "shell\x1b]0;Pwned Terminal\a"
	row := session.Session{
		Name:            "ops",
		Kind:            session.KindLive,
		ActivePaneTitle: hostileTitle,
	}

	rendered := model.FormatSessionRow(row)
	if strings.Contains(rendered, "\x1b]0") || strings.Contains(rendered, "\x1b]") {
		t.Fatalf("FormatSessionRow contains unescaped OSC 0 title sequence: %q", rendered)
	}

	if !strings.Contains(rendered, "shell") {
		t.Fatalf("FormatSessionRow missing sanitized pane title: %q", rendered)
	}
}

func testSanitizeBidiOverride(t *testing.T) {
	t.Helper()

	model := newTestModel()

	hostileName := "invoice\u202eEXE.txt\u202c"
	row := session.Session{
		Name: hostileName,
		Kind: session.KindLive,
	}

	rendered := model.FormatSessionRow(row)
	if strings.ContainsRune(rendered, '\u202e') || strings.ContainsRune(rendered, '\u202c') {
		t.Fatalf("FormatSessionRow leaked bidirectional override control characters: %q", rendered)
	}

	if !strings.Contains(rendered, "invoice") || !strings.Contains(rendered, "EXE.txt") {
		t.Fatalf("FormatSessionRow corrupted printable characters: %q", rendered)
	}
}

func testSanitizeC0Controls(t *testing.T) {
	t.Helper()

	model := newTestModel()

	hostileName := "alpha\r\n\a\b\x00beta"
	row := session.Session{
		Name: hostileName,
		Kind: session.KindLive,
	}

	rendered := model.FormatSessionRow(row)
	for _, forbidden := range []rune{'\r', '\n', '\a', '\b', 0} {
		if strings.ContainsRune(rendered, forbidden) {
			t.Fatalf("FormatSessionRow contains unescaped C0 control code %q: %q", forbidden, rendered)
		}
	}

	if !strings.Contains(rendered, "alphabeta") {
		t.Fatalf("FormatSessionRow missing sanitized text content: %q", rendered)
	}
}

func testPreserveCJKAndEmojiWidth(t *testing.T) {
	t.Helper()

	model := newTestModel()
	model.width = 60

	unicodeName := "sesh-🚀-项目-ワークスペース"
	row := session.Session{
		Name: unicodeName,
		Kind: session.KindLive,
	}

	rendered := model.FormatSessionRow(row)
	if !strings.Contains(rendered, unicodeName) {
		t.Fatalf("FormatSessionRow failed to preserve valid CJK and emoji name: %q", rendered)
	}

	nameWidth := lipgloss.Width(unicodeName)
	expectedWidth := ansi.StringWidth(unicodeName)

	if nameWidth != expectedWidth {
		t.Fatalf("lipgloss.Width (%d) != ansi.StringWidth (%d) for %q", nameWidth, expectedWidth, unicodeName)
	}
}

func testSanitizeRawHostileOSC(t *testing.T) {
	t.Helper()

	rawHostile := "raw-session\x1b]52;c;aGFja2Vk\a"
	if !strings.Contains(rawHostile, "\x1b]") {
		t.Fatal("test fixture missing expected OSC escape sequence")
	}

	sanitized := SanitizeTerminalText(rawHostile)
	if strings.Contains(sanitized, "\x1b]") {
		t.Fatalf("SanitizeTerminalText failed to strip OSC sequence: %q", sanitized)
	}

	if sanitized != "raw-session" {
		t.Fatalf("SanitizeTerminalText = %q, want 'raw-session'", sanitized)
	}
}
