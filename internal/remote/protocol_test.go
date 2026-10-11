//go:build linux || darwin

package remote

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/session"
)

func TestFrameReaderRejectsOversizedFrame(t *testing.T) {
	reader := NewFrameReader(bytes.NewReader(bytes.Repeat([]byte{'x'}, MaxFrameBytes+1)))

	if _, err := reader.Read(); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("Read() error = %v, want %v", err, ErrFrameTooLarge)
	}
}

func TestFrameReaderRejectsTruncatedTrailingFrame(t *testing.T) {
	reader := NewFrameReader(strings.NewReader(`{"version":1,"type":"hello"}`))

	if _, err := reader.Read(); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("Read() error = %v, want %v", err, ErrInvalidFrame)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	frame := Frame{
		Version: ProtocolVersion, Type: FrameHello, PeerID: "peer", StreamInstance: "stream", Sequence: 1,
		Hello: &Hello{PeerID: "peer", StreamInstance: "stream", Hostname: "host", BridgeDir: "/home/user/.local/state/sesh/remote/bridge", StartedAt: time.Unix(1, 0).UTC()},
	}

	var output bytes.Buffer
	if err := NewFrameWriter(&output).Write(frame); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if !bytes.Contains(output.Bytes(), []byte(`"peerId"`)) ||
		!bytes.Contains(output.Bytes(), []byte(`"streamInstance"`)) ||
		bytes.Contains(output.Bytes(), []byte(`"peer_id"`)) {
		t.Fatalf("Write() JSON keys are not lower camel case: %s", output.Bytes())
	}

	got, err := NewFrameReader(&output).Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if !reflect.DeepEqual(got.Hello, frame.Hello) || got.Sequence != frame.Sequence || got.PeerID != frame.PeerID || got.Type != FrameHello {
		t.Fatalf("Read() = %#v, want %#v", got, frame)
	}
}

func TestFrameReadRejectsClosedContractViolations(t *testing.T) {
	valid := `{"version":2,"type":"hello","peerId":"peer","streamInstance":"stream","sequence":1,` +
		`"hello":{"peerId":"peer","streamInstance":"stream","hostname":"host","bridgeDir":"","startedAt":"0001-01-01T00:00:00Z"}}`

	tests := map[string]string{
		"unknown member":      strings.Replace(valid, `"sequence":1`, `"sequence":1,"forged":true`, 1),
		"duplicate member":    strings.Replace(valid, `"sequence":1`, `"sequence":1,"sequence":2`, 1),
		"trailing content":    valid + `{}`,
		"unsupported version": strings.Replace(valid, `"version":2`, `"version":1`, 1),
		"zero sequence":       strings.Replace(valid, `"sequence":1`, `"sequence":0`, 1),
		"peer mismatch":       strings.Replace(valid, `"peerId":"peer","streamInstance"`, `"peerId":"other","streamInstance"`, 1),
		"payload exclusivity": strings.Replace(valid, `"startedAt":"0001-01-01T00:00:00Z"}`, `"startedAt":"0001-01-01T00:00:00Z"},"health":{"observedAt":"0001-01-01T00:00:00Z","sources":{"tmux":{"status":"ok","reason":"none","checkedAt":"0001-01-01T00:00:00Z"},"agents":{"status":"ok","reason":"none","checkedAt":"0001-01-01T00:00:00Z"},"snapshots":{"status":"ok","reason":"none","checkedAt":"0001-01-01T00:00:00Z"}}}`, 1),
		"lower cased key":     strings.Replace(valid, `"streamInstance"`, `"streaminstance"`, 1),
		"unknown frame type":  strings.Replace(valid, `"type":"hello"`, `"type":"inventory"`, 1),
		"bridge dir control":  strings.Replace(valid, `"bridgeDir":""`, `"bridgeDir":"/tmp/\u001b]0;x"`, 1),
	}

	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NewFrameReader(strings.NewReader(payload)).Read(); err == nil {
				t.Fatalf("Read() accepted %s: %s", name, payload)
			}
		})
	}
}

func TestValidateFrameRejectsTerminalControls(t *testing.T) {
	controls := make([]rune, 0, 65)
	for character := rune(0); character <= '\x1f'; character++ {
		controls = append(controls, character)
	}

	for character := rune('\x7f'); character <= '\x9f'; character++ {
		controls = append(controls, character)
	}

	for _, character := range controls {
		frame := Frame{
			Version:        ProtocolVersion,
			Type:           FrameHello,
			PeerID:         "peer",
			StreamInstance: "stream",
			Sequence:       1,
			Hello: &Hello{
				PeerID:         "peer",
				StreamInstance: "stream",
				Hostname:       "host" + string(character),
			},
		}
		if err := ValidateFrame(frame); !errors.Is(err, ErrInvalidFrame) {
			t.Fatalf("ValidateFrame() accepted control U+%04X", character)
		}
	}
}

func TestRemoteAgentMetadataRejectsControlCharacters(t *testing.T) {
	for _, agent := range []session.AgentSession{
		{GitBranch: "branch\x1b[31m"},
		{Worktree: "/work\nforged"},
		{GitChanges: "+1 -2\x1b[31m"},
		{GitAhead: -1},
		{GitBehind: -1},
		{PID: -1},
		{StatusSince: time.Time{}.Add(-time.Second)},
	} {
		if validRemoteAgent(agent) {
			t.Fatalf("accepted unsafe agent metadata: %#v", agent)
		}
	}
}

func TestValidateFrameRejectsOSCAndCSI(t *testing.T) {
	healthy := SourceHealth{
		Tmux:      SourceStatus{Status: HealthOK, Reason: HealthReasonNone},
		Agents:    SourceStatus{Status: HealthOK, Reason: HealthReasonNone},
		Snapshots: SourceStatus{Status: HealthOK, Reason: HealthReasonNone},
	}

	tests := []struct {
		name    string
		session session.Session
	}{
		{
			name: "OSC in session title",
			session: session.Session{
				ID:              "session",
				Name:            "work",
				Kind:            session.KindLive,
				ActivePaneTitle: "\x1b]0;forged title\x07",
			},
		},
		{
			name: "CSI in agent path",
			session: session.Session{
				ID:   "session",
				Name: "work",
				Kind: session.KindLive,
				Agents: []session.AgentSession{{
					CWD:    "\x1b[31mforged",
					Status: session.AgentStatusRunning,
				}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frame := Frame{
				Version:        ProtocolVersion,
				Type:           FrameSnapshot,
				PeerID:         "peer",
				StreamInstance: "stream",
				Sequence:       1,
				Snapshot:       &Snapshot{Sessions: []session.Session{test.session}, Health: healthy},
			}
			if err := ValidateFrame(frame); !errors.Is(err, ErrInvalidFrame) {
				t.Fatalf("ValidateFrame() error = %v, want %v", err, ErrInvalidFrame)
			}
		})
	}
}

func TestValidateFrameRetainsUnicode(t *testing.T) {
	frame := Frame{
		Version:        ProtocolVersion,
		Type:           FrameSnapshot,
		PeerID:         "peer-\u65e5\u672c",
		StreamInstance: "stream-λ",
		Sequence:       1,
		Snapshot: &Snapshot{
			Sessions: []session.Session{{
				ID:              "\u4f1a\u8a71",
				Name:            "\u958b\u767a",
				Kind:            session.KindLive,
				ActivePaneTitle: "\u6b63\u5e38\u306a\u30bf\u30a4\u30c8\u30eb",
				Agents: []session.AgentSession{{
					CWD:         "/\u4f5c\u696d/\u03bb",
					ProjectRoot: "/\u30d7\u30ed\u30b8\u30a7\u30af\u30c8",
					Status:      session.AgentStatusRunning,
				}},
			}},
			Health: SourceHealth{
				Tmux:      SourceStatus{Status: HealthOK, Reason: HealthReasonNone},
				Agents:    SourceStatus{Status: HealthOK, Reason: HealthReasonNone},
				Snapshots: SourceStatus{Status: HealthOK, Reason: HealthReasonNone},
			},
		},
	}
	if err := ValidateFrame(frame); err != nil {
		t.Fatalf("ValidateFrame() error = %v", err)
	}
}

func TestValidateFrameRejectsUnboundedHealthReason(t *testing.T) {
	frame := Frame{
		Version:        ProtocolVersion,
		Type:           FrameHealth,
		PeerID:         "peer",
		StreamInstance: "stream",
		Sequence:       1,
		Health: &Health{Sources: SourceHealth{
			Tmux:      SourceStatus{Status: HealthDegraded, Reason: HealthReason("\x1b]0;forged\x07raw error")},
			Agents:    SourceStatus{Status: HealthOK, Reason: HealthReasonNone},
			Snapshots: SourceStatus{Status: HealthOK, Reason: HealthReasonNone},
		}},
	}
	if err := ValidateFrame(frame); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("ValidateFrame() error = %v, want %v", err, ErrInvalidFrame)
	}
}

func TestValidateFrameRejectsInconsistentHealth(t *testing.T) {
	statuses := []SourceStatus{
		{Status: HealthOK, Reason: HealthReasonUnavailable},
		{Status: HealthDegraded, Reason: HealthReasonNone},
		{Status: HealthStatus("okay"), Reason: HealthReasonNone},
	}

	for _, status := range statuses {
		if ValidSourceHealth(SourceHealth{Tmux: status, Agents: status, Snapshots: status}) {
			t.Fatalf("accepted inconsistent health %#v", status)
		}
	}
}

func TestSnapshotBoundsRejectOversizedInventories(t *testing.T) {
	healthy := SourceHealth{
		Tmux:      SourceStatus{Status: HealthOK, Reason: HealthReasonNone},
		Agents:    SourceStatus{Status: HealthOK, Reason: HealthReasonNone},
		Snapshots: SourceStatus{Status: HealthOK, Reason: HealthReasonNone},
	}

	oversized := make([]session.Session, MaxSessions+1)
	for index := range oversized {
		oversized[index] = session.Session{ID: "s", Name: "s", Kind: session.KindSaved}
	}

	frame := Frame{
		Version: ProtocolVersion, Type: FrameSnapshot, PeerID: "peer", StreamInstance: "stream", Sequence: 1,
		Snapshot: &Snapshot{Sessions: oversized, Health: healthy},
	}
	if err := ValidateFrame(frame); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("ValidateFrame() error = %v, want %v", err, ErrInvalidFrame)
	}
}

func TestSnapshotRejectsSavedSessionsCarryingAgents(t *testing.T) {
	frame := Frame{
		Version: ProtocolVersion, Type: FrameSnapshot, PeerID: "peer", StreamInstance: "stream", Sequence: 1,
		Snapshot: &Snapshot{
			Sessions: []session.Session{{
				ID: "saved:dev", Name: "dev", Kind: session.KindSaved,
				Agents: []session.AgentSession{{CWD: "/work"}},
			}},
			Health: SourceHealth{
				Tmux:      SourceStatus{Status: HealthOK, Reason: HealthReasonNone},
				Agents:    SourceStatus{Status: HealthOK, Reason: HealthReasonNone},
				Snapshots: SourceStatus{Status: HealthOK, Reason: HealthReasonNone},
			},
		},
	}
	if err := ValidateFrame(frame); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("ValidateFrame() error = %v, want %v", err, ErrInvalidFrame)
	}
}

func TestSnapshotRejectsMalformedActionOffers(t *testing.T) {
	for name, offers := range map[string][]action.ID{
		"malformed id": {"session.kill", "Session Kill"},
		"too many":     slices.Repeat([]action.ID{action.SessionKill.ID}, MaxActions+1),
	} {
		frame := snapshotFrameWithSession(session.Session{ID: "$1", Name: "dev", Kind: session.KindLive})
		frame.Snapshot.Actions = offers

		if err := ValidateFrame(frame); !errors.Is(err, ErrInvalidFrame) {
			t.Fatalf("ValidateFrame(%s) error = %v, want %v", name, err, ErrInvalidFrame)
		}
	}
}

func snapshotFrameWithSession(row session.Session) Frame {
	return Frame{
		Version: ProtocolVersion, Type: FrameSnapshot, PeerID: "peer", StreamInstance: "stream", Sequence: 1,
		Snapshot: &Snapshot{Sessions: []session.Session{row}, Health: SourceHealth{
			Tmux:      SourceStatus{Status: HealthOK, Reason: HealthReasonNone},
			Agents:    SourceStatus{Status: HealthOK, Reason: HealthReasonNone},
			Snapshots: SourceStatus{Status: HealthOK, Reason: HealthReasonNone},
		}},
	}
}

func TestSessionFrameRoundTripPreservesSharedModel(t *testing.T) {
	stamp := time.Unix(100, 0).UTC()
	row := session.Session{
		ID: "$1", Name: "dev", Kind: session.KindLive, TmuxServerSocket: "/remote/tmux.sock",
		Generation: "generation", RepositorySource: "default",
		RuntimeIdentity: session.RuntimeIdentity{TmuxServerPID: 42, TmuxServerStartedAt: stamp, TmuxSessionCreatedAt: stamp},
		WindowCount:     3, Attached: true, Activity: stamp,
		ActivePaneID: "%2", ActiveWindowName: "editor", ActiveWindowIndex: "1", ActivePanePath: "/remote/work",
		ActivePaneCommand: "vim", ActivePaneTitle: "project",
		AgentTotal: 6, AgentActive: 1, AgentWaiting: 1, AgentFailed: 1, AgentInterrupted: 1, AgentUnknown: 1,
		AgentIdleSince: stamp, AgentWaitingSince: stamp, AgentActivityChangedAt: stamp,
		Agents: []session.AgentSession{{
			PositionKnown: true, WindowIndex: 1, PaneLeft: 20, PaneTop: 10,
			RegistryID: "agent", Harness: "codex", HarnessSession: "harness-session", PaneID: "%2", PanePID: 321,
			CWD: "/remote/work", ProjectRoot: "/remote", GitBranch: "feature", Worktree: "/remote/worktree",
			GitChanges: "+2 -1", LinkedWorktree: true, GitAhead: 2, GitBehind: 1, GitTrackingKnown: true,
			SessionPath: "/remote/session", Executable: "codex", PID: 123,
			Status: session.AgentStatusFailed, StatusSince: stamp,
		}, {
			RegistryID: "initial-agent", Harness: "codex", PaneID: "%3",
			Status: session.AgentStatusIdle, InitialIdle: true, StatusSince: stamp,
		}},
	}

	var output bytes.Buffer
	if err := NewFrameWriter(&output).Write(snapshotFrameWithSession(row)); err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(output.Bytes(), []byte(`"runtimeIdentity":{"tmuxServerPid":42`)) {
		t.Fatalf("frame does not carry nested runtime identity: %s", output.Bytes())
	}

	frame, err := NewFrameReader(&output).Read()
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(frame.Snapshot.Sessions[0], row) {
		t.Fatalf("session round trip = %#v, want %#v", frame.Snapshot.Sessions[0], row)
	}
}

func TestSessionFrameExcludesLocalState(t *testing.T) {
	row := session.Session{
		ID: "$1", Name: "dev", Kind: session.KindLive, TmuxServerSocket: "/remote/tmux.sock",
		Origin: session.OriginSSH, Current: true, RemoteStatus: session.RemoteStatusStale,
		RuntimeIdentity: session.RuntimeIdentity{
			ConnectionID: "PRIVATE_CONNECTION", ConnectionLabel: "PRIVATE_LABEL", ConfigDigest: "PRIVATE_CONFIG",
			PeerID: "PRIVATE_PEER", StreamInstance: "PRIVATE_STREAM", TmuxServerSocket: "PRIVATE_SOCKET",
		},
	}

	var output bytes.Buffer
	if err := NewFrameWriter(&output).Write(snapshotFrameWithSession(row)); err != nil {
		t.Fatal(err)
	}

	if bytes.Contains(output.Bytes(), []byte("PRIVATE_")) {
		t.Fatalf("local metadata was serialized: %s", output.Bytes())
	}

	frame, err := NewFrameReader(&output).Read()
	if err != nil {
		t.Fatal(err)
	}

	got := frame.Snapshot.Sessions[0]
	if got.Origin != "" || got.Current || got.RemoteStatus != "" || got.RuntimeIdentity != (session.RuntimeIdentity{}) {
		t.Fatalf("local state survived the wire: %#v", got)
	}
}

func TestSessionFrameRejectsPeerSuppliedLocalState(t *testing.T) {
	frame := snapshotFrameWithSession(session.Session{ID: "$1", Name: "dev", Kind: session.KindLive})

	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{
		`"connectionId":"forged"`, `"connectionLabel":"forged"`, `"configDigest":"forged"`,
		`"peerId":"forged"`, `"streamInstance":"forged"`, `"tmuxServerSocket":"forged"`,
	} {
		t.Run(field, func(t *testing.T) {
			payload := bytes.Replace(encoded, []byte(`"runtimeIdentity":{`), []byte(`"runtimeIdentity":{`+field+`,`), 1)
			if _, err := NewFrameReader(bytes.NewReader(payload)).Read(); err == nil {
				t.Fatalf("accepted peer supplied local identity %s", field)
			}
		})
	}

	for _, field := range []string{`"origin":"local"`, `"isCurrent":true`, `"remoteStatus":"stale"`} {
		t.Run(field, func(t *testing.T) {
			payload := bytes.Replace(encoded, []byte(`"id":"$1",`), []byte(`"id":"$1",`+field+`,`), 1)
			if _, err := NewFrameReader(bytes.NewReader(payload)).Read(); err == nil {
				t.Fatalf("accepted peer supplied local state %s", field)
			}
		})
	}
}

func TestSnapshotWireCarriesRawIdentityBytes(t *testing.T) {
	raw := []byte{0xff, 0xfe, 'd', 'e', 'v'}
	row := session.Session{
		ID: "$1", Name: "dev", Kind: session.KindLive, NameRaw: raw, PathRaw: raw,
	}

	var output bytes.Buffer
	if err := NewFrameWriter(&output).Write(snapshotFrameWithSession(row)); err != nil {
		t.Fatal(err)
	}

	frame, err := NewFrameReader(&output).Read()
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(frame.Snapshot.Sessions[0].NameRaw, raw) || !bytes.Equal(frame.Snapshot.Sessions[0].PathRaw, raw) {
		t.Fatalf("raw identity bytes were not preserved: %#v", frame.Snapshot.Sessions[0])
	}
}

func FuzzFrameReader(f *testing.F) {
	seed, err := json.Marshal(Frame{
		Version: ProtocolVersion, Type: FrameHealth, PeerID: "peer", StreamInstance: "stream", Sequence: 1,
		Health: &Health{Sources: SourceHealth{
			Tmux:      SourceStatus{Status: HealthOK, Reason: HealthReasonNone},
			Agents:    SourceStatus{Status: HealthDegraded, Reason: HealthReasonUnavailable},
			Snapshots: SourceStatus{Status: HealthUnknown, Reason: HealthReasonUnavailable},
		}},
	})
	if err != nil {
		f.Fatal(err)
	}

	f.Add(seed)
	f.Add([]byte(`{"version":1}`))
	f.Add([]byte{0xff, 0xfe, '\n'})

	f.Fuzz(func(t *testing.T, payload []byte) {
		frame, readErr := NewFrameReader(bytes.NewReader(payload)).Read()
		if readErr != nil {
			return
		}

		if err := ValidateFrame(frame); err != nil {
			t.Fatalf("accepted frame failed validation: %v", err)
		}

		encoded, err := json.Marshal(frame)
		if err != nil {
			t.Fatalf("re-encoding accepted frame failed: %v", err)
		}

		if _, err := NewFrameReader(bytes.NewReader(encoded)).Read(); err != nil {
			t.Fatalf("re-reading accepted frame failed: %v", err)
		}
	})
}
