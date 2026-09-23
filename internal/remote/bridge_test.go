//go:build linux || darwin

package remote

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var errTestCatalogFailed = errors.New("catalog failed")

func TestBridgeMessageRoundTripAndClosedVocabulary(t *testing.T) {
	message := Message{
		Type:              TypeCatalogResponse,
		OK:                true,
		OriginID:          "laptop",
		OriginLabel:       "Laptop",
		CatalogGeneration: 3,
		Sessions:          []OriginSession{{ID: "$1", Name: testSessionName, WindowCount: 2, Attached: true, AgentTotal: 1}},
	}

	var encoded bytes.Buffer

	if err := WriteMessage(&encoded, message); err != nil {
		t.Fatalf("WriteMessage() error = %v", err)
	}

	decoded, err := ReadMessage(bufio.NewReader(&encoded))
	if err != nil {
		t.Fatalf("ReadMessage() error = %v", err)
	}

	if decoded.OriginID != "laptop" || decoded.CatalogGeneration != 3 || len(decoded.Sessions) != 1 || decoded.Sessions[0].Name != testSessionName {
		t.Fatalf("decoded message = %#v, want the published catalog", decoded)
	}

	invalid := []Message{
		{Type: "unknown_type", OK: true},
		{Type: TypeCatalogResponse, OK: false, Error: "raw remote failure"},
		{Type: TypeCatalogResponse, OK: true, Sessions: []OriginSession{{ID: "", Name: testSessionName}}},
		{Type: TypeCatalogResponse, OK: true, Sessions: []OriginSession{{ID: "$1", Name: testSessionName, WindowCount: -1}}},
		{Type: TypeCatalogResponse, OK: true, OriginLabel: "label\x1b[31m"},
	}

	for _, message := range invalid {
		if err := WriteMessage(&bytes.Buffer{}, message); !errors.Is(err, ErrBridgeInvalidFrame) {
			t.Fatalf("WriteMessage(%#v) error = %v, want %v", message, err, ErrBridgeInvalidFrame)
		}
	}

	oversized := Message{Type: TypeCatalogResponse, OK: true, Error: ""}
	oversized.Sessions = make([]OriginSession, MaxSessions+1)

	if err := WriteMessage(&bytes.Buffer{}, oversized); !errors.Is(err, ErrBridgeInvalidFrame) {
		t.Fatalf("WriteMessage(oversized sessions) error = %v, want %v", err, ErrBridgeInvalidFrame)
	}
}

func TestBridgeMessageRejectsUnknownMembersAndOversizedFrames(t *testing.T) {
	valid := `{"type":"auth_req","token":"` + strings.Repeat("a", 64) + `","ok":false,"error":"","originId":"","originLabel":"","catalogGeneration":0,"sessions":null,"targetId":""}`

	payloads := map[string]string{
		"unknown member":   strings.Replace(valid, `"ok":false`, `"ok":false,"forged":1`, 1),
		"duplicate member": strings.Replace(valid, `"ok":false`, `"ok":false,"ok":true`, 1),
		"trailing content": valid + `{}`,
		"oversized":        `{"type":"auth_req","token":"` + strings.Repeat("a", BridgeMaxFrameBytes) + `"}`,
	}

	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadMessage(bufio.NewReader(strings.NewReader(payload + "\n"))); err == nil {
				t.Fatalf("ReadMessage() accepted %s", name)
			}
		})
	}
}

func TestBridgeServerRequiresPrivateSocketAndCapability(t *testing.T) {
	if _, err := NewBridgeServer(t.Context(), BridgeServerConfig{}); !errors.Is(err, ErrBridgeSocketRequired) {
		t.Fatalf("NewBridgeServer(empty) error = %v, want %v", err, ErrBridgeSocketRequired)
	}

	dir, path := openTestBridgeDir(t)

	if _, err := NewBridgeServer(t.Context(), BridgeServerConfig{
		SocketPath: "relative.sock",
		Token:      strings.Repeat("a", 64),
		ExpiresAt:  time.Now().Add(time.Minute),
	}); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("NewBridgeServer(relative) error = %v, want %v", err, ErrUnsafePath)
	}

	if _, err := NewBridgeServer(t.Context(), BridgeServerConfig{
		SocketPath: filepath.Join(path, "bridge.sock"),
		Token:      strings.Repeat("a", 64),
		ExpiresAt:  time.Now().Add(-time.Second),
	}); !errors.Is(err, ErrCapabilityExpired) {
		t.Fatalf("NewBridgeServer(expired) error = %v, want %v", err, ErrCapabilityExpired)
	}

	if _, err := dir.Names(); err != nil {
		t.Fatal(err)
	}
}

func TestBridgeServerRefusesExistingSocketPath(t *testing.T) {
	_, path := openTestBridgeDir(t)

	socketPath := filepath.Join(path, "bridge.sock")

	if err := os.WriteFile(socketPath, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := NewBridgeServer(t.Context(), BridgeServerConfig{
		SocketPath: socketPath,
		Token:      strings.Repeat("a", 64),
		ExpiresAt:  time.Now().Add(time.Minute),
	})
	if !errors.Is(err, ErrBridgeSocketExists) {
		t.Fatalf("NewBridgeServer(existing path) error = %v, want %v", err, ErrBridgeSocketExists)
	}

	content, err := os.ReadFile(socketPath)
	if err != nil {
		t.Fatalf("existing socket path was removed: %v", err)
	}

	if string(content) != "foreign" {
		t.Fatalf("existing entry = %q, want the foreign bytes preserved", content)
	}
}

func bridgeServerFixture(t *testing.T) (*BridgeServer, string, string) {
	t.Helper()

	_, path := openTestBridgeDir(t)

	token, err := NewBridgeToken()
	if err != nil {
		t.Fatal(err)
	}

	socketPath := filepath.Join(path, "bridge.sock")

	server, err := NewBridgeServer(t.Context(), BridgeServerConfig{
		SocketPath:  socketPath,
		Token:       token,
		ExpiresAt:   time.Now().Add(time.Minute),
		OriginID:    "laptop",
		OriginLabel: "Laptop",
		ListSessions: func() ([]OriginSession, error) {
			return []OriginSession{{
				ID:                  "$1",
				Name:                testSessionName,
				WindowCount:         2,
				Attached:            true,
				AgentTotal:          0,
				AgentActive:         0,
				AgentWaiting:        0,
				TmuxServerPID:       42,
				TmuxServerStartedAt: time.Unix(100, 0).UTC(),
				SessionCreatedAt:    time.Unix(200, 0).UTC(),
			}}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewBridgeServer() error = %v", err)
	}

	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("closing bridge server: %v", err)
		}
	})

	return server, socketPath, token
}

func TestBridgeServerServesCatalogToAuthenticatedClient(t *testing.T) {
	_, socketPath, token := bridgeServerFixture(t)

	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Fatalf("bridge socket mode = %o, want 0600", info.Mode().Perm())
	}

	client := NewBridgeClient(socketPath, token)

	catalog, err := client.GetCatalog(t.Context())
	if err != nil {
		t.Fatalf("GetCatalog() error = %v", err)
	}

	if catalog.OriginID != "laptop" || catalog.OriginLabel != "Laptop" || catalog.CatalogGeneration != 1 {
		t.Fatalf("catalog = %#v, want the origin catalog with a generation", catalog)
	}

	if len(catalog.Sessions) != 1 || catalog.Sessions[0].Name != testSessionName {
		t.Fatalf("catalog sessions = %#v, want the origin sessions", catalog.Sessions)
	}
}

func TestBridgeClientRejectsWrongCapability(t *testing.T) {
	_, socketPath, _ := bridgeServerFixture(t)

	wrong := NewBridgeClient(socketPath, strings.Repeat("b", 64))

	if _, err := wrong.GetCatalog(t.Context()); !errors.Is(err, ErrBridgeAuthFailed) {
		t.Fatalf("GetCatalog(wrong token) error = %v, want %v", err, ErrBridgeAuthFailed)
	}
}

func TestBridgeServerRejectsExpiredCapability(t *testing.T) {
	_, path := openTestBridgeDir(t)

	token, err := NewBridgeToken()
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	current := now

	socketPath := filepath.Join(path, "bridge.sock")

	server, err := NewBridgeServer(t.Context(), BridgeServerConfig{
		SocketPath:   socketPath,
		Token:        token,
		ExpiresAt:    now.Add(time.Minute),
		ListSessions: func() ([]OriginSession, error) { return nil, nil },
		Now:          func() time.Time { return current },
	})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = server.Close() }()

	current = now.Add(2 * time.Minute)

	client := NewBridgeClient(socketPath, token)

	if _, err := client.GetCatalog(t.Context()); !errors.Is(err, ErrBridgeAuthFailed) {
		t.Fatalf("GetCatalog(expired) error = %v, want %v", err, ErrBridgeAuthFailed)
	}
}

func TestBridgeServerValidatesSwitchTargets(t *testing.T) {
	server, socketPath, token := bridgeServerFixture(t)

	client := NewBridgeClient(socketPath, token)

	catalog, err := client.GetCatalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	unknownTarget := OriginSession{
		ID: "$2", Name: "other", WindowCount: 1, Attached: false,
		AgentTotal: 0, AgentActive: 0, AgentWaiting: 0,
		TmuxServerPID: 42, TmuxServerStartedAt: time.Unix(100, 0).UTC(), SessionCreatedAt: time.Unix(200, 0).UTC(),
	}

	if err := client.RequestSwitch(t.Context(), catalog.CatalogGeneration, unknownTarget); !errors.Is(err, ErrBridgeSwitchRejected) {
		t.Fatalf("RequestSwitch(unknown target) error = %v, want %v", err, ErrBridgeSwitchRejected)
	}

	validTarget := catalog.Sessions[0]

	if err := client.RequestSwitch(t.Context(), catalog.CatalogGeneration+1, validTarget); !errors.Is(err, ErrBridgeSwitchRejected) {
		t.Fatalf("RequestSwitch(stale generation) error = %v, want %v", err, ErrBridgeSwitchRejected)
	}

	recycled := validTarget

	recycled.TmuxServerPID = 999
	if err := client.RequestSwitch(t.Context(), catalog.CatalogGeneration, recycled); !errors.Is(err, ErrBridgeSwitchRejected) {
		t.Fatalf("RequestSwitch(recycled server) error = %v, want %v", err, ErrBridgeSwitchRejected)
	}

	if _, ok := server.SwitchTarget(); ok {
		t.Fatal("refused switch request recorded a target")
	}

	if err := client.RequestSwitch(t.Context(), catalog.CatalogGeneration, validTarget); err != nil {
		t.Fatalf("RequestSwitch(valid target) error = %v", err)
	}

	target, ok := server.SwitchTarget()
	if !ok || target.ID != "$1" || target.Name != testSessionName {
		t.Fatalf("switch target = (%#v, %t), want the validated origin session", target, ok)
	}

	select {
	case <-server.SwitchRequested():
	default:
		t.Fatal("accepted switch request did not notify the origin")
	}
}

func TestBridgeServerBoundsUnauthenticatedWork(t *testing.T) {
	_, socketPath, token := bridgeServerFixture(t)

	idle := make([]net.Conn, 0, bridgeMaxHandshakes)

	for range bridgeMaxHandshakes {
		connection, err := (&net.Dialer{}).DialContext(t.Context(), "unix", socketPath)
		if err != nil {
			t.Fatal(err)
		}

		idle = append(idle, connection)
	}

	defer func() {
		for _, connection := range idle {
			_ = connection.Close()
		}
	}()

	client := NewBridgeClient(socketPath, token)
	client.Timeout = 500 * time.Millisecond

	if _, err := client.GetCatalog(t.Context()); err == nil {
		t.Fatal("bridge admitted work beyond its unauthenticated bound")
	}

	_ = idle[0].Close()
	_ = idle[1].Close()

	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		if _, err := client.GetCatalog(t.Context()); err == nil {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("bridge did not accept work after the unauthenticated bound was released")
}

func TestBridgeServerCloseRemovesOnlyItsOwnSocket(t *testing.T) {
	dir, path := openTestBridgeDir(t)

	token, err := NewBridgeToken()
	if err != nil {
		t.Fatal(err)
	}

	foreign := filepath.Join(path, "foreign.sock")
	if err := os.WriteFile(foreign, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}

	socketPath := filepath.Join(path, "bridge.sock")

	server, err := NewBridgeServer(t.Context(), BridgeServerConfig{
		SocketPath:   socketPath,
		Token:        token,
		ExpiresAt:    time.Now().Add(time.Minute),
		ListSessions: func() ([]OriginSession, error) { return nil, nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := server.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bridge socket survived Close(): %v", err)
	}

	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("Close() removed a foreign entry: %v", err)
	}

	if _, err := dir.Names(); err != nil {
		t.Fatal(err)
	}

	if err := server.Close(); err != nil {
		t.Fatalf("repeated Close() error = %v", err)
	}
}

func TestBridgeClientRejectsCancellation(t *testing.T) {
	_, socketPath, token := bridgeServerFixture(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	client := NewBridgeClient(socketPath, token)

	if _, err := client.GetCatalog(ctx); err == nil {
		t.Fatal("GetCatalog() ignored cancellation")
	}
}

func TestBridgeClientRequiresCapability(t *testing.T) {
	client := NewBridgeClient("", "")

	if _, err := client.GetCatalog(t.Context()); !errors.Is(err, ErrBridgeSocketRequired) {
		t.Fatalf("GetCatalog() error = %v, want %v", err, ErrBridgeSocketRequired)
	}
}

func TestBridgeRequestBoundClosesLongLivedConnections(t *testing.T) {
	_, socketPath, token := bridgeServerFixture(t)

	connection, err := (&net.Dialer{}).DialContext(t.Context(), "unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = connection.Close() }()

	reader := bufio.NewReaderSize(connection, BridgeMaxFrameBytes)

	if err := WriteMessage(connection, Message{Type: TypeAuthRequest, Token: token}); err != nil {
		t.Fatal(err)
	}

	authResponse, err := ReadMessage(reader)
	if err != nil || !authResponse.OK {
		t.Fatalf("authentication = (%#v, %v), want success", authResponse, err)
	}

	for range bridgeMaxRequests {
		if err := WriteMessage(connection, Message{Type: TypeCatalogRequest}); err != nil {
			t.Fatal(err)
		}

		if _, err := ReadMessage(reader); err != nil {
			t.Fatal(err)
		}
	}

	if err := WriteMessage(connection, Message{Type: TypeCatalogRequest}); err == nil {
		if _, err := ReadMessage(reader); err == nil {
			t.Fatal("bridge served a request beyond its bound")
		}
	}
}

func TestBridgeCatalogResponseCarriesBoundedSessions(t *testing.T) {
	dir, path := openTestBridgeDir(t)

	token, err := NewBridgeToken()
	if err != nil {
		t.Fatal(err)
	}

	oversized := make([]OriginSession, MaxSessions+1)
	for index := range oversized {
		oversized[index] = OriginSession{ID: "s" + string(rune('a'+index%26)), Name: "n"}
	}

	socketPath := filepath.Join(path, "bridge.sock")

	server, err := NewBridgeServer(t.Context(), BridgeServerConfig{
		SocketPath:   socketPath,
		Token:        token,
		ExpiresAt:    time.Now().Add(time.Minute),
		ListSessions: func() ([]OriginSession, error) { return oversized, nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = server.Close() }()

	if _, err := dir.Names(); err != nil {
		t.Fatal(err)
	}

	client := NewBridgeClient(socketPath, token)

	if _, err := client.GetCatalog(t.Context()); err == nil {
		t.Fatal("bridge published an unbounded catalog")
	}
}

func TestBridgeServerReportsCatalogFailures(t *testing.T) {
	_, path := openTestBridgeDir(t)

	token, err := NewBridgeToken()
	if err != nil {
		t.Fatal(err)
	}

	socketPath := filepath.Join(path, "bridge.sock")

	server, err := NewBridgeServer(t.Context(), BridgeServerConfig{
		SocketPath:   socketPath,
		Token:        token,
		ExpiresAt:    time.Now().Add(time.Minute),
		ListSessions: func() ([]OriginSession, error) { return nil, errTestCatalogFailed },
	})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = server.Close() }()

	client := NewBridgeClient(socketPath, token)

	if _, err := client.GetCatalog(t.Context()); !errors.Is(err, ErrBridgeInvalidFrame) {
		t.Fatalf("GetCatalog() error = %v, want %v", err, ErrBridgeInvalidFrame)
	}
}

func TestBridgeMessageEncodingIsStable(t *testing.T) {
	message := Message{Type: TypeSwitchRequest, TargetID: "$1", CatalogGeneration: 2}

	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(encoded), `"targetId":"$1"`) || !strings.Contains(string(encoded), `"catalogGeneration":2`) {
		t.Fatalf("encoded message = %s, want lower camel case members", encoded)
	}
}
