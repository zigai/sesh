package tmuxserver

import (
	"strings"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"
)

func TestLayoutRejectsMalformedValues(t *testing.T) {
	for _, body := range []string{
		"", "80x24,0,0,", "80x24,0,0,999", "80x24,0,0,0 trailing",
		"80x24,0,0{40x24,0,0,0", "80x24,0,0[80x12,0,0,0}",
		"999999999999999999999999999999999x24,0,0,0",
	} {
		value := gotmux.Layout(layoutChecksum(body) + "," + body)
		if _, err := DecodeLayout(value, map[string]string{"%0": "p0"}); err == nil {
			t.Fatalf("accepted malformed layout %q", body)
		}
	}

	if _, err := DecodeLayout("0000,80x24,0,0,0", map[string]string{"%0": "p0"}); err == nil {
		t.Fatal("accepted invalid checksum")
	}
}

func FuzzLayout(f *testing.F) {
	for _, body := range []string{"80x24,0,0,0", "80x24,0,0{40x24,0,0,0,39x24,41,0,1}"} {
		f.Add(layoutChecksum(body) + "," + body)
	}

	f.Fuzz(func(t *testing.T, value string) {
		layout, err := DecodeLayout(gotmux.Layout(value), map[string]string{"%0": "p0", "%1": "p1"})
		if err != nil {
			return
		}

		encoded, err := EncodeLayout(layout.Tiled, map[string]gotmux.PaneID{"p0": "%0", "p1": "%1"})
		if err != nil {
			t.Fatal(err)
		}

		if _, _, valid := strings.Cut(string(encoded), ","); !valid {
			t.Fatal("missing checksum separator")
		}

		if _, err := DecodeLayout(encoded, map[string]string{"%0": "p0", "%1": "p1"}); err != nil {
			t.Fatal(err)
		}
	})
}
