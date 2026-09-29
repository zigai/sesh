package session_test

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"testing"

	"github.com/zigai/sesh/internal/session"
)

func TestRawBytesKeepStrictBase64JSONContract(t *testing.T) {
	t.Parallel()

	for _, value := range []session.RawBytes{session.RawBytes("sesh"), {0xff, 'a'}, {}} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}

		var decoded session.RawBytes
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("decode %s: %v", data, err)
		}

		if !bytes.Equal(decoded, value) {
			t.Fatalf("decoded %s = %q, want %q", data, decoded, value)
		}
	}

	if data, _ := json.Marshal(session.RawBytes("sesh")); string(data) != `"c2VzaA=="` {
		t.Fatalf("encoded = %s, want base64 %q", data, `"c2VzaA=="`)
	}

	var decoded session.RawBytes
	for _, invalid := range []string{`"not base64!"`, `"c2VzaA"`, `{"base64":"c2VzaA=="}`} {
		if err := json.Unmarshal([]byte(invalid), &decoded); !errors.Is(err, session.ErrInvalidRawBytes) {
			t.Fatalf("decode %s error = %v, want invalid raw bytes", invalid, err)
		}
	}
}
