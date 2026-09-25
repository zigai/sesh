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

type loadOptions struct {
	explicitPath  string
	withoutConfig bool
	defaults      *Config
	input         io.Reader
}

func WithInput(r io.Reader) LoadOption {
	return func(o *loadOptions) {
		o.input = r
	}
}

func WithExplicitPath(path string) LoadOption {
	return func(o *loadOptions) {
		o.explicitPath = path
	}
}

func WithoutConfig() LoadOption {
	return func(o *loadOptions) {
		o.withoutConfig = true
	}
}

func WithDefaults(defaults *Config) LoadOption {
	return func(o *loadOptions) {
		o.defaults = defaults
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
		defaults:      nil,
		input:         nil,
	}
	for _, opt := range opts {
		opt(options)
	}

	var baseCfg *Config
	if options.defaults != nil {
		baseCfg = options.defaults.Clone()
	} else {
		baseCfg = DefaultConfig()
	}

	stagingCfg := baseCfg.Clone()

	files, err := loadConfigFiles(options)
	if err != nil {
		return err
	}

	if err := loadAndValidate(stagingCfg, baseCfg, files); err != nil {
		return err
	}

	*target = *stagingCfg.Clone()

	return nil
}

func loadConfigFiles(options *loadOptions) ([][]byte, error) {
	if options.withoutConfig {
		return nil, nil
	}

	path := options.explicitPath
	if path == "" {
		path = os.Getenv("SESH_CONFIG")
	}

	if path != "" {
		data, _, err := readConfigFile(path, options.input)
		if err != nil {
			return nil, err
		}

		return [][]byte{data}, nil
	}

	return loadDiscoveredConfigFiles()
}

func loadDiscoveredConfigFiles() ([][]byte, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, oops.In("config").Code("config_invalid_path").With("operation", "load", "reason", "resolve_cwd").Wrap(err)
	}

	var files [][]byte

	userPath := DefaultConfigPath()

	for _, source := range []string{"/etc/sesh/config.toml", userPath, filepath.Join(cwd, ".sesh.toml")} {
		var data []byte

		var hasFile bool
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

	return validateLoaded(target)
}

func decodeFileWithPresets(data []byte, current *Config) error {
	if err := strictDecode(data, current); err != nil {
		return err
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

	var probe rawThemePresetProbe
	_ = toml.Unmarshal(data, &probe)

	if probe.Theme.Preset != nil && *probe.Theme.Preset != "" {
		preset, ok := LookupPreset(*probe.Theme.Preset)
		if !ok {
			return fmt.Errorf("%w %q: expected dracula, catppuccin, catppuccin-latte, tokyo-night, gruvbox, nord, or terminal", errThemePresetInvalid, *probe.Theme.Preset)
		}
		current.Theme.applyPreset(preset)

		if probe.Theme.Primary != nil {
			current.Theme.Primary = *probe.Theme.Primary
		}
		if probe.Theme.Secondary != nil {
			current.Theme.Secondary = *probe.Theme.Secondary
		}
		if probe.Theme.Muted != nil {
			current.Theme.Muted = *probe.Theme.Muted
		}
		if probe.Theme.Error != nil {
			current.Theme.Error = *probe.Theme.Error
		}
		if probe.Theme.Warning != nil {
			current.Theme.Warning = *probe.Theme.Warning
		}
		if probe.Theme.Success != nil {
			current.Theme.Success = *probe.Theme.Success
		}
		if probe.Theme.Border != nil {
			current.Theme.Border = *probe.Theme.Border
		}
		if probe.Theme.Selected != nil {
			current.Theme.Selected = *probe.Theme.Selected
		}
		if probe.Theme.Background != nil {
			current.Theme.Background = *probe.Theme.Background
		}
		if probe.Theme.Foreground != nil {
			current.Theme.Foreground = *probe.Theme.Foreground
		}
		if probe.Theme.AgentStatus != nil {
			if probe.Theme.AgentStatus.Running != "" {
				current.Theme.AgentStatus.Running = probe.Theme.AgentStatus.Running
			}
			if probe.Theme.AgentStatus.Waiting != "" {
				current.Theme.AgentStatus.Waiting = probe.Theme.AgentStatus.Waiting
			}
			if probe.Theme.AgentStatus.Failed != "" {
				current.Theme.AgentStatus.Failed = probe.Theme.AgentStatus.Failed
			}
			if probe.Theme.AgentStatus.Interrupted != "" {
				current.Theme.AgentStatus.Interrupted = probe.Theme.AgentStatus.Interrupted
			}
			if probe.Theme.AgentStatus.Idle != "" {
				current.Theme.AgentStatus.Idle = probe.Theme.AgentStatus.Idle
			}
		}
	}

	return nil
}

func loadConfigEnvironment(target *Config, baseCfg *Config) error {
	strataOpts := []strata.Option{
		strata.WithoutFiles(),
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
