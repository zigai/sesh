package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/samber/oops"
	"github.com/zigai/strata"
)

var errSourceFilePathRequired = errors.New("configuration source requires an explicit file path")

type LoadOption func(*loadOptions)

type Source struct {
	Path          string
	WithoutConfig bool
	Input         string
}

type loadOptions struct {
	explicitPath  string
	withoutConfig bool
	input         io.Reader
}

type rawThemePresetProbe struct {
	Theme struct {
		Preset      *string                 `toml:"preset"`
		Primary     *string                 `toml:"primary"`
		Secondary   *string                 `toml:"secondary"`
		Muted       *string                 `toml:"muted"`
		Error       *string                 `toml:"error"`
		Warning     *string                 `toml:"warning"`
		Success     *string                 `toml:"success"`
		Border      *string                 `toml:"border"`
		Selected    *string                 `toml:"selected"`
		Background  *string                 `toml:"background"`
		Foreground  *string                 `toml:"foreground"`
		AgentStatus *AgentStatusThemeConfig `toml:"agent_status"`
	} `toml:"theme"`
}

func WithInput(r io.Reader) LoadOption {
	return func(opts *loadOptions) {
		opts.input = r
	}
}

func WithExplicitPath(path string) LoadOption {
	return func(opts *loadOptions) {
		opts.explicitPath = path
	}
}

func WithoutConfig() LoadOption {
	return func(opts *loadOptions) {
		opts.withoutConfig = true
	}
}

func Load(opts ...LoadOption) (*Config, error) {
	cfg := DefaultConfig()
	if err := LoadInto(cfg, opts...); err != nil {
		return nil, err
	}

	return cfg, nil
}

func ReadSourceFile(path string) ([]byte, error) {
	if path == "" || path == "-" {
		return nil, oops.In("config").
			Code("config_invalid_path").
			With("operation", "read_source", "reason", "file_path_required").
			Wrap(errSourceFilePathRequired)
	}

	data, hasFile, err := readConfigFile(path, nil)
	if err != nil {
		return nil, err
	}

	if !hasFile {
		return nil, oops.In("config").
			Code("config_not_found").
			With("operation", "read_source", "reason", "file_not_found").
			Wrap(os.ErrNotExist)
	}

	return data, nil
}

func LoadInto(target *Config, opts ...LoadOption) error {
	if target == nil {
		return oops.In("config").
			Code("config_invalid").
			With("operation", "load", "reason", "nil_target").
			Wrap(errNilConfig)
	}

	options := &loadOptions{
		explicitPath:  "",
		withoutConfig: false,
		input:         nil,
	}
	for _, opt := range opts {
		opt(options)
	}

	baseCfg := DefaultConfig()
	stagingCfg := baseCfg.Clone()

	files, err := loadConfigFiles(options)
	if err != nil {
		return err
	}

	if err := loadAndValidate(stagingCfg, baseCfg, files); err != nil {
		return err
	}

	source, err := loadedSource(options, files)
	if err != nil {
		return err
	}

	stagingCfg.source = source
	*target = *stagingCfg.Clone()

	return nil
}

func loadedSource(opts *loadOptions, files [][]byte) (Source, error) {
	source := Source{Path: "", WithoutConfig: opts.withoutConfig, Input: ""}
	if opts.withoutConfig {
		return source, nil
	}

	source.Path = opts.explicitPath
	if source.Path == "" {
		source.Path = os.Getenv("SESH_CONFIG")
	}

	if source.Path == "" {
		return source, nil
	}

	if source.Path == "-" {
		source.Input = string(files[0])

		return source, nil
	}

	path, err := filepath.Abs(source.Path)
	if err != nil {
		return source, oops.In("config").Code("config_invalid_path").Wrap(err)
	}

	source.Path = path

	return source, nil
}

func loadConfigFiles(opts *loadOptions) ([][]byte, error) {
	if opts.withoutConfig {
		return nil, nil
	}

	path := opts.explicitPath
	if path == "" {
		path = os.Getenv("SESH_CONFIG")
	}

	if path != "" {
		data, _, err := readConfigFile(path, opts.input)
		if err != nil {
			return nil, err
		}

		return [][]byte{data}, nil
	}

	return loadDiscoveredConfigFiles()
}

func loadDiscoveredConfigFiles() ([][]byte, error) {
	var files [][]byte

	userPath := DefaultConfigPath()

	for _, source := range []string{"/etc/sesh/config.toml", userPath} {
		var (
			data    []byte
			hasFile bool
			err     error
		)

		if source == userPath {
			data, hasFile, err = readConfigFile("", nil)
		} else {
			data, hasFile, err = readOptionalConfigFile(source)
		}

		if err != nil {
			return nil, err
		}

		if hasFile {
			files = append(files, data)
		}
	}

	return files, nil
}

func readOptionalConfigFile(path string) ([]byte, bool, error) {
	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, false, oops.In("config").Code("config_invalid_path").With("operation", "load", "reason", "resolve_abs_path").Wrap(err)
	}

	return readPinnedConfigFile(filepath.Dir(absPath), filepath.Base(absPath), false)
}

func validateLoaded(target *Config) error {
	if err := validateConfig(target); err != nil {
		return enrichValidationError(err)
	}

	return nil
}

func readConfigFile(explicitPath string, input io.Reader) ([]byte, bool, error) {
	if explicitPath == "-" {
		r := input
		if r == nil {
			r = os.Stdin
		}

		data, err := readStdinConfig(r)
		if err != nil {
			return nil, false, err
		}

		return data, true, nil
	}

	targetPath := explicitPath

	isExplicit := targetPath != ""
	if !isExplicit {
		targetPath = DefaultConfigPath()
	}

	absPath, err := filepath.Abs(filepath.Clean(targetPath))
	if err != nil {
		return nil, false, oops.In("config").
			Code("config_invalid_path").
			With("operation", "load", "reason", "resolve_abs_path").
			Wrap(err)
	}

	if !isExplicit {
		if realPath, evalErr := filepath.EvalSymlinks(absPath); evalErr == nil {
			absPath = realPath
		}
	}

	parentDir := filepath.Dir(absPath)
	fileName := filepath.Base(absPath)

	return readPinnedConfigFile(parentDir, fileName, isExplicit)
}

func readPinnedConfigFile(parentDir, fileName string, isExplicit bool) ([]byte, bool, error) {
	dir, err := openPinnedDir(parentDir, false)
	if err != nil {
		notFoundErr := handleNotFoundError(err, isExplicit)
		return nil, false, notFoundErr
	}

	fileBytes, readErr := dir.ReadFile(fileName, maxConfigFileSize)
	closeErr := dir.Close()

	if readErr != nil {
		notFoundErr := handleNotFoundError(errors.Join(readErr, closeErr), isExplicit)
		return nil, false, notFoundErr
	}

	if closeErr != nil {
		return nil, false, closeErr
	}

	return fileBytes, true, nil
}

func handleNotFoundError(err error, isExplicit bool) error {
	if errors.Is(err, os.ErrNotExist) {
		if !isExplicit {
			return nil
		}

		return oops.In("config").
			Code("config_not_found").
			With("operation", "load", "reason", "file_not_found").
			Wrap(err)
	}

	return err
}

func readStdinConfig(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxConfigFileSize+1))
	if err != nil {
		return nil, oops.In("config").
			Code("config_read_failed").
			With("operation", "load", "reason", "read_stdin_failed").
			Wrap(err)
	}

	if int64(len(data)) > maxConfigFileSize {
		return nil, oops.In("config").
			Code("config_file_too_large").
			With("operation", "load", "reason", "file_too_large").
			Wrap(ErrFileTooLarge)
	}

	return data, nil
}

func loadAndValidate(target *Config, baseCfg *Config, files [][]byte) error {
	current := baseCfg.Clone()
	for _, data := range files {
		if err := decodeFileWithPresets(data, current); err != nil {
			return enrichLoadError(err)
		}
	}

	if err := loadConfigEnvironment(target, current); err != nil {
		return err
	}

	applyEnvironmentThemePreset(&target.Theme, current.Theme)

	return validateLoaded(target)
}

// applyEnvironmentThemePreset expands a preset that only the environment selected, keeping
// every other theme color the environment set explicitly. Invalid names are left for validation.
func applyEnvironmentThemePreset(theme *ThemeConfig, fileTheme ThemeConfig) {
	name := strings.TrimSpace(theme.Preset)
	if name == "" || strings.EqualFold(name, strings.TrimSpace(fileTheme.Preset)) {
		return
	}

	preset, ok := LookupPreset(name)
	if !ok {
		return
	}

	fromEnv := *theme
	theme.applyPreset(preset)

	for _, field := range []struct {
		target        *string
		fromEnv, file string
	}{
		{&theme.Primary, fromEnv.Primary, fileTheme.Primary},
		{&theme.Secondary, fromEnv.Secondary, fileTheme.Secondary},
		{&theme.Muted, fromEnv.Muted, fileTheme.Muted},
		{&theme.Error, fromEnv.Error, fileTheme.Error},
		{&theme.Warning, fromEnv.Warning, fileTheme.Warning},
		{&theme.Success, fromEnv.Success, fileTheme.Success},
		{&theme.Border, fromEnv.Border, fileTheme.Border},
		{&theme.Selected, fromEnv.Selected, fileTheme.Selected},
		{&theme.Background, fromEnv.Background, fileTheme.Background},
		{&theme.Foreground, fromEnv.Foreground, fileTheme.Foreground},
	} {
		if field.fromEnv != field.file {
			*field.target = field.fromEnv
		}
	}
}

func decodeFileWithPresets(data []byte, current *Config) error {
	if err := strictDecode(data, current); err != nil {
		return err
	}

	var probe rawThemePresetProbe

	_ = toml.Unmarshal(data, &probe)

	return applyThemeProbe(&current.Theme, &probe)
}

func applyThemeProbe(theme *ThemeConfig, probe *rawThemePresetProbe) error {
	if probe.Theme.Preset == nil || *probe.Theme.Preset == "" {
		return nil
	}

	preset, ok := LookupPreset(*probe.Theme.Preset)
	if !ok {
		return fmt.Errorf("%w %q: expected dracula, catppuccin, catppuccin-latte, tokyo-night, gruvbox, nord, or terminal", errThemePresetInvalid, *probe.Theme.Preset)
	}

	theme.applyPreset(preset)
	overlayThemeFields(theme, probe)

	return nil
}

func overlayThemeFields(theme *ThemeConfig, probe *rawThemePresetProbe) {
	if probe.Theme.Primary != nil {
		theme.Primary = *probe.Theme.Primary
	}

	if probe.Theme.Secondary != nil {
		theme.Secondary = *probe.Theme.Secondary
	}

	if probe.Theme.Muted != nil {
		theme.Muted = *probe.Theme.Muted
	}

	if probe.Theme.Error != nil {
		theme.Error = *probe.Theme.Error
	}

	if probe.Theme.Warning != nil {
		theme.Warning = *probe.Theme.Warning
	}

	if probe.Theme.Success != nil {
		theme.Success = *probe.Theme.Success
	}

	if probe.Theme.Border != nil {
		theme.Border = *probe.Theme.Border
	}

	if probe.Theme.Selected != nil {
		theme.Selected = *probe.Theme.Selected
	}

	if probe.Theme.Background != nil {
		theme.Background = *probe.Theme.Background
	}

	if probe.Theme.Foreground != nil {
		theme.Foreground = *probe.Theme.Foreground
	}

	if probe.Theme.AgentStatus != nil {
		overlayAgentStatusTheme(theme, probe.Theme.AgentStatus)
	}
}

func overlayAgentStatusTheme(theme *ThemeConfig, status *AgentStatusThemeConfig) {
	if theme.AgentStatus == nil {
		theme.AgentStatus = &AgentStatusThemeConfig{
			Running:     "",
			Waiting:     "",
			Failed:      "",
			Interrupted: "",
			Idle:        "",
		}
	}

	if status.Running != "" {
		theme.AgentStatus.Running = status.Running
	}

	if status.Waiting != "" {
		theme.AgentStatus.Waiting = status.Waiting
	}

	if status.Failed != "" {
		theme.AgentStatus.Failed = status.Failed
	}

	if status.Interrupted != "" {
		theme.AgentStatus.Interrupted = status.Interrupted
	}

	if status.Idle != "" {
		theme.AgentStatus.Idle = status.Idle
	}
}

func loadConfigEnvironment(target *Config, baseCfg *Config) error {
	strataOpts := []strata.Option{
		strata.WithoutFileDiscovery(),
		strata.WithDefaults(*baseCfg),
		strata.WithEnvPrefix("SESH_"),
	}

	_, err := strata.LoadInto(target, strataOpts...)
	if err != nil {
		return enrichLoadError(err)
	}

	return nil
}

func strictDecode(data []byte, target any) error {
	d := toml.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	d.EnableUnmarshalerInterface()

	if err := d.Decode(target); err != nil {
		return fmt.Errorf("toml decode: %w", err)
	}

	return nil
}

func enrichLoadError(err error) error {
	if err == nil {
		return nil
	}

	if strictErr, ok := errors.AsType[*toml.StrictMissingError](err); ok {
		return enrichStrictMissingError(strictErr, err)
	}

	if decodeErr, ok := errors.AsType[*toml.DecodeError](err); ok {
		return enrichTOMLDecodeError(decodeErr, err)
	}

	return oops.In("config").
		Code("config_invalid").
		With("operation", "load", "reason", "load_failed").
		Wrap(err)
}

func enrichStrictMissingError(strictErr *toml.StrictMissingError, rawErr error) error {
	var (
		line int
		col  int
	)

	if len(strictErr.Errors) > 0 {
		e := strictErr.Errors[0]
		line, col = e.Position()
	}

	b := oops.In("config").
		Code("config_invalid").
		With("operation", "load", "reason", "unknown_field")

	if line > 0 {
		b = b.With("line", line, "column", col)
	}

	return b.Wrap(rawErr)
}

func enrichTOMLDecodeError(decodeErr *toml.DecodeError, rawErr error) error {
	line, col := decodeErr.Position()
	msg := decodeErr.Error()
	reason := "decode_error"

	if strings.Contains(msg, "already defined") {
		reason = "duplicate_key"
	} else if strings.Contains(msg, "cannot decode") {
		reason = "invalid_type"
	}

	b := oops.In("config").
		Code("config_invalid").
		With("operation", "load", "reason", reason)

	if line > 0 {
		b = b.With("line", line, "column", col)
	}

	return b.Wrap(rawErr)
}

func enrichValidationError(err error) error {
	if err == nil {
		return nil
	}

	return oops.In("config").
		Code("config_invalid").
		With("operation", "validate", "reason", "validation_failed").
		Wrap(err)
}
