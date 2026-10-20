package logview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/rs/zerolog"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/logging"
)

const (
	logTimeLayout          = "2006-01-02 15:04:05"
	logLevelAlignmentWidth = 5
	DefaultLogLines        = 50
	defaultLogLines        = DefaultLogLines
	MaxLogRecordBytes      = 4 << 20
	maxLogRecordBytes      = MaxLogRecordBytes

	oneSecondMs         = 1000
	oneMinuteMs         = 60_000
	oneHourMs           = 3_600_000
	oneSecondFloat      = 1000.0
	oneMinuteFloat      = 60_000.0
	oneHourFloat        = 3_600_000.0
	hoursPerDay         = 24
	minGraphicQuotedLen = 2
)

var (
	ErrLogRecordTooLong   = errors.New("log record exceeds maximum size")
	errLogRecordTooLong   = ErrLogRecordTooLong
	ErrMalformedLogRecord = errors.New("invalid diagnostic log record")
	errMalformedLogRecord = ErrMalformedLogRecord
)

type Options struct {
	Raw       bool
	Pretty    bool
	Watch     bool
	Color     bool
	Verbose   bool
	Relative  bool
	Lines     int
	Level     zerolog.Level
	HasLevel  bool
	Component string
	Since     time.Time
	HasSince  bool
	Until     time.Time
	HasUntil  bool
	Grep      *regexp.Regexp
}

type Styles struct {
	timestamp lipgloss.Style
	trace     lipgloss.Style
	debug     lipgloss.Style
	info      lipgloss.Style
	warn      lipgloss.Style
	error     lipgloss.Style
	critical  lipgloss.Style
	component lipgloss.Style
	fieldKey  lipgloss.Style
	fieldVal  lipgloss.Style
	dim       lipgloss.Style
	dimCyan   lipgloss.Style
	verbose   bool
	relative  bool
	shortTime bool
	now       time.Time
}

type LogStreamWriter struct {
	output      io.Writer
	diagnostics io.Writer
	options     Options
	styles      Styles
	lineNumber  int
}

func (o Options) toLoggingFilter() logging.FilterOptions {
	return logging.FilterOptions{
		Level:     o.Level,
		HasLevel:  o.HasLevel,
		Component: o.Component,
		Since:     o.Since,
		HasSince:  o.HasSince,
		Until:     o.Until,
		HasUntil:  o.HasUntil,
		Grep:      o.Grep,
	}
}

func DisplayRetainedLogs(ctx context.Context, output, diagnostics io.Writer, path string, opts Options) error {
	readOpts := logging.ReadOptions{
		Lines:  opts.Lines,
		Follow: opts.Watch,
		Filter: opts.toLoggingFilter(),
	}

	if !opts.Watch && opts.Lines > 0 && !opts.Relative && !opts.Raw {
		return displayBoundedRetainedLogs(ctx, output, diagnostics, path, readOpts, opts)
	}

	return StreamRetainedLogs(ctx, output, diagnostics, path, readOpts, opts)
}

func StreamRetainedLogs(ctx context.Context, output, diagnostics io.Writer, path string, readOpts logging.ReadOptions, options Options) error {
	styles := NewLogStyles(options.Color, options)
	writer := NewLogStreamWriter(output, diagnostics, options, styles)

	if err := logging.StreamLogs(ctx, path, readOpts, writer); err != nil {
		return oops.In("tui").With("operation", "stream_logs").Wrap(err)
	}

	return nil
}

func NewLogStreamWriter(output, diagnostics io.Writer, opts Options, styles Styles) *LogStreamWriter {
	return &LogStreamWriter{
		output:      output,
		diagnostics: diagnostics,
		options:     opts,
		styles:      styles,
		lineNumber:  0,
	}
}

func RenderLogLine(line []byte, styles Styles) (string, error) {
	record := map[string]any{}

	trimmed := bytes.TrimSuffix(bytes.TrimSuffix(line, []byte{'\n'}), []byte{'\r'})
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()

	if err := decoder.Decode(&record); err != nil {
		return "", oops.In("tui").With("operation", "decode_json").Wrap(err)
	}

	if record == nil || !json.Valid(trimmed) {
		return "", oops.In("tui").With("operation", "decode_json").Wrap(errMalformedLogRecord)
	}

	timestamp := formatLogTimestamp(logString(record, "time"), styles)

	timestampWidth := 19
	if styles.relative {
		timestampWidth = 10
	} else if styles.shortTime {
		timestampWidth = 8
	}

	padding := max(timestampWidth-lipgloss.Width(timestamp), 0)
	paddedTimestamp := timestamp + strings.Repeat(" ", padding)

	level := strings.ToUpper(safeLogText(logString(record, "level")))
	component := safeLogText(logString(record, "component"))
	message := safeLogText(logString(record, "message"))

	if level == "" {
		level = "INFO"
	}

	if component == "" {
		component = "unknown"
	}

	paddedLevel := padShortLogLevel(level)

	levelStyle := logLevelStyle(styles, level)
	styledLevel := levelStyle.Render(paddedLevel)
	styledComponent := styles.component.Render(component + ":")

	fields := renderLogFields(record, styles)

	sections := []string{
		styles.timestamp.Render(paddedTimestamp),
		styledLevel,
		styledComponent,
		message,
	}

	content := strings.Join(sections, " ")
	if fields == "" {
		return content, nil
	}

	return content + " " + fields, nil
}

func NewLogStyles(color bool, extra ...any) Styles {
	var (
		isVerbose  bool
		isRelative bool
	)

	now := time.Now().UTC()

	for _, arg := range extra {
		switch v := arg.(type) {
		case bool:
			isVerbose = v
		case Options:
			isVerbose = v.Verbose
			isRelative = v.Relative
		case *Options:
			if v != nil {
				isVerbose = v.Verbose
				isRelative = v.Relative
			}
		}
	}

	if !color {
		return Styles{
			timestamp: lipgloss.Style{},
			trace:     lipgloss.Style{},
			debug:     lipgloss.Style{},
			info:      lipgloss.Style{},
			warn:      lipgloss.Style{},
			error:     lipgloss.Style{},
			critical:  lipgloss.Style{},
			component: lipgloss.Style{},
			fieldKey:  lipgloss.Style{},
			fieldVal:  lipgloss.Style{},
			dim:       lipgloss.Style{},
			dimCyan:   lipgloss.Style{},
			verbose:   isVerbose,
			relative:  isRelative,
			shortTime: false,
			now:       now,
		}
	}

	return Styles{
		timestamp: lipgloss.NewStyle().Foreground(lipgloss.Color("243")),
		trace:     lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Bold(true),
		debug:     lipgloss.NewStyle().Foreground(lipgloss.Color("63")).Bold(true),
		info:      lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Bold(true),
		warn:      lipgloss.NewStyle().Foreground(lipgloss.Color("192")).Bold(true),
		error:     lipgloss.NewStyle().Foreground(lipgloss.Color("204")).Bold(true),
		critical:  lipgloss.NewStyle().Foreground(lipgloss.Color("134")).Bold(true),
		component: lipgloss.NewStyle().Bold(true).Faint(true),
		fieldKey:  lipgloss.NewStyle().Faint(true),
		fieldVal:  lipgloss.NewStyle(),
		dim:       lipgloss.NewStyle().Faint(true),
		dimCyan:   lipgloss.NewStyle().Foreground(lipgloss.Color("37")),
		verbose:   isVerbose,
		relative:  isRelative,
		shortTime: false,
		now:       now,
	}
}

func displayBoundedRetainedLogs(ctx context.Context, output, diagnostics io.Writer, path string, readOpts logging.ReadOptions, options Options) error {
	var buf bytes.Buffer
	if err := logging.StreamLogs(ctx, path, readOpts, &buf); err != nil {
		return oops.In("tui").With("operation", "read_logs").Wrap(err)
	}

	records := splitLogRecords(buf.Bytes())

	styles := NewLogStyles(options.Color, options)
	if !styles.relative && shouldElideSameDayDate(records, styles.now) {
		styles.shortTime = true
	}

	for i, line := range records {
		if err := ctx.Err(); err != nil {
			return oops.In("tui").With("operation", "read_logs").Wrap(err)
		}

		if err := writeLogLine(output, diagnostics, line, options.Raw, options.Pretty, styles, i+1); err != nil {
			return err
		}
	}

	return nil
}

func splitLogRecords(raw []byte) [][]byte {
	if len(raw) == 0 {
		return nil
	}

	var records [][]byte

	for len(raw) > 0 {
		end := bytes.IndexByte(raw, '\n')
		if end < 0 {
			break
		}

		records = append(records, raw[:end+1])
		raw = raw[end+1:]
	}

	return records
}

func (w *LogStreamWriter) Write(p []byte) (int, error) {
	if len(p) > maxLogRecordBytes {
		return 0, oops.In("tui").With("operation", "stream_logs").Wrap(errLogRecordTooLong)
	}

	w.lineNumber++
	if err := writeLogLine(w.output, w.diagnostics, p, w.options.Raw, w.options.Pretty, w.styles, w.lineNumber); err != nil {
		return 0, err
	}

	return len(p), nil
}

func writeLogLine(output, diagnostics io.Writer, line []byte, raw bool, pretty bool, styles Styles, lineNumber int) error {
	if raw {
		return writeRawLogLine(output, line, pretty)
	}

	rendered, err := RenderLogLine(line, styles)
	if err != nil {
		return writeSkippedLogRecord(diagnostics, lineNumber, "malformed")
	}

	return writeLogOutput(output, []byte(rendered+"\n"))
}

func writeRawLogLine(output io.Writer, line []byte, pretty bool) error {
	if !pretty {
		return writeLogOutput(output, line)
	}

	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return writeLogOutput(output, line)
	}

	var indented bytes.Buffer
	if err := json.Indent(&indented, trimmed, "", "  "); err != nil {
		return writeLogOutput(output, line)
	}

	return writeLogOutput(output, append(indented.Bytes(), '\n'))
}

func writeSkippedLogRecord(output io.Writer, lineNumber int, reason string) error {
	notice := fmt.Sprintf("warning: omitted %s diagnostic log record %d\n", reason, lineNumber)

	return writeLogOutput(output, []byte(notice))
}

func writeLogOutput(output io.Writer, data []byte) error {
	written, err := output.Write(data)
	if err != nil {
		return oops.In("tui").With("operation", "write_log_output").Wrap(err)
	}

	if written != len(data) {
		return oops.In("tui").With("operation", "write_log_output").Wrap(io.ErrShortWrite)
	}

	return nil
}

func logLevelStyle(styles Styles, level string) lipgloss.Style {
	switch level {
	case "TRACE":
		return styles.trace
	case "DEBUG":
		return styles.debug
	case "WARN", "WARNING":
		return styles.warn
	case "ERROR":
		return styles.error
	case "FATAL", "PANIC":
		return styles.critical
	default:
		return styles.info
	}
}

func padShortLogLevel(level string) string {
	padding := max(logLevelAlignmentWidth-lipgloss.Width(level), 0)

	return level + strings.Repeat(" ", padding)
}

func humanizeDuration(ms int64) string {
	if ms <= 0 {
		return "0ms"
	}

	if ms < oneSecondMs {
		return fmt.Sprintf("%dms", ms)
	}

	if ms < oneMinuteMs {
		seconds := float64(ms) / oneSecondFloat

		return fmt.Sprintf("%.1fs", seconds)
	}

	if ms < oneHourMs {
		minutes := float64(ms) / oneMinuteFloat

		return fmt.Sprintf("%.1fm", minutes)
	}

	hours := float64(ms) / oneHourFloat

	return fmt.Sprintf("%.1fh", hours)
}

func shouldPruneField(key string, value any, record map[string]any, verbose bool) bool {
	if verbose {
		return false
	}

	return key == "socket" ||
		isRedundantCommandField(key, value, record) ||
		isDefaultExitCodeField(key, value, record) ||
		isDefaultNoiseBooleanField(key, value) ||
		isDefaultNoiseCountField(key, value)
}

func isRedundantCommandField(key string, value any, record map[string]any) bool {
	if key != "command" {
		return false
	}

	comp, _ := record["component"].(string)
	cmdVal, _ := value.(string)

	if comp == "command" || comp == "" || cmdVal == "" {
		return false
	}

	normComp := strings.ReplaceAll(comp, "_", ".")
	normCmd := strings.ReplaceAll(cmdVal, "_", ".")

	return normCmd == normComp || strings.HasPrefix(normCmd, normComp+".")
}

func isDefaultExitCodeField(key string, value any, record map[string]any) bool {
	if key != "exit_code" {
		return false
	}

	status, _ := record["status"].(string)

	return (status == "ok" || status == "started" || status == "") && isZeroNumber(value)
}

func isDefaultNoiseBooleanField(key string, value any) bool {
	b, ok := value.(bool)
	if !ok || b {
		return false
	}

	switch key {
	case "duplicate", "partial", "restore_partial", "boot_reconciled",
		"default_session_changed", "output_dir_explicit",
		"cross_session", "cross_window":
		return true
	default:
		return false
	}
}

func isDefaultNoiseCountField(key string, value any) bool {
	if !isZeroNumber(value) {
		return false
	}

	switch key {
	case "skipped_pane_content_count", "warning_count", "error_count":
		return true
	default:
		return false
	}
}

func isZeroNumber(value any) bool {
	switch v := value.(type) {
	case int:
		return v == 0
	case int64:
		return v == 0
	case float64:
		return v == 0
	case json.Number:
		n, err := v.Int64()

		return err == nil && n == 0
	default:
		return false
	}
}

func toInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		return int64(v), true
	case json.Number:
		n, err := v.Int64()
		if err == nil {
			return n, true
		}
	}

	return 0, false
}

func formatLogTimestamp(value string, styles Styles) string {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, value)
		if err != nil {
			return safeLogText(value)
		}
	}

	if styles.relative {
		now := styles.now
		if now.IsZero() {
			now = time.Now().UTC()
		}

		return formatRelativeTime(parsed, now)
	}

	if styles.shortTime {
		return parsed.UTC().Format("15:04:05")
	}

	return parsed.UTC().Format(logTimeLayout)
}

func formatRelativeTime(t time.Time, now time.Time) string {
	diff := max(now.Sub(t), 0)

	switch {
	case diff < 5*time.Second:
		return "just now"
	case diff < time.Minute:
		return fmt.Sprintf("%ds ago", int(diff.Seconds()))
	case diff < time.Hour:
		return fmt.Sprintf("%dm ago", int(diff.Minutes()))
	case diff < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(diff.Hours()))
	case diff < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(diff.Hours()/hoursPerDay))
	default:
		return t.UTC().Format("2006-01-02")
	}
}

func renderLogFields(record map[string]any, styles Styles) string {
	keys := orderedLogFieldKeys(record)

	fields := make([]string, 0, len(keys))
	eq := styles.dim.Render("=")

	for _, key := range keys {
		val := record[key]
		if shouldPruneField(key, val, record, styles.verbose) {
			continue
		}

		displayKey := safeLogText(key)

		var displayVal string

		if key == "duration_ms" {
			if ms, ok := toInt64(val); ok {
				displayKey = "duration"
				displayVal = humanizeDuration(ms)
			}
		}

		if displayVal == "" {
			renderedVal, err := renderLogFieldValue(val)
			if err != nil {
				continue
			}

			displayVal = renderedVal
		}

		valStyle := styles.fieldVal

		if key == "status" {
			switch displayVal {
			case "degraded", "partial":
				valStyle = styles.warn
			case "failed":
				valStyle = styles.error
			case "skipped":
				valStyle = styles.dimCyan
			case "recovered":
				valStyle = styles.info
			}
		}

		fields = append(fields, styles.fieldKey.Render(displayKey)+eq+valStyle.Render(displayVal))
	}

	return strings.Join(fields, " ")
}

func orderedLogFieldKeys(record map[string]any) []string {
	priority := []string{"command", "operation", "status", "reason", "phase", "error_code"}
	keys := make([]string, 0, len(record))

	seen := make(map[string]struct{}, len(priority)+1)
	for _, key := range priority {
		if _, ok := record[key]; ok {
			keys = append(keys, key)
			seen[key] = struct{}{}
		}
	}

	rest := make([]string, 0, len(record))

	var hasDuration, hasError bool

	for key := range record {
		if isLogDisplayBaseField(key) {
			continue
		}

		if key == "duration_ms" {
			hasDuration = true

			continue
		}

		if key == "error" {
			hasError = true

			continue
		}

		if _, ok := seen[key]; ok {
			continue
		}

		rest = append(rest, key)
	}

	sort.Strings(rest)

	keys = append(keys, rest...)
	if hasDuration {
		keys = append(keys, "duration_ms")
	}

	if hasError {
		keys = append(keys, "error")
	}

	return keys
}

func isLogDisplayBaseField(key string) bool {
	switch key {
	case "time", "level", "message", "component", "version", "commit", "pid":
		return true
	default:
		return false
	}
}

func renderLogFieldValue(value any) (string, error) {
	if text, ok := value.(string); ok {
		safe := safeLogText(text)
		if safe == "" || strings.ContainsAny(safe, " \t\r\n\"") {
			return strconv.Quote(safe), nil
		}

		return safe, nil
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return "", oops.In("tui").With("operation", "encode_field_value").Wrap(err)
	}

	return string(encoded), nil
}

func logString(record map[string]any, key string) string {
	value, _ := record[key].(string)

	return value
}

func safeLogText(value string) string {
	quoted := strconv.QuoteToGraphic(value)
	if len(quoted) < minGraphicQuotedLen {
		return ""
	}

	return quoted[1 : len(quoted)-1]
}

func shouldElideSameDayDate(records [][]byte, now time.Time) bool {
	if len(records) == 0 {
		return false
	}

	if now.IsZero() {
		now = time.Now().UTC()
	}

	nowY, nowM, nowD := now.Date()
	foundAny := false

	for _, rec := range records {
		matches, ok := recordMatchesDate(rec, nowY, nowM, nowD)
		if !ok {
			continue
		}

		if !matches {
			return false
		}

		foundAny = true
	}

	return foundAny
}

func recordMatchesDate(line []byte, y int, m time.Month, d int) (bool, bool) {
	if len(line) == 0 {
		return false, false
	}

	record := map[string]any{}

	trimmed := bytes.TrimSuffix(bytes.TrimSuffix(line, []byte{'\n'}), []byte{'\r'})
	if err := json.Unmarshal(trimmed, &record); err != nil {
		return false, false
	}

	timeStr, _ := record["time"].(string)
	if timeStr == "" {
		return false, false
	}

	t, err := parseRecordTime(timeStr)
	if err != nil {
		return false, false
	}

	ry, rm, rd := t.UTC().Date()

	return ry == y && rm == m && rd == d, true
}

func parseRecordTime(value string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t, nil
	}

	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing record time %q: %w", value, err)
	}

	return t, nil
}
