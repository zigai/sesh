package cmdlogs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type logReviewWriter func([]byte) (int, error)

func (w logReviewWriter) Write(p []byte) (int, error) { return w(p) }

func TestLogFormatterPreservesExactNumbers(t *testing.T) {
	line := []byte(`{"level":"info","message":"observed","sequence":9007199254740993,"duration_ms":4983}`)

	text, err := renderLogLine(line, newLogStyles(false))
	if err != nil || !strings.Contains(text, "sequence=9007199254740993") || !strings.Contains(text, "duration=5.0s") {
		t.Fatalf("numbers changed in rendering: %q, %v", text, err)
	}
}

func TestLogReadPreservesJoinedCancellationFailure(t *testing.T) {
	const validRecord = `{"time":"2026-08-06T15:46:01Z","level":"info","command":"continuum.tick","version":"dev","commit":"abc123","pid":43,"component":"resurrect","operation":"save","status":"ok","duplicate":false,"partial":false,"duration_ms":4983,"message":"resurrect save completed"}`

	path := filepath.Join(t.TempDir(), "retained.log")
	if err := os.WriteFile(path, []byte(validRecord+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cause := errors.Join(context.Canceled, io.ErrUnexpectedEOF)
	output := logReviewWriter(func([]byte) (int, error) { return 0, cause })

	err := displayRetainedLogs(t.Context(), output, io.Discard, path, logViewOptions{Raw: true})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("joined output failure was discarded: %v", err)
	}
}

func TestLogRecordWriterRejectsOversize(t *testing.T) {
	writer := newLogStreamWriter(io.Discard, io.Discard, logViewOptions{Raw: true}, newLogStyles(false))
	data := bytes.Repeat([]byte{'x'}, maxLogRecordBytes+1)

	data[len(data)-1] = '\n'
	if _, err := writer.Write(data); !errors.Is(err, errLogRecordTooLong) {
		t.Fatalf("oversized complete record accepted: %v", err)
	}
}

func TestParseTimeFilterTimezoneConsistency(t *testing.T) {
	tokyo := time.FixedZone("JST", 9*3600)
	now := time.Date(2026, 9, 10, 1, 0, 0, 0, tokyo)

	filter, err := parseTimeFilter("15:30", now)
	if err != nil {
		t.Fatal(err)
	}

	expected := time.Date(2026, 9, 9, 15, 30, 0, 0, time.UTC)
	if !filter.Equal(expected) {
		t.Fatalf("parseTimeFilter(15:30) = %s, want %s", filter, expected)
	}

	if filter.After(now) {
		t.Fatalf("parseTimeFilter(15:30) is in future: %s > %s", filter, now)
	}
}
