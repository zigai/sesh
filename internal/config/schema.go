package config

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const (
	DefaultSeshFormat            = "{source_prefix}{session_name}{right}{session_agent_context}"
	DefaultAgentsSessionFormat   = "{source_prefix}{session_name}"
	DefaultAgentsAgentFormat     = "{tree}{status_glyph} {status_age:<7}   {agent_cwd_name:<23.23}  {agent_session}{right}{harness:<6.6}   {agent_git_label:<6.22} {agent_git_added:<5} {agent_git_deleted:<5} {agent_git_ahead:<2} {agent_git_behind:<2}"
	DefaultWorkSectionFormat     = "{session_name}"
	DefaultWorkTaskFormat        = "{tree}{status_glyph} {status_age:<7}   {agent_project_name:<16.16}  {agent_session}{right}{harness:<6.6}   {agent_git_label:<6.22} {agent_git_added:<5} {agent_git_deleted:<5} {agent_pr:<6}"
	defaultDirectorySuggestions  = 5
	defaultAutosaveInterval      = "3m"
	defaultSnapshotRetentionDays = 14
	defaultMaxParallelConnects   = 4
	defaultBackoffFactor         = 2.0
	shortHexLength               = 3
	fullHexLength                = 6
	yiqDivisor                   = 1000
	yiqThreshold                 = 100
)

var (
	errHookArgsMustBeArray     = errors.New("hook args must be an array")
	errHookArgsMustBeStrings   = errors.New("hook args must be strings")
	errHookCommandMustBeString = errors.New("hook command must be a string")
	errHookTimeoutMustBeString = errors.New("hook timeout must be a string")
	errHookCommandInvalidType  = errors.New("hook command must be a string or table")
	errUnknownField            = errors.New("unknown field")
	errExpectedArray           = errors.New("expected array")
	errUnknownHookField        = errors.New("unknown hook field")
)

var DefaultProcessEnvironment = []string{
	"PATH",
	"VIRTUAL_ENV",
	"CONDA_PREFIX",
	"CONDA_DEFAULT_ENV",
	"PYTHONPATH",
	"NODE_PATH",
}

type Config struct {
	source        Source
	Theme         ThemeConfig                 `toml:"theme"`
	Keys          KeyConfig                   `toml:"keys"`
	Popup         PopupConfig                 `toml:"popup"`
	Display       DisplayConfig               `toml:"display"`
	Paths         PathConfig                  `toml:"paths"`
	Settings      SettingsConfig              `toml:"settings"`
	Autosave      AutosaveConfig              `toml:"autosave"`
	Snapshot      SnapshotConfig              `toml:"snapshot"`
	Hooks         HookConfig                  `toml:"hooks"`
	AgentSessions AgentSessionsConfig         `toml:"agent_sessions"`
	AgentStatus   AgentStatusConfig           `toml:"agent_status"`
	Remote        RemoteConfig                `toml:"remote"`
	Actions       ActionsConfig               `toml:"actions"`
	Projects      ProjectsConfig              `toml:"projects"`
	Tasks         TasksConfig                 `toml:"tasks"`
	TaskActions   map[string]TaskActionConfig `toml:"task_actions"`
}

// ActionsConfig overrides per-action policy, keyed by action group then verb: [actions.session.kill].
type ActionsConfig map[string]map[string]ActionConfig

// ActionConfig overrides one action's defaults; unset fields keep the action's own default.
type ActionConfig struct {
	Confirm     *bool `toml:"confirm"`
	AllowRemote *bool `toml:"allow_remote"`
}

type PopupConfig struct {
	SeshWidth    string `toml:"sesh_width"`
	SeshHeight   string `toml:"sesh_height"`
	AgentsWidth  string `toml:"agents_width"`
	AgentsHeight string `toml:"agents_height"`
}

type DisplayConfig struct {
	SeshFormat          string        `toml:"sesh_format"`
	AgentsSessionFormat string        `toml:"agents_session_format"`
	AgentsAgentFormat   string        `toml:"agents_agent_format"`
	WorkSectionFormat   string        `toml:"work_section_format"`
	WorkTaskFormat      string        `toml:"work_task_format"`
	Fields              FieldCommands `toml:"fields,omitempty"`
}

type AgentStatusThemeConfig struct {
	Running     string `toml:"running"`
	Waiting     string `toml:"waiting"`
	Failed      string `toml:"failed"`
	Interrupted string `toml:"interrupted"`
	Idle        string `toml:"idle"`
}

type ThemeConfig struct {
	Preset      string                  `toml:"preset"`
	Primary     string                  `toml:"primary"`
	Secondary   string                  `toml:"secondary"`
	Muted       string                  `toml:"muted"`
	Error       string                  `toml:"error"`
	Warning     string                  `toml:"warning"`
	Success     string                  `toml:"success"`
	Border      string                  `toml:"border"`
	Selected    string                  `toml:"selected"`
	Background  string                  `toml:"background"`
	Foreground  string                  `toml:"foreground"`
	AgentStatus *AgentStatusThemeConfig `toml:"agent_status,omitempty"`
}

type KeyConfig struct {
	Bind      []KeyBindConfig `toml:"bind"`
	Help      string          `toml:"help"`
	CycleMode string          `toml:"cycle_mode"`
	AgentSort string          `toml:"agent_sort"`
	PageUp    string          `toml:"page_up"`
	PageDown  string          `toml:"page_down"`
	Top       string          `toml:"top"`
	Bottom    string          `toml:"bottom"`
	Select1   string          `toml:"select_1"`
	Select2   string          `toml:"select_2"`
	Select3   string          `toml:"select_3"`
	Select4   string          `toml:"select_4"`
	Select5   string          `toml:"select_5"`
	Select6   string          `toml:"select_6"`
	Select7   string          `toml:"select_7"`
	Select8   string          `toml:"select_8"`
	Select9   string          `toml:"select_9"`
	Select0   string          `toml:"select_0"`
}

// KeyBindConfig binds a key in the picker or a tmux key table to an action. It replaces the
// default binding of the same key; an empty action removes that default.
type KeyBindConfig struct {
	Key    string         `toml:"key"`
	Action string         `toml:"action"`
	In     string         `toml:"in"`
	Table  string         `toml:"table"`
	Params map[string]any `toml:"params"`
}

type PathConfig struct {
	DataDir string `toml:"data_dir"`
	LogFile string `toml:"log_file"`
}

type SettingsConfig struct {
	ShowFooter                   bool     `toml:"show_footer"`
	DirectorySuggestions         int      `toml:"directory_suggestions"`
	DefaultSessionPathSuggestion string   `toml:"default_session_path_suggestion"`
	DisplayModes                 []string `toml:"display_modes"`
	LogLevel                     string   `toml:"log_level"`
	ShortcutStyle                string   `toml:"shortcut_style"`
}

type AutosaveConfig struct {
	Enabled            bool   `toml:"enabled"`
	Interval           string `toml:"interval"`
	RestoreLastOnStart bool   `toml:"restore_last_on_start"`
}

type SnapshotConfig struct {
	RetentionDays      int      `toml:"retention_days"`
	MaxCount           int      `toml:"max_count"`
	MaxBytes           int64    `toml:"max_bytes"`
	ProcessEnvironment []string `toml:"process_environment"`
}

type AgentSessionsConfig struct {
	PopupSort        string             `toml:"popup_sort"`
	Enabled          bool               `toml:"enabled"`
	PopupCounts      bool               `toml:"popup_counts"`
	RefreshInterval  string             `toml:"refresh_interval"`
	StatusStyle      string             `toml:"status_style,omitempty"`
	StatusTimeFormat string             `toml:"status_time_format,omitempty"`
	StatusTimeStyle  string             `toml:"status_time_style,omitempty"`
	Status           *AgentStatusConfig `toml:"status,omitempty"`
}

type AgentStatusConfig struct {
	Enabled                  bool                    `toml:"enabled"`
	StorePath                string                  `toml:"store_path,omitempty"`
	TmuxBinary               string                  `toml:"tmux_binary,omitempty"`
	StyleWaiting             string                  `toml:"style_waiting,omitempty"`
	StyleFailed              string                  `toml:"style_failed,omitempty"`
	StyleUnread              string                  `toml:"style_unread,omitempty"`
	Colors                   *AgentStatusThemeConfig `toml:"colors,omitempty"`
	Palette                  map[string]string       `toml:"palette,omitempty"`
	SetWindowStatusStyle     bool                    `toml:"set_window_status_style"`
	SetWindowStatusBellStyle bool                    `toml:"set_window_status_bell_style"`
	ActiveBypass             bool                    `toml:"active_bypass"`
	ClearFailedOnVisit       bool                    `toml:"clear_failed_on_visit"`
	InitialBackoff           string                  `toml:"initial_backoff"`
	MaxBackoff               string                  `toml:"max_backoff"`
	BackoffFactor            float64                 `toml:"backoff_factor"`
	ClearOnShutdown          bool                    `toml:"clear_on_shutdown"`
	Quiet                    bool                    `toml:"quiet"`

	explicitBools map[string]bool
}

type rawAgentStatus struct {
	Enabled                  *bool                   `toml:"enabled"`
	StorePath                *string                 `toml:"store_path"`
	TmuxBinary               *string                 `toml:"tmux_binary"`
	StyleWaiting             *string                 `toml:"style_waiting"`
	StyleFailed              *string                 `toml:"style_failed"`
	StyleUnread              *string                 `toml:"style_unread"`
	Colors                   *AgentStatusThemeConfig `toml:"colors"`
	Palette                  map[string]string       `toml:"palette"`
	SetWindowStatusStyle     *bool                   `toml:"set_window_status_style"`
	SetWindowStatusBellStyle *bool                   `toml:"set_window_status_bell_style"`
	ActiveBypass             *bool                   `toml:"active_bypass"`
	ClearFailedOnVisit       *bool                   `toml:"clear_failed_on_visit"`
	InitialBackoff           *string                 `toml:"initial_backoff"`
	MaxBackoff               *string                 `toml:"max_backoff"`
	BackoffFactor            *float64                `toml:"backoff_factor"`
	ClearOnShutdown          *bool                   `toml:"clear_on_shutdown"`
	Quiet                    *bool                   `toml:"quiet"`
}

type RemoteConfig struct {
	Enabled             bool                     `toml:"enabled"`
	SSHPath             string                   `toml:"ssh_path"`
	TmuxPollInterval    string                   `toml:"tmux_poll_interval"`
	ConnectTimeout      string                   `toml:"connect_timeout"`
	ActionTimeout       string                   `toml:"action_timeout"`
	HeartbeatInterval   string                   `toml:"heartbeat_interval"`
	StaleAfter          string                   `toml:"stale_after"`
	HideAfter           string                   `toml:"hide_after"`
	MaxParallelConnects int                      `toml:"max_parallel_connects"`
	Connections         []RemoteConnectionConfig `toml:"connections"`
	EnableReverseBridge bool                     `toml:"enable_reverse_bridge"`
	ReturnToOrigin      bool                     `toml:"return_to_origin"`
}

type RemoteConnectionConfig struct {
	ID         string `toml:"id"`
	Label      string `toml:"label"`
	Host       string `toml:"host"`
	SeshPath   string `toml:"sesh_path"`
	TmuxSocket string `toml:"tmux_socket"`
}

type HookConfig struct {
	Timeout           string        `toml:"timeout"`
	SessionSwitchPre  []HookCommand `toml:"session_switch_pre"`
	SessionSwitchPost []HookCommand `toml:"session_switch_post"`
	SessionAttachPre  []HookCommand `toml:"session_attach_pre"`
	SessionAttachPost []HookCommand `toml:"session_attach_post"`
	TmuxStartupPost   []HookCommand `toml:"tmux_startup_post"`
}

type HookCommand struct {
	ShellCommand string   `toml:"-"`
	Command      string   `toml:"command,omitempty"`
	Args         []string `toml:"args,omitempty"`
	Timeout      string   `toml:"timeout,omitempty"`
}

func (c *Config) SetDefaults() {
	if c.Theme.Primary == "" {
		*c = *DefaultConfig().Clone()
	}
}

func FilterProcessEnvironmentKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}

	seen := make(map[string]bool, len(keys))

	var filtered []string

	for _, k := range keys {
		if k == "TMUX" || k == "TMUX_PANE" {
			continue
		}

		if seen[k] {
			continue
		}

		seen[k] = true
		filtered = append(filtered, k)
	}

	return filtered
}

func (c *Config) Source() Source {
	return c.source
}

func (c *Config) Clone() *Config {
	if c == nil {
		return nil
	}

	cp := *c
	cp.Settings.DisplayModes = slices.Clone(c.Settings.DisplayModes)
	cp.Display.Fields = c.Display.Fields.Clone()
	cp.Snapshot.ProcessEnvironment = slices.Clone(c.Snapshot.ProcessEnvironment)

	cp.Hooks.SessionSwitchPre = cloneHookCommands(c.Hooks.SessionSwitchPre)
	cp.Hooks.SessionSwitchPost = cloneHookCommands(c.Hooks.SessionSwitchPost)
	cp.Hooks.SessionAttachPre = cloneHookCommands(c.Hooks.SessionAttachPre)
	cp.Hooks.SessionAttachPost = cloneHookCommands(c.Hooks.SessionAttachPost)
	cp.Hooks.TmuxStartupPost = cloneHookCommands(c.Hooks.TmuxStartupPost)

	if c.AgentSessions.Status != nil {
		clonedStatus := c.AgentSessions.Status.Clone()
		cp.AgentSessions.Status = &clonedStatus
	}

	cp.Remote.Connections = slices.Clone(c.Remote.Connections)
	cp.Actions = c.Actions.Clone()
	cp.Projects = c.Projects.Clone()
	cp.Tasks = c.Tasks.Clone()
	cp.TaskActions = cloneTaskActions(c.TaskActions)
	cp.Keys.Bind = cloneKeyBinds(c.Keys.Bind)

	cp.AgentStatus = c.AgentStatus.Clone()
	if c.Theme.AgentStatus != nil {
		cloned := *c.Theme.AgentStatus
		cp.Theme.AgentStatus = &cloned
	}

	return &cp
}

func (a ActionsConfig) Clone() ActionsConfig {
	if a == nil {
		return nil
	}

	cloned := make(ActionsConfig, len(a))
	for group, verbs := range a {
		cloned[group] = maps.Clone(verbs)
	}

	return cloned
}

func (c *AgentStatusConfig) Clone() AgentStatusConfig {
	cp := *c
	if c.Palette != nil {
		cp.Palette = maps.Clone(c.Palette)
	}

	if c.Colors != nil {
		cloned := *c.Colors
		cp.Colors = &cloned
	}

	if c.explicitBools != nil {
		cp.explicitBools = maps.Clone(c.explicitBools)
	}

	return cp
}

func (r RemoteConnectionConfig) ResolvedSeshPath() string {
	if r.SeshPath != "" {
		return r.SeshPath
	}

	return "sesh"
}

func (c *AgentStatusConfig) UnmarshalTOML(data []byte) error {
	var raw rawAgentStatus

	d := toml.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()

	if err := d.Decode(&raw); err != nil {
		return fmt.Errorf("decode toml: %w", err)
	}

	c.explicitBools = make(map[string]bool)
	c.applyRawBooleans(&raw)
	c.applyRawStringsAndFloats(&raw)

	if raw.Palette != nil {
		c.Palette = raw.Palette
	}

	return nil
}

func (h *HookConfig) UnmarshalTOML(data []byte) error {
	var raw map[string]any

	d := toml.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(&raw); err != nil {
		return fmt.Errorf("decode hooks: %w", err)
	}

	for k, v := range raw {
		if err := h.setHookField(k, v); err != nil {
			return err
		}
	}

	return nil
}

func DefaultConfig() *Config {
	return &Config{
		source: Source{Path: "", WithoutConfig: false, Input: ""},
		Theme:  DefaultThemeConfig(),
		Keys: KeyConfig{
			Bind:      nil,
			Help:      "?",
			CycleMode: "ctrl+f",
			AgentSort: "ctrl+s",
			PageUp:    "pgup",
			PageDown:  "pgdown",
			Top:       "home",
			Bottom:    "end",
			Select1:   "ctrl+1",
			Select2:   "ctrl+2",
			Select3:   "ctrl+3",
			Select4:   "ctrl+4",
			Select5:   "ctrl+5",
			Select6:   "ctrl+6",
			Select7:   "ctrl+7",
			Select8:   "ctrl+8",
			Select9:   "ctrl+9",
			Select0:   "ctrl+0",
		},
		Popup: PopupConfig{
			SeshWidth:    "40%",
			SeshHeight:   "45%",
			AgentsWidth:  "40%",
			AgentsHeight: "80%",
		},
		Display: DisplayConfig{
			SeshFormat:          DefaultSeshFormat,
			AgentsSessionFormat: DefaultAgentsSessionFormat,
			AgentsAgentFormat:   DefaultAgentsAgentFormat,
			WorkSectionFormat:   DefaultWorkSectionFormat,
			WorkTaskFormat:      DefaultWorkTaskFormat,
			Fields:              nil,
		},
		Paths: PathConfig{
			DataDir: "",
			LogFile: "",
		},
		Settings: SettingsConfig{
			ShowFooter:                   true,
			DirectorySuggestions:         defaultDirectorySuggestions,
			DefaultSessionPathSuggestion: "~/Projects/{session}",
			DisplayModes:                 []string{"sesh", "agents", "work"},
			LogLevel:                     "info",
			ShortcutStyle:                "none",
		},
		Autosave: AutosaveConfig{
			Enabled:            true,
			Interval:           defaultAutosaveInterval,
			RestoreLastOnStart: true,
		},
		Snapshot: SnapshotConfig{
			RetentionDays:      defaultSnapshotRetentionDays,
			MaxCount:           0,
			MaxBytes:           0,
			ProcessEnvironment: slices.Clone(DefaultProcessEnvironment),
		},
		Hooks: HookConfig{
			Timeout:           "30s",
			SessionSwitchPre:  nil,
			SessionSwitchPost: nil,
			SessionAttachPre:  nil,
			SessionAttachPost: nil,
			TmuxStartupPost:   nil,
		},
		AgentSessions: AgentSessionsConfig{
			PopupSort:        "position",
			Enabled:          true,
			PopupCounts:      true,
			RefreshInterval:  defaultAgentSessionsRefreshInterval.String(),
			StatusStyle:      "glyph",
			StatusTimeFormat: "relative-seconds",
			StatusTimeStyle:  "",
			Status:           nil,
		},
		Remote: RemoteConfig{
			Enabled:             false,
			SSHPath:             "ssh",
			TmuxPollInterval:    "1s",
			ConnectTimeout:      "5s",
			ActionTimeout:       "2m",
			HeartbeatInterval:   "10s",
			StaleAfter:          "30s",
			HideAfter:           "10m",
			MaxParallelConnects: defaultMaxParallelConnects,
			Connections:         []RemoteConnectionConfig{},
			EnableReverseBridge: true,
			ReturnToOrigin:      true,
		},
		Actions:     nil,
		Projects:    DefaultProjectsConfig(),
		Tasks:       DefaultTasksConfig(),
		TaskActions: nil,
		AgentStatus: AgentStatusConfig{
			Enabled:                  true,
			StorePath:                "",
			TmuxBinary:               "tmux",
			StyleWaiting:             "fg=#1e1e2e,bg=#ffdd57,bold",
			StyleFailed:              "fg=#1e1e2e,bg=#ff5555,bold",
			StyleUnread:              "fg=#1e1e2e,bg=#6c7086,bold",
			Palette:                  map[string]string{},
			SetWindowStatusStyle:     true,
			SetWindowStatusBellStyle: true,
			ActiveBypass:             true,
			ClearFailedOnVisit:       true,
			InitialBackoff:           "500ms",
			MaxBackoff:               "5s",
			BackoffFactor:            defaultBackoffFactor,
			ClearOnShutdown:          true,
			Quiet:                    false,
			Colors:                   nil,
			explicitBools:            nil,
		},
	}
}

func (s SettingsConfig) NormalizedShortcutStyle() string {
	switch strings.ToLower(strings.TrimSpace(s.ShortcutStyle)) {
	case "indent", "numbers", "numbered-indent":
		return "indent"
	case "right", "menu", "badge":
		return "right"
	case "none", "off", "tree":
		return "none"
	default:
		return "none"
	}
}

func (c AgentSessionsConfig) NormalizedStatusTimeFormat() string {
	if c.StatusTimeFormat != "" {
		return NormalizeStatusTimeFormat(c.StatusTimeFormat)
	}

	if c.StatusTimeStyle != "" {
		return NormalizeStatusTimeFormat(c.StatusTimeStyle)
	}

	return "relative-seconds"
}

func (c *AgentStatusConfig) ResolvedPalette() map[string]string {
	palette := map[string]string{
		"waiting": resolveStatusStyle(c.StyleWaiting, c.colorForRole("waiting"), "fg=#1e1e2e,bg=#ffdd57,bold"),
		"failed":  resolveStatusStyle(c.StyleFailed, c.colorForRole("failed"), "fg=#1e1e2e,bg=#ff5555,bold"),
		"unread":  resolveStatusStyle(c.StyleUnread, c.colorForRole("unread"), "fg=#1e1e2e,bg=#6c7086,bold"),
	}

	maps.Copy(palette, c.Palette)

	return palette
}

func (c *Config) ResolvedAgentStatusColors() AgentStatusThemeConfig {
	if c == nil {
		return defaultStatusColors(DefaultThemeConfig())
	}

	colors := defaultStatusColors(c.Theme)
	applyThemeStatusColors(&colors, c.Theme.AgentStatus)

	if c.AgentStatus.Colors != nil {
		applyThemeStatusColors(&colors, c.AgentStatus.Colors)
	}

	return colors
}

func (c *Config) ResolvedPalette() map[string]string {
	if c == nil {
		return DefaultConfig().AgentStatus.ResolvedPalette()
	}

	statusCfg := c.ResolvedAgentStatus()
	colors := c.ResolvedAgentStatusColors()

	palette := map[string]string{
		"waiting": resolveStatusStyle(statusCfg.StyleWaiting, colors.Waiting, "fg=#1e1e2e,bg=#ffdd57,bold"),
		"failed":  resolveStatusStyle(statusCfg.StyleFailed, colors.Failed, "fg=#1e1e2e,bg=#ff5555,bold"),
		"unread":  resolveStatusStyle(statusCfg.StyleUnread, colors.Idle, "fg=#1e1e2e,bg=#6c7086,bold"),
	}

	maps.Copy(palette, statusCfg.Palette)

	return palette
}

func SynthesizeTmuxStyle(color string) string {
	color = strings.TrimSpace(color)
	if color == "" || strings.EqualFold(color, "default") {
		return "bold"
	}

	if strings.HasPrefix(color, "#") && (len(color) == shortHexLength+1 || len(color) == fullHexLength+1) {
		contrast := ContrastForeground(color)
		return fmt.Sprintf("fg=%s,bg=%s,bold", contrast, color)
	}

	return fmt.Sprintf("fg=black,bg=%s,bold", color)
}

func ContrastForeground(hexColor string) string {
	hexColor = strings.TrimPrefix(hexColor, "#")
	if len(hexColor) == shortHexLength {
		hexColor = string([]byte{hexColor[0], hexColor[0], hexColor[1], hexColor[1], hexColor[2], hexColor[2]})
	}

	if len(hexColor) != fullHexLength {
		return "#ffffff"
	}

	r, err := strconv.ParseUint(hexColor[0:2], 16, 8)
	if err != nil {
		return "#ffffff"
	}

	g, err := strconv.ParseUint(hexColor[2:4], 16, 8)
	if err != nil {
		return "#ffffff"
	}

	b, err := strconv.ParseUint(hexColor[4:6], 16, 8)
	if err != nil {
		return "#ffffff"
	}

	yiq := (r*299 + g*587 + b*114) / yiqDivisor
	if yiq >= yiqThreshold {
		return "#1e1e2e"
	}

	return "#ffffff"
}

func (c *AgentStatusConfig) colorForRole(role string) string {
	if c.Colors == nil {
		return ""
	}

	switch role {
	case "waiting":
		return c.Colors.Waiting
	case "failed":
		return c.Colors.Failed
	case "unread":
		return c.Colors.Idle
	default:
		return ""
	}
}

func resolveStatusStyle(configured, customColor, fallback string) string {
	if customColor != "" && (configured == "" || configured == fallback) {
		return SynthesizeTmuxStyle(customColor)
	}

	if configured == "" {
		return fallback
	}

	return configured
}

func defaultStatusColors(theme ThemeConfig) AgentStatusThemeConfig {
	return AgentStatusThemeConfig{
		Running:     theme.Success,
		Waiting:     theme.Secondary,
		Failed:      theme.Error,
		Interrupted: theme.Warning,
		Idle:        theme.Muted,
	}
}

func applyThemeStatusColors(target *AgentStatusThemeConfig, source *AgentStatusThemeConfig) {
	if source == nil {
		return
	}

	if source.Running != "" {
		target.Running = source.Running
	}

	if source.Waiting != "" {
		target.Waiting = source.Waiting
	}

	if source.Failed != "" {
		target.Failed = source.Failed
	}

	if source.Interrupted != "" {
		target.Interrupted = source.Interrupted
	}

	if source.Idle != "" {
		target.Idle = source.Idle
	}
}

func (c *AgentStatusConfig) applyRawBooleans(raw *rawAgentStatus) {
	if raw.Enabled != nil {
		c.Enabled = *raw.Enabled
		c.explicitBools["enabled"] = true
	}

	if raw.SetWindowStatusStyle != nil {
		c.SetWindowStatusStyle = *raw.SetWindowStatusStyle
		c.explicitBools["set_window_status_style"] = true
	}

	if raw.SetWindowStatusBellStyle != nil {
		c.SetWindowStatusBellStyle = *raw.SetWindowStatusBellStyle
		c.explicitBools["set_window_status_bell_style"] = true
	}

	if raw.ActiveBypass != nil {
		c.ActiveBypass = *raw.ActiveBypass
		c.explicitBools["active_bypass"] = true
	}

	if raw.ClearFailedOnVisit != nil {
		c.ClearFailedOnVisit = *raw.ClearFailedOnVisit
		c.explicitBools["clear_failed_on_visit"] = true
	}

	if raw.ClearOnShutdown != nil {
		c.ClearOnShutdown = *raw.ClearOnShutdown
		c.explicitBools["clear_on_shutdown"] = true
	}

	if raw.Quiet != nil {
		c.Quiet = *raw.Quiet
		c.explicitBools["quiet"] = true
	}
}

func (c *AgentStatusConfig) applyRawStringsAndFloats(raw *rawAgentStatus) {
	if raw.StorePath != nil {
		c.StorePath = *raw.StorePath
	}

	if raw.TmuxBinary != nil {
		c.TmuxBinary = *raw.TmuxBinary
	}

	if raw.StyleWaiting != nil {
		c.StyleWaiting = *raw.StyleWaiting
	}

	if raw.StyleFailed != nil {
		c.StyleFailed = *raw.StyleFailed
	}

	if raw.StyleUnread != nil {
		c.StyleUnread = *raw.StyleUnread
	}

	if raw.Colors != nil {
		cloned := *raw.Colors
		c.Colors = &cloned
	}

	if raw.InitialBackoff != nil {
		c.InitialBackoff = *raw.InitialBackoff
	}

	if raw.MaxBackoff != nil {
		c.MaxBackoff = *raw.MaxBackoff
	}

	if raw.BackoffFactor != nil {
		c.BackoffFactor = *raw.BackoffFactor
	}
}

func (c *Config) ResolvedAgentStatus() AgentStatusConfig {
	if c == nil {
		return DefaultConfig().AgentStatus.Clone()
	}

	if c.AgentSessions.Status == nil {
		return c.AgentStatus.Clone()
	}

	return mergeAgentStatus(c.AgentStatus, *c.AgentSessions.Status)
}

func mergeAgentStatus(base, override AgentStatusConfig) AgentStatusConfig {
	merged := base.Clone()

	if override.explicitBools == nil {
		applyImplicitBools(&merged, base, override)
	} else {
		applyExplicitBools(&merged, override)
	}

	applyStringAndPaletteOverrides(&merged, override)

	return merged
}

func applyImplicitBools(merged *AgentStatusConfig, base, override AgentStatusConfig) {
	if override.Enabled != base.Enabled {
		merged.Enabled = override.Enabled
	}

	if override.ClearFailedOnVisit != base.ClearFailedOnVisit {
		merged.ClearFailedOnVisit = override.ClearFailedOnVisit
	}

	if override.SetWindowStatusStyle != base.SetWindowStatusStyle {
		merged.SetWindowStatusStyle = override.SetWindowStatusStyle
	}

	if override.SetWindowStatusBellStyle != base.SetWindowStatusBellStyle {
		merged.SetWindowStatusBellStyle = override.SetWindowStatusBellStyle
	}

	if override.ActiveBypass != base.ActiveBypass {
		merged.ActiveBypass = override.ActiveBypass
	}

	if override.ClearOnShutdown != base.ClearOnShutdown {
		merged.ClearOnShutdown = override.ClearOnShutdown
	}

	if override.Quiet != base.Quiet {
		merged.Quiet = override.Quiet
	}
}

func applyExplicitBools(merged *AgentStatusConfig, override AgentStatusConfig) {
	if override.explicitBools["enabled"] {
		merged.Enabled = override.Enabled
	}

	if override.explicitBools["set_window_status_style"] {
		merged.SetWindowStatusStyle = override.SetWindowStatusStyle
	}

	if override.explicitBools["set_window_status_bell_style"] {
		merged.SetWindowStatusBellStyle = override.SetWindowStatusBellStyle
	}

	if override.explicitBools["active_bypass"] {
		merged.ActiveBypass = override.ActiveBypass
	}

	if override.explicitBools["clear_failed_on_visit"] {
		merged.ClearFailedOnVisit = override.ClearFailedOnVisit
	}

	if override.explicitBools["clear_on_shutdown"] {
		merged.ClearOnShutdown = override.ClearOnShutdown
	}

	if override.explicitBools["quiet"] {
		merged.Quiet = override.Quiet
	}
}

func applyStringAndPaletteOverrides(merged *AgentStatusConfig, override AgentStatusConfig) {
	if override.StorePath != "" {
		merged.StorePath = override.StorePath
	}

	if override.TmuxBinary != "" {
		merged.TmuxBinary = override.TmuxBinary
	}

	applyStyleOverrides(merged, override)

	if override.Colors != nil {
		if merged.Colors == nil {
			cloned := *override.Colors
			merged.Colors = &cloned
		} else {
			applyThemeStatusColors(merged.Colors, override.Colors)
		}
	}

	if len(override.Palette) > 0 {
		if merged.Palette == nil {
			merged.Palette = make(map[string]string, len(override.Palette))
		}

		maps.Copy(merged.Palette, override.Palette)
	}

	if override.InitialBackoff != "" {
		merged.InitialBackoff = override.InitialBackoff
	}

	if override.MaxBackoff != "" {
		merged.MaxBackoff = override.MaxBackoff
	}

	if override.BackoffFactor > 0 {
		merged.BackoffFactor = override.BackoffFactor
	}
}

func applyStyleOverrides(merged *AgentStatusConfig, override AgentStatusConfig) {
	if override.StyleWaiting != "" {
		merged.StyleWaiting = override.StyleWaiting
	}

	if override.StyleFailed != "" {
		merged.StyleFailed = override.StyleFailed
	}

	if override.StyleUnread != "" {
		merged.StyleUnread = override.StyleUnread
	}
}

func (h *HookConfig) setHookField(k string, v any) error {
	if k == "timeout" {
		s, ok := v.(string)
		if !ok {
			return errHookTimeoutMustBeString
		}

		h.Timeout = s

		return nil
	}

	return h.setHookListField(k, v)
}

func (h *HookConfig) setHookListField(k string, v any) error {
	switch k {
	case "session_switch_pre", "session_switch_post", "session_attach_pre", "session_attach_post", "tmux_startup_post":
		cmds, err := parseHookCommands(v)
		if err != nil {
			return err
		}

		switch k {
		case "session_switch_pre":
			h.SessionSwitchPre = cmds
		case "session_switch_post":
			h.SessionSwitchPost = cmds
		case "session_attach_pre":
			h.SessionAttachPre = cmds
		case "session_attach_post":
			h.SessionAttachPost = cmds
		case "tmux_startup_post":
			h.TmuxStartupPost = cmds
		}

		return nil
	default:
		return fmt.Errorf("%w: %s", errUnknownField, k)
	}
}

func parseHookCommands(raw any) ([]HookCommand, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, errExpectedArray
	}

	res := make([]HookCommand, 0, len(list))

	for _, item := range list {
		cmd, err := parseHookCommand(item)
		if err != nil {
			return nil, err
		}

		res = append(res, cmd)
	}

	return res, nil
}

func parseHookCommand(item any) (HookCommand, error) {
	switch v := item.(type) {
	case string:
		return HookCommand{
			ShellCommand: v,
			Command:      "",
			Args:         nil,
			Timeout:      "",
		}, nil
	case map[string]any:
		return parseHookCommandTable(v)
	default:
		return HookCommand{
			ShellCommand: "",
			Command:      "",
			Args:         nil,
			Timeout:      "",
		}, errHookCommandInvalidType
	}
}

func parseHookCommandTable(v map[string]any) (HookCommand, error) {
	var cmd HookCommand

	for propKey, propVal := range v {
		switch propKey {
		case "command":
			s, ok := propVal.(string)
			if !ok {
				return cmd, errHookCommandMustBeString
			}

			cmd.Command = s
		case "args":
			arr, ok := propVal.([]any)
			if !ok {
				return cmd, errHookArgsMustBeArray
			}

			args := make([]string, len(arr))
			for i, a := range arr {
				s, ok := a.(string)
				if !ok {
					return cmd, errHookArgsMustBeStrings
				}

				args[i] = s
			}

			cmd.Args = args
		case "timeout":
			s, ok := propVal.(string)
			if !ok {
				return cmd, errHookTimeoutMustBeString
			}

			cmd.Timeout = s
		default:
			return cmd, fmt.Errorf("%w: %s", errUnknownHookField, propKey)
		}
	}

	return cmd, nil
}

func cloneHookCommands(cmds []HookCommand) []HookCommand {
	if cmds == nil {
		return nil
	}

	out := make([]HookCommand, len(cmds))
	for i, cmd := range cmds {
		out[i] = HookCommand{
			ShellCommand: cmd.ShellCommand,
			Command:      cmd.Command,
			Args:         slices.Clone(cmd.Args),
			Timeout:      cmd.Timeout,
		}
	}

	return out
}

func cloneKeyBinds(binds []KeyBindConfig) []KeyBindConfig {
	if binds == nil {
		return nil
	}

	cloned := make([]KeyBindConfig, len(binds))
	for index, bind := range binds {
		bind.Params = maps.Clone(bind.Params)
		cloned[index] = bind
	}

	return cloned
}
