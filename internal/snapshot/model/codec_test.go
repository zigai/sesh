package model

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"pgregory.net/rapid"
)

func fixture() Manifest {
	m := Manifest{
		Format: "sesh", Version: Schema, Started: 1, Finished: 2,
		Source:   Source{TmuxVersion: "3.6", LibraryVersion: "v0.3.0", Consistency: "observed"},
		Sessions: []Session{{ID: "s0", Name: Bytes("work"), Directory: Bytes("/tmp"), Group: "g0"}, {ID: "s1", Name: Bytes("peer"), Directory: Bytes("/tmp"), Group: "g0"}},
		Groups:   []Group{{ID: "g0", Name: Bytes("group"), Members: []string{"s0", "s1"}}},
		Windows:  []Window{{ID: "w0", Name: Bytes("editor"), Width: 80, Height: 24, Layout: Layout{Width: 80, Height: 24, Pane: "p0"}}},
		Links:    []Link{{Session: "s0", Window: "w0", Index: 3, Active: true}, {Session: "s1", Window: "w0", Index: 3, Active: true}},
		Panes:    []Pane{{ID: "p0", Window: "w0", Directory: Bytes("/tmp"), Width: 80, Height: 24, Active: true, Launch: Launch{Recovery: "unverified"}}},
	}
	for _, domain := range []string{"S01", "S02", "S03", "S04", "S05", "S06", "S07", "S08", "S09", "S10"} {
		m.Evidence = append(m.Evidence, Evidence{Domain: domain, Status: "unverified", Reason: "not_captured"})
	}

	return m
}

func TestNativeBytesAndRelationships(t *testing.T) {
	m := fixture()
	m.Panes[0].Launch.Arguments = []Bytes{{0xff, '\n', '\t', ' ', '\''}, {}, Bytes(" spaces ")}
	m.Sessions[0].Environment = []Variable{{Name: Bytes("EMPTY"), State: "set"}, {Name: Bytes("MISSING"), State: "removed"}, {Name: Bytes("SECRET"), Value: Bytes("private"), State: "set", Hidden: true}}

	data, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}

	got, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff(m, got, cmpopts.EquateEmpty()); diff != "" {
		t.Fatal(diff)
	}

	if got.Complete() {
		t.Fatal("unverified graph reported complete")
	}

	closure, err := got.Closure([]string{"s0"})
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff([]string{"s0", "s1"}, closure); diff != "" {
		t.Fatal(diff)
	}
}

func TestRejectsMalformedNativeDocuments(t *testing.T) {
	data, err := Encode(fixture())
	if err != nil {
		t.Fatal(err)
	}

	version := []byte(fmt.Sprintf(`"version":%d`, Schema))
	withVersion := func(replacement string) []byte { return bytes.Replace(data, version, []byte(replacement), 1) }

	cases := map[string][]byte{
		"legacy":                 []byte("session\twork\t0\n"),
		"duplicate":              withVersion(string(version) + "," + string(version)),
		"unknown":                withVersion(string(version) + `,"other":0`),
		"missing":                bytes.Replace(data, append(bytes.Clone(version), ','), nil, 1),
		"case":                   bytes.Replace(data, []byte(`"version"`), []byte(`"Version"`), 1),
		"version":                withVersion(fmt.Sprintf(`"version":%d`, Schema-1)),
		"null":                   bytes.Replace(data, []byte(`"last":false`), []byte(`"last":null`), 1),
		"trailing":               append(bytes.Clone(data), []byte(` {}`)...),
		"missing false":          bytes.Replace(data, []byte(`"last":false,`), nil, 1),
		"invalid utf8":           append([]byte{'"', 0xff, '"'}, data...),
		"utf8 as base64":         bytes.Replace(data, []byte(`"work"`), []byte(`{"base64":"d29yaw=="}`), 1),
		"noncanonical base64":    bytes.Replace(data, []byte(`"work"`), []byte(`{"base64":"/x=="}`), 1),
		"newline base64":         bytes.Replace(data, []byte(`"work"`), []byte(`{"base64":"/\nw=="}`), 1),
		"unknown bytes member":   bytes.Replace(data, []byte(`"work"`), []byte(`{"hex":"ff"}`), 1),
		"extra bytes member":     bytes.Replace(data, []byte(`"work"`), []byte(`{"base64":"/w==","other":""}`), 1),
		"bytes as number":        bytes.Replace(data, []byte(`"work"`), []byte(`7`), 1),
		"bytes as array":         bytes.Replace(data, []byte(`"work"`), []byte(`[119]`), 1),
		"empty base64 of object": bytes.Replace(data, []byte(`"work"`), []byte(`{}`), 1),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Decode(input)
			if err == nil {
				t.Fatal("accepted invalid document")
			}

			if name == "version" && !errors.Is(err, ErrVersion) {
				t.Fatalf("wrong version error: %v", err)
			}
		})
	}
}

func TestBytesEncodeReadableTextAndBase64Otherwise(t *testing.T) {
	cases := []struct {
		name  string
		value Bytes
		want  string
	}{
		{name: "text", value: Bytes("work"), want: `"work"`},
		{name: "unicode and controls", value: Bytes("é🚀\t\n\x00"), want: `"é🚀\t\n\u0000"`},
		{name: "empty", value: Bytes{}, want: `""`},
		{name: "invalid utf8", value: Bytes{0xff, 'a'}, want: `{"base64":"/2E="}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			data, err := json.Marshal(testCase.value)
			if err != nil {
				t.Fatal(err)
			}

			if string(data) != testCase.want {
				t.Fatalf("encoded = %s, want %s", data, testCase.want)
			}

			var decoded Bytes
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(decoded, testCase.value) {
				t.Fatalf("decoded = %q, want %q", decoded, testCase.value)
			}
		})
	}
}

func TestGraphValidation(t *testing.T) {
	cases := map[string]func(*Manifest){
		"dangling link":        func(m *Manifest) { m.Links[0].Window = "unknown" },
		"duplicate pane":       func(m *Manifest) { m.Panes = append(m.Panes, m.Panes[0]) },
		"duplicate slot":       func(m *Manifest) { m.Links = append(m.Links, m.Links[0]) },
		"missing selection":    func(m *Manifest) { m.Links[0].Active = false },
		"missing group member": func(m *Manifest) { m.Groups[0].Members[1] = "unknown" },
		"group indexes":        func(m *Manifest) { m.Links[1].Index++ },
		"geometry":             func(m *Manifest) { m.Panes[0].Width-- },
		"missing artifact":     func(m *Manifest) { m.Panes[0].Terminal.Artifact = "a0" },
		"unsafe artifact":      func(m *Manifest) { m.Artifacts = []Artifact{{ID: "../escape"}} },
		"orphan artifact": func(m *Manifest) {
			m.Artifacts = []Artifact{{ID: "a0", Kind: "terminal-text", SHA256: strings.Repeat("0", 64)}}
		},
		"missing evidence":     func(m *Manifest) { m.Evidence = m.Evidence[:1] },
		"no reason":            func(m *Manifest) { m.Evidence[0].Reason = "" },
		"nul argument":         func(m *Manifest) { m.Panes[0].Launch.Arguments = []Bytes{{0}} },
		"relative cwd":         func(m *Manifest) { m.Panes[0].Directory = Bytes("relative") },
		"short endpoint scope": func(m *Manifest) { m.Source.EndpointScope = "abcd" },
		"uppercase endpoint scope": func(m *Manifest) {
			m.Source.EndpointScope = strings.Repeat("A", EndpointScopeLength)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := fixture()
			mutate(&m)

			if err := m.Validate(); err == nil {
				t.Fatal("accepted invalid graph")
			}
		})
	}
}

func TestEndpointScopeIsCanonicalAndRoundTrips(t *testing.T) {
	scope, err := EndpointScope("/tmp/sesh-test.sock")
	if err != nil {
		t.Fatalf("EndpointScope failed: %v", err)
	}

	if len(scope) != EndpointScopeLength || scope != strings.ToLower(scope) {
		t.Fatalf("scope = %q", scope)
	}

	for _, path := range []string{"relative.sock", "/tmp/../tmp/sesh-test.sock"} {
		if _, err := EndpointScope(path); !errors.Is(err, ErrInvalid) {
			t.Fatalf("EndpointScope(%q) error = %v, want ErrInvalid", path, err)
		}
	}

	m := fixture()
	m.Source.EndpointScope = scope

	data, err := Encode(m)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	got, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if got.Source.EndpointScope != scope {
		t.Fatalf("endpoint scope = %q, want %q", got.Source.EndpointScope, scope)
	}
}

func TestSingleMemberGroup(t *testing.T) {
	m := fixture()
	m.Groups[0].Members = []string{"s0"}

	m.Sessions[1].Group = ""
	if err := m.Validate(); err != nil {
		t.Fatal("valid single-member group rejected", err)
	}
}

func TestBytePreservationProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		value := rapid.SliceOfN(rapid.ByteRange(1, 255), 0, 256).Draw(t, "argument")
		m := fixture()
		m.Panes[0].Launch.Arguments = []Bytes{value}

		data, err := Encode(m)
		if err != nil {
			t.Fatal(err)
		}

		got, err := Decode(data)
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(value, got.Panes[0].Launch.Arguments[0]) {
			t.Fatal("argument bytes changed")
		}
	})
}

func FuzzNativeManifest(f *testing.F) {
	data, err := Encode(fixture())
	if err != nil {
		f.Fatal(err)
	}

	f.Add(data)
	f.Add([]byte(`{"format":"sesh","version":1}`))
	f.Add([]byte("pane\tlegacy\n"))
	f.Fuzz(func(t *testing.T, input []byte) {
		m, err := Decode(input)
		if err != nil {
			return
		}

		encoded, err := Encode(m)
		if err != nil {
			t.Fatal(err)
		}

		var raw map[string]any
		if err := json.Unmarshal(encoded, &raw); err != nil {
			t.Fatal(err)
		}

		if !strings.Contains(string(encoded), `"format":"sesh"`) {
			t.Fatal("changed format")
		}
	})
}

func TestMetadataAndLaunchBytes(t *testing.T) {
	for _, value := range []string{"invalid\xff\tbytes", "a\tb\nc\r", "trailing\n", ":leading-colon", "sesh_record_v1\t[]", "two words", "", "'quotes'", "$HOME", "; exit 1", "žiga"} {
		t.Run(value, func(t *testing.T) {
			m := fixture()
			m.Panes[0].Title = Bytes(value)
			m.Panes[0].Directory = Bytes("/tmp/" + value)
			m.Panes[0].Launch = Launch{Executable: Bytes("/bin/tool"), Arguments: []Bytes{Bytes("tool"), Bytes(value), {}}, Directory: Bytes("/tmp/" + value), Environment: []Variable{{Name: Bytes("VALUE"), State: "set", Value: Bytes(value)}}, Recovery: "restart"}
			m.Windows[0].Name = Bytes(value)
			m.Windows[0].Options = []Option{{Scope: "window", Name: Bytes("@text"), Value: Bytes(value), Present: true}}

			data, err := Encode(m)
			if err != nil {
				t.Fatal(err)
			}

			got, err := Decode(data)
			if err != nil {
				t.Fatal(err)
			}

			if diff := cmp.Diff(m, got, cmpopts.EquateEmpty()); diff != "" {
				t.Fatal(diff)
			}

			again, err := Encode(got)
			if err != nil || !bytes.Equal(data, again) {
				t.Fatal("native encoding is not stable", err)
			}
		})
	}
}
