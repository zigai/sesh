package logs

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/logging"
	"github.com/zigai/sesh/internal/tui/logview"
)

const (
	annotationStreaming = clibase.AnnotationStreaming
	defaultLogLines     = logview.DefaultLogLines
	hoursPerDay         = 24
)

var (
	errInvalidFilterLogLevel = errors.New("invalid log level: expected trace, debug, info, warn, or error")
	errInvalidTimeFilter     = errors.New("invalid time filter: expected duration (e.g. 15m, 1h, 2d), 'today', or date/time")
	errInvalidLinesCount     = errors.New("invalid line count: expected an integer from 0 to 100000")
	errInvalidColorChoice    = errors.New("invalid color choice: expected auto, always, or never")
	errTimeOrderInvalid      = errors.New("invalid time filter: --since must not be after --until")
	errPathAndClearExclusive = errors.New("--path and --clear are mutually exclusive")
	errPathIncompatible      = errors.New("--path cannot be combined with log viewing flags")
	errClearIncompatible     = errors.New("--clear cannot be combined with log viewing flags")
	errAllAndLinesExclusive  = errors.New("--all and --lines cannot be used together")
	errInvalidGrepPattern    = errors.New("invalid grep pattern: regular expression compilation failed")
)

func NewCommand() *cobra.Command {
	return newLogsCommand()
}

func newLogsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Display diagnostic logs",
		Long: `Display diagnostic logs from the native persistent log file.

By default, the 50 most recent records are displayed in human-readable format.
When --json is specified, log bytes are emitted unchanged, normally as JSON Lines (unlike
finite snapshot commands which emit a JSON envelope). Pure --path and --clear
operations emit a defined finite JSON envelope when combined with --json.
Raw output preserves malformed stored lines for inspection. --pretty formats
JSON for humans and takes precedence over --json.`,
		Args: clibase.UsageArgs(cobra.NoArgs),
		Annotations: map[string]string{
			clibase.AnnotationStreaming: "true",
		},
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			return ValidateCommand(cmd)
		},
		RunE: runLogs,
	}

	cmd.Flags().BoolP("watch", "w", false, "Continue displaying new log records")
	cmd.Flags().Bool("json", false, "Display the raw JSON Lines stream (or finite JSON envelope for --path/--clear)")
	cmd.Flags().BoolP("pretty", "p", false, "Indent JSON output for human readability")
	cmd.Flags().BoolP("verbose", "v", false, "Display all structured fields without compact pruning")
	cmd.Flags().IntP("lines", "n", defaultLogLines, "Number of most recent log lines to display (0 for all, maximum 100000)")
	cmd.Flags().BoolP("all", "a", false, "Display all historical log lines without limit")
	cmd.Flags().StringP("level", "l", "", "Minimum log level to display")
	clibase.SetFlagChoices(cmd, "level", "trace", "debug", "info", "warn", "error")
	cmd.Flags().StringP("component", "c", "", "Filter logs by component (e.g. cli, capture, engine, store, hooks, logging)")
	cmd.Flags().Bool("path", false, "Print the path to the diagnostic log file and exit")
	cmd.Flags().Bool("clear", false, "Safely truncate the active log file and remove rotated backups")
	cmd.Flags().String("color", "auto", "Colorize output")
	clibase.SetFlagChoices(cmd, "color", "auto", "always", "never")
	cmd.Flags().String("since", "", "Display log records newer than relative duration (e.g. 15m, 2h, 1d) or timestamp")
	cmd.Flags().String("until", "", "Display log records older than relative duration or timestamp")
	cmd.Flags().StringP("grep", "g", "", "Filter log records matching regular expression")
	cmd.Flags().BoolP("relative", "r", false, "Display relative timestamps (e.g. 2m ago, just now)")

	return cmd
}

func ValidateCommand(cmd *cobra.Command) error {
	return validateLogsCommand(cmd)
}

func ValidateLogsCommand(cmd *cobra.Command) error {
	return validateLogsCommand(cmd)
}

func validateLogsCommand(cmd *cobra.Command) error {
	if err := validateExclusiveFlags(cmd); err != nil {
		return err
	}

	return validateOptionBounds(cmd)
}

func validateExclusiveFlags(cmd *cobra.Command) error {
	pathFlag := getBoolFlag(cmd, "path")
	clearFlag := getBoolFlag(cmd, "clear")

	if pathFlag && clearFlag {
		return clibase.NewExitError(clibase.ExitCodeUsage, errPathAndClearExclusive)
	}

	if pathFlag && hasAnyViewingFlag(cmd) {
		return clibase.NewExitError(clibase.ExitCodeUsage, errPathIncompatible)
	}

	if clearFlag && hasAnyViewingFlag(cmd) {
		return clibase.NewExitError(clibase.ExitCodeUsage, errClearIncompatible)
	}

	if getBoolFlag(cmd, "all") && cmd.Flags().Changed("lines") {
		return clibase.NewExitError(clibase.ExitCodeUsage, errAllAndLinesExclusive)
	}

	return nil
}

func hasAnyViewingFlag(cmd *cobra.Command) bool {
	return getBoolFlag(cmd, "watch") ||
		getBoolFlag(cmd, "pretty") ||
		getBoolFlag(cmd, "verbose") ||
		cmd.Flags().Changed("lines") ||
		getBoolFlag(cmd, "all") ||
		getStringFlag(cmd, "level") != "" ||
		getStringFlag(cmd, "component") != "" ||
		cmd.Flags().Changed("color") ||
		getStringFlag(cmd, "since") != "" ||
		getStringFlag(cmd, "until") != "" ||
		getStringFlag(cmd, "grep") != "" ||
		getBoolFlag(cmd, "relative")
}

func validateOptionBounds(cmd *cobra.Command) error {
	var dummy logview.Options
	if err := parseLogsLinesOptions(cmd, &dummy); err != nil {
		return clibase.NewExitError(clibase.ExitCodeUsage, err)
	}

	if err := parseLogsFilterOptions(cmd, &dummy); err != nil {
		return clibase.NewExitError(clibase.ExitCodeUsage, err)
	}

	if err := parseLogsTimeOptions(cmd, &dummy); err != nil {
		return err
	}

	if _, err := parseLogsColorOption(cmd); err != nil {
		return err
	}

	return nil
}

func parseFilterLogLevel(value string) (zerolog.Level, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "warning" {
		normalized = "warn"
	}

	level, err := zerolog.ParseLevel(normalized)
	if err != nil || level > zerolog.ErrorLevel || level < zerolog.TraceLevel {
		return zerolog.NoLevel, errInvalidFilterLogLevel
	}

	return level, nil
}

func parseLogsOptions(cmd *cobra.Command) (logview.Options, error) {
	colorEnabled, err := parseLogsColorOption(cmd)
	if err != nil {
		return logview.Options{}, err
	}

	opts := logview.Options{
		Raw:       getBoolFlag(cmd, "json") || getBoolFlag(cmd, "pretty"),
		Pretty:    getBoolFlag(cmd, "pretty"),
		Watch:     getBoolFlag(cmd, "watch"),
		Color:     colorEnabled,
		Verbose:   getBoolFlag(cmd, "verbose"),
		Relative:  getBoolFlag(cmd, "relative"),
		Lines:     defaultLogLines,
		Level:     zerolog.NoLevel,
		HasLevel:  false,
		Component: strings.ToLower(strings.TrimSpace(getStringFlag(cmd, "component"))),
		Since:     time.Time{},
		HasSince:  false,
		Until:     time.Time{},
		HasUntil:  false,
		Grep:      nil,
	}

	if err := parseLogsLinesOptions(cmd, &opts); err != nil {
		return logview.Options{}, clibase.NewExitError(clibase.ExitCodeUsage, err)
	}

	if err := parseLogsFilterOptions(cmd, &opts); err != nil {
		return logview.Options{}, clibase.NewExitError(clibase.ExitCodeUsage, err)
	}

	if err := parseLogsTimeOptions(cmd, &opts); err != nil {
		return logview.Options{}, err
	}

	return opts, nil
}

func parseLogsLinesOptions(cmd *cobra.Command, opts *logview.Options) error {
	switch {
	case getBoolFlag(cmd, "all"):
		opts.Lines = 0
	case cmd.Flags().Changed("lines"):
		lines, err := cmd.Flags().GetInt("lines")
		if err != nil || lines < 0 || lines > logging.MaxRetainedLogRecords {
			return errInvalidLinesCount
		}

		opts.Lines = lines
	}

	return nil
}

func parseLogsFilterOptions(cmd *cobra.Command, opts *logview.Options) error {
	rawLevel := getStringFlag(cmd, "level")
	if rawLevel != "" {
		level, err := parseFilterLogLevel(rawLevel)
		if err != nil {
			return err
		}

		opts.Level = level
		opts.HasLevel = true
	}

	rawGrep := getStringFlag(cmd, "grep")
	if rawGrep != "" {
		rx, err := regexp.Compile(rawGrep)
		if err != nil {
			return errInvalidGrepPattern
		}

		opts.Grep = rx
	}

	return nil
}

func parseLogsTimeOptions(cmd *cobra.Command, opts *logview.Options) error {
	now := time.Now()

	rawSince := getStringFlag(cmd, "since")
	if rawSince != "" {
		since, err := parseTimeFilter(rawSince, now)
		if err != nil {
			return clibase.NewExitError(clibase.ExitCodeUsage, err)
		}

		opts.Since = since
		opts.HasSince = true
	}

	rawUntil := getStringFlag(cmd, "until")
	if rawUntil != "" {
		until, err := parseTimeFilter(rawUntil, now)
		if err != nil {
			return clibase.NewExitError(clibase.ExitCodeUsage, err)
		}

		opts.Until = until
		opts.HasUntil = true
	}

	if opts.HasSince && opts.HasUntil && opts.Since.After(opts.Until) {
		return clibase.NewExitError(clibase.ExitCodeUsage, errTimeOrderInvalid)
	}

	return nil
}

func parseLogsColorOption(cmd *cobra.Command) (bool, error) {
	colorChoice := strings.ToLower(strings.TrimSpace(getStringFlag(cmd, "color")))
	if colorChoice == "" {
		colorChoice = "auto"
	}

	switch colorChoice {
	case "always", "yes", "true", "1", "force":
		return true, nil
	case "never", "no", "false", "0":
		return false, nil
	case "auto":
	default:
		return false, clibase.NewExitError(clibase.ExitCodeUsage, errInvalidColorChoice)
	}

	return clibase.ColorOutput(cmd), nil
}

func runLogs(cmd *cobra.Command, _ []string) error {
	if err := cmd.Context().Err(); err != nil {
		return oops.In("cli").With("operation", "read_logs").Wrap(err)
	}

	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return err
	}

	path, err := config.GetLogFile(cfg)
	if err != nil {
		return oops.In("cli").With("operation", "resolve_log_file").Wrap(err)
	}

	if getBoolFlag(cmd, "path") {
		if clibase.JSONOutputEnabled(cmd) {
			return clibase.WriteJSONResult(cmd, map[string]any{"path": path})
		}

		return clibase.WriteLine(cmd, path)
	}

	if getBoolFlag(cmd, "clear") {
		if err := logging.Truncate(cmd.Context(), path); err != nil {
			return oops.In("cli").Code("logging_failed").With("operation", "truncate_logs").Wrap(err)
		}

		if clibase.JSONOutputEnabled(cmd) {
			return clibase.WriteJSONResult(cmd, map[string]any{"cleared": true, "path": path})
		}

		return clibase.WriteLine(cmd, "Cleared diagnostic log")
	}

	opts, err := parseLogsOptions(cmd)
	if err != nil {
		return err
	}

	if err := logview.DisplayRetainedLogs(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), path, opts); err != nil {
		return oops.In("cli").With("operation", "display_logs").Wrap(err)
	}

	return nil
}

func parseHumanDuration(value string) (time.Duration, error) {
	if strings.HasPrefix(value, "-") {
		return 0, errInvalidTimeFilter
	}

	if before, ok := strings.CutSuffix(value, "d"); ok {
		daysStr := before

		days, err := strconv.Atoi(daysStr)
		if err != nil || days < 0 || days > 100000 {
			return 0, errInvalidTimeFilter
		}

		return time.Duration(days) * 24 * time.Hour, nil
	}

	d, err := time.ParseDuration(value)
	if err != nil || d < 0 {
		return 0, errInvalidTimeFilter
	}

	return d, nil
}

func parseTimeFilter(value string, now time.Time) (time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, nil
	}

	if strings.HasPrefix(trimmed, "-") {
		return time.Time{}, errInvalidTimeFilter
	}

	utcNow := now.UTC()
	if strings.EqualFold(trimmed, "today") {
		return time.Date(utcNow.Year(), utcNow.Month(), utcNow.Day(), 0, 0, 0, 0, time.UTC), nil
	}

	if strings.EqualFold(trimmed, "yesterday") {
		yesterday := utcNow.AddDate(0, 0, -1)
		return time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 0, 0, 0, 0, time.UTC), nil
	}

	if duration, err := parseHumanDuration(trimmed); err == nil {
		return now.Add(-duration), nil
	}

	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
		"15:04:05",
		"15:04",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			if layout == "15:04:05" || layout == "15:04" {
				return time.Date(utcNow.Year(), utcNow.Month(), utcNow.Day(), parsed.Hour(), parsed.Minute(), parsed.Second(), 0, time.UTC), nil
			}

			return parsed.UTC(), nil
		}
	}

	return time.Time{}, errInvalidTimeFilter
}

func getBoolFlag(cmd *cobra.Command, name string) bool {
	if cmd == nil || cmd.Flags().Lookup(name) == nil {
		return false
	}

	val, err := cmd.Flags().GetBool(name)

	return err == nil && val
}

func getStringFlag(cmd *cobra.Command, name string) string {
	if cmd == nil || cmd.Flags().Lookup(name) == nil {
		return ""
	}

	val, _ := cmd.Flags().GetString(name)

	return val
}
