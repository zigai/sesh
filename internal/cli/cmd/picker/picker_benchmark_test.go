//go:build linux || darwin

package cmdpicker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkPickerSessionNameLargeTranscript(b *testing.B) {
	const messageCount = 20000

	message := `{"type":"message","message":{"role":"assistant","content":"` + strings.Repeat("x", 512) + `"}}` + "\n"
	transcript := `{"type":"session_info","name":"Large session"}` + "\n" + strings.Repeat(message, messageCount)

	path := filepath.Join(b.TempDir(), "conversation.jsonl")
	if err := os.WriteFile(path, []byte(transcript), 0o600); err != nil {
		b.Fatal(err)
	}

	b.Run("FirstBoundedChunk", func(b *testing.B) {
		b.SetBytes(pickerNameFileReadLimit)
		b.ReportAllocs()

		for b.Loop() {
			state := readPickerSessionName(b.Context(), path, pickerSessionNameState{}, pickerNameFileReadLimit)
			if !state.hasInfo || state.name != "Large session" || state.offset > pickerNameFileReadLimit || state.offset <= pickerNameFileReadLimit-int64(len(message)) {
				b.Fatalf("first chunk name/offset = %q/%d", state.name, state.offset)
			}
		}
	})
	b.Run("UnchangedTranscript", func(b *testing.B) {
		state := warmBenchmarkTranscript(b, path, int64(len(transcript)))
		b.ReportAllocs()

		for b.Loop() {
			current := readPickerSessionName(b.Context(), path, state, pickerNameFileReadLimit)
			if !current.hasInfo || current.name != "Large session" || current.offset != int64(len(transcript)) {
				b.Fatalf("unchanged transcript name/offset = %q/%d", current.name, current.offset)
			}
		}
	})
}

func warmBenchmarkTranscript(b *testing.B, path string, size int64) pickerSessionNameState {
	b.Helper()

	var state pickerSessionNameState
	for state.offset < size {
		previous := state.offset

		state = readPickerSessionName(b.Context(), path, state, pickerNameFileReadLimit)
		if state.offset <= previous {
			b.Fatal("transcript warmup made no progress")
		}
	}

	return state
}
