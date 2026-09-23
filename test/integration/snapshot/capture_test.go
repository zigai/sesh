//go:build integration

package snapshot_test

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	"github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/snapshot/capture"
	"github.com/zigai/sesh/internal/snapshot/model"
)

func TestCaptureNativeObservation(t *testing.T) {
	server := tmuxtest.NewServer(t)

	session, err := server.NewSession(t.Context(), tmux.NewSessionOptions{Name: "observed", Program: tmux.Exec("/bin/sh", "-c", "printf 'OBSERVATION_READY'; exec sleep 300"), Size: tmux.Size{Width: 100, Height: 35}, Start: tmux.ExistingOnly})
	if err != nil {
		t.Fatal(err)
	}

	windows, _, err := session.WindowInfos(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	pane, err := windows[0].Handle().ActivePane(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	waitForCapture(t, pane, []byte("OBSERVATION_READY"))
	setCaptureSettings(t, session)

	result, err := capture.Observe(t.Context(), server, []tmux.SessionID{session.ID()})
	if err != nil {
		t.Fatal(err)
	}

	assertNativeObservation(t, result)

	wantScope, err := model.EndpointScope(server.Endpoint().SocketPath)
	if err != nil {
		t.Fatal(err)
	}

	if result.Manifest.Source.EndpointScope != wantScope {
		t.Fatalf("endpoint scope = %q, want %q", result.Manifest.Source.EndpointScope, wantScope)
	}

	if _, err := capture.Observe(t.Context(), server, nil); !errors.Is(err, capture.ErrSelection) {
		t.Fatal("accepted ambient selection")
	}
}

func setCaptureSettings(t *testing.T, session tmux.Session) {
	t.Helper()

	for name, value := range map[string]string{"SESH_MULTILINE": "first\nsecond\t \xff", "SESH_EMPTY": ""} {
		if err := session.Environment().Set(t.Context(), name, value); err != nil {
			t.Fatal(err)
		}
	}

	if err := session.Environment().Remove(t.Context(), "SESH_REMOVED"); err != nil {
		t.Fatal(err)
	}

	if err := session.Environment().SetHidden(t.Context(), "SESH_HIDDEN", "hidden-value"); err != nil {
		t.Fatal(err)
	}

	if err := session.Options().SetUser(t.Context(), "@native", "first\nsecond\t \xff"); err != nil {
		t.Fatal(err)
	}
}

func assertCaptureSettings(t *testing.T, session model.Session) {
	t.Helper()

	for _, want := range []model.Variable{
		{Name: model.Bytes("SESH_MULTILINE"), Value: model.Bytes("first\nsecond\t \xff"), State: "set"},
		{Name: model.Bytes("SESH_EMPTY"), Value: model.Bytes(""), State: "set"},
		{Name: model.Bytes("SESH_REMOVED"), State: "removed"},
		{Name: model.Bytes("SESH_HIDDEN"), Value: model.Bytes("hidden-value"), State: "set", Hidden: true},
	} {
		index := slices.IndexFunc(session.Environment, func(v model.Variable) bool { return bytes.Equal(v.Name, want.Name) })
		if index < 0 {
			t.Fatalf("missing fixture variable %s", want.Name)
		}

		got := session.Environment[index]
		if !bytes.Equal(got.Value, want.Value) || got.State != want.State || got.Hidden != want.Hidden {
			t.Fatalf("variable mismatch: got %#v want %#v", got, want)
		}
	}

	index := slices.IndexFunc(session.Options, func(o model.Option) bool { return string(o.Name) == "@native" })
	if index < 0 || string(session.Options[index].Value) != "first\nsecond\t \xff" {
		t.Fatal("lost option bytes")
	}
}

func assertNativeObservation(t *testing.T, result capture.Observation) {
	t.Helper()

	if result.Manifest.Complete() || result.Manifest.Source.Consistency != "observed" {
		t.Fatal("observation claimed checkpoint parity")
	}

	if len(result.Manifest.Sessions) != 1 || len(result.Manifest.Windows) != 1 || len(result.Manifest.Panes) != 1 {
		t.Fatal("capture included unrelated fixture session")
	}

	assertCaptureSettings(t, result.Manifest.Sessions[0])

	if len(result.Artifacts) != 1 {
		t.Fatal("missing text evidence")
	}

	for _, data := range result.Artifacts {
		if !bytes.Contains(data, []byte("OBSERVATION_READY")) {
			t.Fatal("missing captured marker")
		}
	}

	if _, err := model.Encode(result.Manifest); err != nil {
		t.Fatal(err)
	}
}

func assertCapturedClosure(t *testing.T, server *tmux.Server, first, second, third tmux.Session) {
	t.Helper()

	result, err := capture.Observe(t.Context(), server, []tmux.SessionID{first.ID()})
	if err != nil {
		t.Fatal(err)
	}

	m := result.Manifest
	if len(m.Sessions) != 3 || len(m.Windows) != 2 || len(m.Panes) != 4 || len(m.Groups) != 1 || len(m.Links) != 4 {
		t.Fatalf("incorrect graph closure: %d sessions, %d windows, %d panes, %d groups, %d links", len(m.Sessions), len(m.Windows), len(m.Panes), len(m.Groups), len(m.Links))
	}

	for _, session := range []tmux.Session{first, second, third} {
		info, err := session.Info(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		if !slices.ContainsFunc(m.Sessions, func(s model.Session) bool { return string(s.Name) == info.Name }) {
			t.Fatal("lost session")
		}
	}

	if len(m.Groups[0].Members) != 2 {
		t.Fatal("lost group member")
	}

	if !slices.ContainsFunc(m.Links, func(l model.Link) bool { return l.Index == 9 }) {
		t.Fatal("lost sparse link")
	}
}
