package config

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/zigai/sesh/internal/display"
)

const (
	defaultAgentSessionsRefreshInterval = 250 * time.Millisecond
	maxRemoteIDBytes                    = 256
	maxRemoteArgumentBytes              = 16 << 10
	minimumSnapshotCount                = 5
	maximumSnapshotRetentionDays        = math.MaxInt64 / int64(24*time.Hour)

	MaxRemoteConnections               = 128
	MaxProcessEnvironmentKeys          = 256
	MaxProcessEnvironmentKeyLength     = 256
	MaxProcessEnvironmentTotalKeyBytes = 4096
)

var (
	errAgentPopupSortInvalid                = errors.New("invalid agent_sessions.popup_sort")
	errHookCommandEmpty                     = errors.New("command cannot be empty")
	errHookCommandAmbiguousFormat           = errors.New("use either a shell command string or command object")
	errHookTimeoutNotPositive               = errors.New("timeout must be positive")
	errFieldUndefined                       = errors.New("undefined custom field")
	errFieldNameInvalid                     = errors.New("field names may only contain a-z, 0-9 and _")
	errFieldRefreshInvalid                  = errors.New("refresh must be a positive duration")
	errDuplicateKeyBinding                  = errors.New("duplicate key binding")
	errInvalidSessionKey                    = errors.New("invalid session key")
	errDisplayModesEmpty                    = errors.New("display modes cannot be empty")
	errDisplayModeInvalid                   = errors.New("invalid display mode")
	errDisplayModeDuplicate                 = errors.New("duplicate display mode")
	errPopupDimensionInvalid                = errors.New("invalid popup dimension")
	errPopupDimensionPositive               = errors.New("expected a positive cell count or percentage")
	errPopupPercentageRange                 = errors.New("percentage must be between 1% and 100%")
	errSnapshotRetentionNegative            = errors.New("snapshot retention values cannot be negative")
	errSnapshotRetentionTooLong             = errors.New("snapshot.retention_days exceeds the supported duration")
	errSnapshotCountTooLow                  = errors.New("snapshot.max_count must be zero or at least five")
	errAgentStatusStyleInvalid              = errors.New("invalid agent_sessions.status_style")
	errAgentStatusTimeFormatInvalid         = errors.New("invalid agent_sessions.status_time_format")
	errAgentStatusTimeStyleInvalid          = errors.New("invalid agent_sessions.status_time_style")
	errInvalidLogLevel                      = errors.New("invalid settings.log_level")
	errShortcutStyleInvalid                 = errors.New("invalid settings.shortcut_style")
	errSessionKeyActionInvalid              = errors.New("invalid settings.session_key_action")
	errSessionPrefixInvalid                 = errors.New("invalid keys.session_prefix")
	errAgentKeysInvalid                     = errors.New("invalid keys.agent_keys")
	errAutosaveIntervalEmpty                = errors.New("invalid autosave.interval: cannot be empty when autosave is enabled")
	errAutosaveIntervalNotPositive          = errors.New("invalid autosave.interval: must be positive duration")
	errAgentRefreshIntervalNotPositive      = errors.New("invalid agent_sessions.refresh_interval: must be positive duration")
	errAgentStatusInitialBackoffNotPositive = errors.New("agent_status.initial_backoff must be positive")
	errAgentStatusMaxBackoffNotPositive     = errors.New("agent_status.max_backoff must be positive")
	errAgentStatusMaxLessThanInitial        = errors.New("agent_status.max_backoff must be greater than or equal to initial_backoff")
	errAgentStatusBackoffFactorNegative     = errors.New("agent_status.backoff_factor must be non-negative")
	errNilConfig                            = errors.New("cannot validate nil config")

	errRemoteStaleAfterTooShort             = errors.New("invalid remote timing: stale_after must be greater than heartbeat_interval")
	errRemoteHideAfterTooShort              = errors.New("invalid remote timing: hide_after must be greater than stale_after")
	errRemoteMaxParallelConnectsNotPositive = errors.New("invalid remote.max_parallel_connects: expected a positive integer")
	errRemoteMaxParallelConnectsTooLarge    = errors.New("invalid remote.max_parallel_connects: exceeds remote connection limit")
	errRemoteConnectionDuplicate            = errors.New("duplicate connection ID")
	errRemoteDurationNotPositive            = errors.New("expected a positive duration")
	errRemoteValueRequired                  = errors.New("value is required")
	errRemoteControlCharacter               = errors.New("value cannot contain control characters")
	errRemoteSurroundingWhitespace          = errors.New("value cannot have leading or trailing whitespace")
	errRemoteStartsWithDash                 = errors.New("value cannot start with a dash")
	errRemoteIDCharacters                   = errors.New("value may contain only letters, numbers, underscores, dashes, and dots")
	errRemoteValueTooLong                   = errors.New("value exceeds the remote protocol limit")
	errRemoteConnectionsTooMany             = errors.New("remote connection count exceeds catalog capacity")

	ErrSnapshotProcessEnvInvalid = errors.New("invalid snapshot.process_environment: invalid environment variable name")
	ErrSnapshotProcessEnvLimit   = errors.New("invalid snapshot.process_environment: environment limit exceeded")
)

func ValidateAutosaveConfig(autosave AutosaveConfig) error {
	if autosave.Interval != "" {
		if _, err := validatePositiveDuration("autosave.interval", autosave.Interval, errAutosaveIntervalNotPositive); err != nil {
			return err
		}
	} else if autosave.Enabled {
		return errAutosaveIntervalEmpty
	}

	return nil
}

func AutosaveInterval(cfg *Config) time.Duration {
	fallback, _ := time.ParseDuration(defaultAutosaveInterval)
	if cfg == nil || cfg.Autosave.Interval == "" {
		return fallback
	}

	dur, err := time.ParseDuration(cfg.Autosave.Interval)
	if err != nil || dur <= 0 {
		return fallback
	}

	return dur
}

func ValidateSnapshotConfig(snapshot SnapshotConfig) error {
	switch {
	case snapshot.RetentionDays < 0:
		return fmt.Errorf("%w: snapshot.retention_days", errSnapshotRetentionNegative)
	case snapshot.MaxCount < 0:
		return fmt.Errorf("%w: snapshot.max_count", errSnapshotRetentionNegative)
	case snapshot.MaxBytes < 0:
		return fmt.Errorf("%w: snapshot.max_bytes", errSnapshotRetentionNegative)
	case int64(snapshot.RetentionDays) > maximumSnapshotRetentionDays:
		return errSnapshotRetentionTooLong
	case snapshot.MaxCount > 0 && snapshot.MaxCount < minimumSnapshotCount:
		return errSnapshotCountTooLow
	default:
		return ValidateProcessEnvironmentKeys(snapshot.ProcessEnvironment)
	}
}

func ValidateProcessEnvironmentKeys(keys []string) error {
	if len(keys) > MaxProcessEnvironmentKeys {
		return ErrSnapshotProcessEnvLimit
	}

	totalBytes := 0

	for _, key := range keys {
		if len(key) == 0 || strings.ContainsAny(key, "=\x00") {
			return ErrSnapshotProcessEnvInvalid
		}

		if len(key) > MaxProcessEnvironmentKeyLength {
			return ErrSnapshotProcessEnvLimit
		}

		totalBytes += len(key)

		if totalBytes > MaxProcessEnvironmentTotalKeyBytes {
			return ErrSnapshotProcessEnvLimit
		}
	}

	return nil
}

func ValidateDisplayConfig(disp DisplayConfig) error {
	formats := []struct {
		name   string
		kind   display.FormatKind
		format string
	}{
		{name: "sesh_format", kind: display.FormatKindSession, format: disp.SeshFormat},
		{name: "agents_session_format", kind: display.FormatKindAgentsSession, format: disp.AgentsSessionFormat},
		{name: "agents_agent_format", kind: display.FormatKindAgentsAgent, format: disp.AgentsAgentFormat},
		{name: "work_section_format", kind: display.FormatKindAgentsSession, format: disp.WorkSectionFormat},
		{name: "work_task_format", kind: display.FormatKindAgentsAgent, format: disp.WorkTaskFormat},
	}

	for _, candidate := range formats {
		template, err := display.Compile(candidate.kind, candidate.format)
		if err != nil {
			return fmt.Errorf("invalid display.%s %q: %w", candidate.name, candidate.format, err)
		}

		for _, name := range template.CustomFields() {
			if _, ok := disp.Fields[name]; !ok {
				return fmt.Errorf("invalid display.%s: %w: custom.%s has no display.fields.%s entry", candidate.name, errFieldUndefined, name, name)
			}
		}
	}

	for name, command := range disp.Fields {
		if err := ValidateFieldCommand(name, command); err != nil {
			return fmt.Errorf("invalid display.fields.%s: %w", name, err)
		}
	}

	return nil
}

func ValidateFieldCommand(name string, command FieldCommand) error {
	if !display.ValidCustomFieldName(name) {
		return errFieldNameInvalid
	}

	if strings.TrimSpace(command.ShellCommand) == "" && strings.TrimSpace(command.Command) == "" {
		return errHookCommandEmpty
	}

	if command.ShellCommand != "" && command.Command != "" {
		return errHookCommandAmbiguousFormat
	}

	if command.Refresh != "" {
		refresh, err := time.ParseDuration(command.Refresh)
		if err != nil || refresh <= 0 {
			return fmt.Errorf("%w: %q", errFieldRefreshInvalid, command.Refresh)
		}
	}

	return nil
}

func ValidatePopupConfig(popup PopupConfig) error {
	dimensions := []struct {
		name  string
		value string
	}{
		{"popup.sesh_width", popup.SeshWidth},
		{"popup.sesh_height", popup.SeshHeight},
		{"popup.agents_width", popup.AgentsWidth},
		{"popup.agents_height", popup.AgentsHeight},
	}

	for _, d := range dimensions {
		if err := validatePopupDimension(d.value); err != nil {
			return fmt.Errorf("%w %s=%q: %w", errPopupDimensionInvalid, d.name, d.value, err)
		}
	}

	return nil
}

func ValidateAgentSessionsConfig(agentSessions AgentSessionsConfig) error {
	switch strings.ToLower(strings.TrimSpace(agentSessions.PopupSort)) {
	case "", "position", "name", "attention":
	default:
		return fmt.Errorf("%w %q: expected position, name, or attention", errAgentPopupSortInvalid, agentSessions.PopupSort)
	}

	if agentSessions.RefreshInterval != "" {
		dur, err := time.ParseDuration(agentSessions.RefreshInterval)
		if err != nil {
			return fmt.Errorf("invalid agent_sessions.refresh_interval %q: %w", agentSessions.RefreshInterval, err)
		}

		if dur <= 0 {
			return fmt.Errorf("%w %q", errAgentRefreshIntervalNotPositive, agentSessions.RefreshInterval)
		}
	}

	if err := validateAgentSessionStatusFormats(agentSessions); err != nil {
		return err
	}

	if agentSessions.Status != nil {
		if err := ValidateAgentStatusConfig(*agentSessions.Status); err != nil {
			return err
		}
	}

	return nil
}

func ValidateAgentStatusConfig(status AgentStatusConfig) error {
	var (
		initDur time.Duration
		maxDur  time.Duration
		err     error
	)

	if status.InitialBackoff != "" {
		initDur, err = validatePositiveDuration("agent_status.initial_backoff", status.InitialBackoff, errAgentStatusInitialBackoffNotPositive)
		if err != nil {
			return err
		}
	}

	if status.MaxBackoff != "" {
		maxDur, err = validatePositiveDuration("agent_status.max_backoff", status.MaxBackoff, errAgentStatusMaxBackoffNotPositive)
		if err != nil {
			return err
		}
	}

	if initDur > 0 && maxDur > 0 && maxDur < initDur {
		return fmt.Errorf("%w: max (%s) < initial (%s)", errAgentStatusMaxLessThanInitial, status.MaxBackoff, status.InitialBackoff)
	}

	if status.BackoffFactor < 0 {
		return fmt.Errorf("%w: got %f", errAgentStatusBackoffFactorNegative, status.BackoffFactor)
	}

	return nil
}

func ValidateRemoteConfig(remote RemoteConfig) error {
	if err := validateRemoteArgument("ssh_path", remote.SSHPath, true); err != nil {
		return err
	}

	if err := validateRemoteTiming(remote); err != nil {
		return err
	}

	if remote.MaxParallelConnects <= 0 {
		return errRemoteMaxParallelConnectsNotPositive
	}

	if remote.MaxParallelConnects > MaxRemoteConnections {
		return errRemoteMaxParallelConnectsTooLarge
	}

	return validateRemoteConnections(remote.Connections)
}

func ValidateHookConfig(hooks HookConfig) error {
	if hooks.Timeout != "" {
		if err := validateHookTimeout("hooks timeout", hooks.Timeout); err != nil {
			return err
		}
	}

	hookLists := []struct {
		name     string
		commands []HookCommand
	}{
		{"session_switch_pre", hooks.SessionSwitchPre},
		{"session_switch_post", hooks.SessionSwitchPost},
		{"session_attach_pre", hooks.SessionAttachPre},
		{"session_attach_post", hooks.SessionAttachPost},
		{"tmux_startup_post", hooks.TmuxStartupPost},
	}

	for _, list := range hookLists {
		for i, command := range list.commands {
			if err := ValidateHookCommand(command); err != nil {
				return fmt.Errorf("invalid hooks.%s[%d]: %w", list.name, i, err)
			}
		}
	}

	return nil
}

func ValidateHookCommand(command HookCommand) error {
	if strings.TrimSpace(command.ShellCommand) == "" && strings.TrimSpace(command.Command) == "" {
		return errHookCommandEmpty
	}

	if command.ShellCommand != "" && command.Command != "" {
		return errHookCommandAmbiguousFormat
	}

	if command.Timeout != "" {
		if err := validateHookTimeout("timeout", command.Timeout); err != nil {
			return err
		}
	}

	return nil
}

func ValidateKeyConfig(keys KeyConfig) error {
	seen := map[string]string{}

	keyEntries := []struct {
		name string
		key  string
	}{
		{"help", keys.Help},
		{"cycle_mode", keys.CycleMode},
		{"agent_sort", keys.AgentSort},
		{"page_up", keys.PageUp},
		{"page_down", keys.PageDown},
		{"top", keys.Top},
		{"bottom", keys.Bottom},
	}

	for _, entry := range keyEntries {
		normalized := PickerKeyName(entry.key)
		if normalized == "" {
			continue
		}

		if previous, exists := seen[normalized]; exists {
			return fmt.Errorf("%w: %s and %s both use %q", errDuplicateKeyBinding, previous, entry.name, entry.key)
		}

		seen[normalized] = entry.name
	}

	if err := validateSessionPrefix(keys, seen); err != nil {
		return err
	}

	if err := validateAgentKeys(keys.AgentKeys); err != nil {
		return err
	}

	return validateSessionKeys(keys, seen)
}

func ValidStatusTimeFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "relative", "relative-seconds", "compact", "human", "default", "age", "hh:mm",
		"clock", "duration", "hms", "hh:mm:ss", "00:00:00", "00:01:23", "timer", "stopwatch", "elapsed":
		return true
	default:
		return false
	}
}

func NormalizeStatusTimeFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "default", "relative-seconds":
		return "relative-seconds"
	case "hh:mm":
		return "hh:mm"
	case "clock", "duration", "hms", "hh:mm:ss", "00:00:00", "00:01:23", "timer", "stopwatch", "elapsed":
		return "clock"
	default:
		return "relative"
	}
}

func ValidateThemeConfig(theme ThemeConfig) error {
	if theme.Preset != "" {
		if _, ok := LookupPreset(theme.Preset); !ok {
			return fmt.Errorf("%w %q: expected dracula, catppuccin, catppuccin-latte, tokyo-night, gruvbox, nord, or terminal", errThemePresetInvalid, theme.Preset)
		}
	}

	return nil
}

func validateConfig(cfg *Config) error {
	if cfg == nil {
		return errNilConfig
	}

	validators := []func() error{
		func() error { return ValidateThemeConfig(cfg.Theme) },
		func() error { return ValidateAutosaveConfig(cfg.Autosave) },
		func() error { return ValidateKeyConfig(cfg.Keys) },
		func() error { return ValidateHookConfig(cfg.Hooks) },
		func() error { return validateDisplayModes(cfg.Settings.DisplayModes) },
		func() error { return validateLogLevel(cfg.Settings.LogLevel) },
		func() error { return validateShortcutStyle(cfg.Settings.ShortcutStyle) },
		func() error { return validateSessionKeyAction(cfg.Settings.SessionKeyAction) },
		func() error { return ValidateDisplayConfig(cfg.Display) },
		func() error { return ValidatePopupConfig(cfg.Popup) },
		func() error { return ValidateSnapshotConfig(cfg.Snapshot) },
		func() error { return ValidateAgentSessionsConfig(cfg.AgentSessions) },
		func() error { return ValidateAgentStatusConfig(cfg.AgentStatus) },
		func() error { return ValidateRemoteConfig(cfg.Remote) },
		func() error { return ValidateProjectsConfig(cfg.Projects) },
		func() error { return ValidateTasksConfig(cfg.Tasks) },
		func() error { return ValidateTaskActions(cfg) },
		func() error { return ValidateTaskWindows(cfg) },
	}
	for _, v := range validators {
		if err := v(); err != nil {
			return err
		}
	}

	return nil
}

func validateRemoteTiming(remote RemoteConfig) error {
	if _, err := parseRemoteDuration("tmux_poll_interval", remote.TmuxPollInterval); err != nil {
		return err
	}

	if _, err := parseRemoteDuration("action_timeout", remote.ActionTimeout); err != nil {
		return err
	}

	if _, err := parseRemoteDuration("connect_timeout", remote.ConnectTimeout); err != nil {
		return err
	}

	heartbeatInterval, err := parseRemoteDuration("heartbeat_interval", remote.HeartbeatInterval)
	if err != nil {
		return err
	}

	staleAfter, err := parseRemoteDuration("stale_after", remote.StaleAfter)
	if err != nil {
		return err
	}

	hideAfter, err := parseRemoteDuration("hide_after", remote.HideAfter)
	if err != nil {
		return err
	}

	if staleAfter <= heartbeatInterval {
		return errRemoteStaleAfterTooShort
	}

	if hideAfter <= staleAfter {
		return errRemoteHideAfterTooShort
	}

	return nil
}

func validateRemoteConnections(connections []RemoteConnectionConfig) error {
	if len(connections) > MaxRemoteConnections {
		return errRemoteConnectionsTooMany
	}

	seen := make(map[string]struct{}, len(connections))
	for i, connection := range connections {
		if err := validateRemoteConnection(connection); err != nil {
			return fmt.Errorf("invalid remote.connections[%d]: %w", i, err)
		}

		if _, ok := seen[connection.ID]; ok {
			return fmt.Errorf("invalid remote.connections[%d].id %q: %w", i, connection.ID, errRemoteConnectionDuplicate)
		}

		seen[connection.ID] = struct{}{}
	}

	return nil
}

func parseRemoteDuration(name string, value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid remote.%s %q: %w", name, value, err)
	}

	if duration <= 0 {
		return 0, fmt.Errorf("invalid remote.%s %q: %w", name, value, errRemoteDurationNotPositive)
	}

	return duration, nil
}

func validateRemoteConnection(connection RemoteConnectionConfig) error {
	if err := validateRemoteID(connection.ID); err != nil {
		return fmt.Errorf("id %q: %w", connection.ID, err)
	}

	if err := validateRemoteArgument("host", connection.Host, true); err != nil {
		return err
	}

	if err := validateRemoteArgument("label", connection.Label, false); err != nil {
		return err
	}

	if err := validateRemoteArgument("sesh_path", connection.SeshPath, false); err != nil {
		return err
	}

	return validateRemoteArgument("tmux_socket", connection.TmuxSocket, false)
}

func validateRemoteID(id string) error {
	if id == "" {
		return errRemoteValueRequired
	}

	if len(id) > maxRemoteIDBytes {
		return errRemoteValueTooLong
	}

	if containsControlCharacter(id) {
		return errRemoteControlCharacter
	}

	if strings.TrimSpace(id) != id {
		return errRemoteSurroundingWhitespace
	}

	if strings.HasPrefix(id, "-") {
		return errRemoteStartsWithDash
	}

	for _, character := range id {
		if !isRemoteIDCharacter(character) {
			return errRemoteIDCharacters
		}
	}

	return nil
}

func isRemoteIDCharacter(character rune) bool {
	return (character >= 'a' && character <= 'z') ||
		(character >= 'A' && character <= 'Z') ||
		(character >= '0' && character <= '9') ||
		character == '_' ||
		character == '-' ||
		character == '.'
}

func validateRemoteArgument(name string, value string, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("%s: %w", name, errRemoteValueRequired)
		}

		return nil
	}

	if len(value) > maxRemoteArgumentBytes {
		return fmt.Errorf("%s: %w", name, errRemoteValueTooLong)
	}

	if strings.HasPrefix(value, "-") {
		return fmt.Errorf("%s %q: %w", name, value, errRemoteStartsWithDash)
	}

	if containsControlCharacter(value) {
		return fmt.Errorf("%s %q: %w", name, value, errRemoteControlCharacter)
	}

	return nil
}

func containsControlCharacter(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func validatePopupDimension(value string) error {
	if value == "auto" || value == "dynamic" {
		return nil
	}

	percentage := strings.HasSuffix(value, "%")
	if percentage {
		value = strings.TrimSuffix(value, "%")
	}

	if value == "" {
		return errPopupDimensionPositive
	}

	size, err := strconv.Atoi(value)
	if err != nil || size <= 0 {
		return errPopupDimensionPositive
	}

	if percentage && size > 100 {
		return errPopupPercentageRange
	}

	return nil
}

func validateDisplayModes(modes []string) error {
	if len(modes) == 0 {
		return errDisplayModesEmpty
	}

	seen := make(map[string]bool, len(modes))
	for _, mode := range modes {
		normalized := strings.ToLower(strings.TrimSpace(mode))
		if normalized != "sesh" && normalized != "agents" && normalized != "work" {
			return fmt.Errorf("%w %q: expected sesh, agents or work", errDisplayModeInvalid, mode)
		}

		if seen[normalized] {
			return fmt.Errorf("%w %q", errDisplayModeDuplicate, mode)
		}

		seen[normalized] = true
	}

	return nil
}

func validateShortcutStyle(style string) error {
	trimmed := strings.ToLower(strings.TrimSpace(style))
	if trimmed == "" {
		return nil
	}

	switch trimmed {
	case "none", "indent", "right":
		return nil
	default:
		return fmt.Errorf("%w %q: expected none, indent, or right", errShortcutStyleInvalid, style)
	}
}

func validateSessionKeyAction(sessionKeyAction string) error {
	switch sessionKeyAction {
	case "focus", "open":
		return nil
	default:
		return fmt.Errorf("%w %q: expected focus or open", errSessionKeyActionInvalid, sessionKeyAction)
	}
}

func validateLogLevel(level string) error {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "debug", "info", "warn", "error", "disabled":
		return nil
	default:
		return fmt.Errorf("%w %q: expected debug, info, warn, error, or disabled", errInvalidLogLevel, level)
	}
}

func validateHookTimeout(label string, value string) error {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("invalid %s %q: %w", label, value, err)
	}

	if duration <= 0 {
		return fmt.Errorf("invalid %s %q: %w", label, value, errHookTimeoutNotPositive)
	}

	return nil
}

func validateAgentSessionStatusFormats(agentSessions AgentSessionsConfig) error {
	if agentSessions.StatusStyle != "" {
		switch strings.ToLower(strings.TrimSpace(agentSessions.StatusStyle)) {
		case "glyph", "circle", "circles", "icon", "icons", "text":
		default:
			return fmt.Errorf("%w %q: expected glyph or text", errAgentStatusStyleInvalid, agentSessions.StatusStyle)
		}
	}

	if agentSessions.StatusTimeFormat != "" {
		if !ValidStatusTimeFormat(agentSessions.StatusTimeFormat) {
			return fmt.Errorf("%w %q: expected relative, relative-seconds, clock (00:01:23), or hh:mm (00:01)", errAgentStatusTimeFormatInvalid, agentSessions.StatusTimeFormat)
		}
	}

	if agentSessions.StatusTimeStyle != "" {
		if !ValidStatusTimeFormat(agentSessions.StatusTimeStyle) {
			return fmt.Errorf("%w %q: expected relative, clock (00:01:23), or hh:mm (00:01)", errAgentStatusTimeStyleInvalid, agentSessions.StatusTimeStyle)
		}
	}

	return nil
}

func validatePositiveDuration(label, raw string, errNotPositive error) (time.Duration, error) {
	dur, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", label, raw, err)
	}

	if dur <= 0 {
		return 0, fmt.Errorf("%w, got %q", errNotPositive, raw)
	}

	return dur, nil
}

func validateSessionPrefix(keys KeyConfig, seen map[string]string) error {
	if keys.SessionKeysHoldAlt() {
		return nil
	}

	prefix := PickerKeyName(keys.SessionPrefix)
	if prefix == "" || PickerKeyTypes(prefix) {
		return fmt.Errorf("%w %q: expected alt or a key that does not type into the search field", errSessionPrefixInvalid, keys.SessionPrefix)
	}

	for _, fixed := range [][]string{
		PickerSelectKeys, PickerCloseKeys, PickerHandoffKeys,
		PickerStepUpKeys, PickerStepDownKeys, PickerGroupUpKeys, PickerGroupDownKeys,
	} {
		if slices.Contains(fixed, prefix) {
			return fmt.Errorf("%w %q: the picker already uses it", errSessionPrefixInvalid, keys.SessionPrefix)
		}
	}

	if previous, exists := seen[prefix]; exists {
		return fmt.Errorf("%w: %s and session_prefix both use %q", errDuplicateKeyBinding, previous, keys.SessionPrefix)
	}

	seen[prefix] = "session_prefix"

	return nil
}

func validateAgentKeys(agentKeys string) error {
	if agentKeys == "" {
		return fmt.Errorf("%w: expected at least one key", errAgentKeysInvalid)
	}

	used := make(map[rune]bool, len(agentKeys))

	for _, key := range agentKeys {
		if !ValidAgentKey(key) {
			return fmt.Errorf("%w %q: %q is not a lowercase letter or digit", errAgentKeysInvalid, agentKeys, key)
		}

		if used[key] {
			return fmt.Errorf("%w %q: %q appears twice", errAgentKeysInvalid, agentKeys, key)
		}

		used[key] = true
	}

	return nil
}

func validateSessionKeys(keys KeyConfig, seen map[string]string) error {
	for _, name := range slices.Sorted(maps.Keys(keys.Sessions)) {
		letter := keys.Sessions[name]
		if name == "" || !ValidSessionKey(letter) {
			return fmt.Errorf("%w: sessions.%q = %q must be one lowercase letter", errInvalidSessionKey, name, letter)
		}

		key := keys.PickerSessionKey(letter)
		if previous, exists := seen[key]; exists {
			return fmt.Errorf("%w: %s and sessions.%q both use %q", errDuplicateKeyBinding, previous, name, key)
		}

		seen[key] = fmt.Sprintf("sessions.%q", name)
	}

	return nil
}
