//go:build linux || darwin

package remote

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json/v2"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/rs/zerolog"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/logging"
	"github.com/zigai/sesh/internal/privatefs"
)

const (
	bridgeInitialFrameCapacity = 256
	BridgeMaxFrameBytes        = 1 << 20
	bridgeHandshakeTimeout     = 5 * time.Second
	bridgeRequestTimeout       = 30 * time.Second
	bridgeMaxConnections       = 8
	bridgeMaxHandshakes        = 2
	bridgeMaxRequests          = 64
)

const (
	TypeAuthRequest     MessageType = "auth_req"
	TypeAuthResponse    MessageType = "auth_resp"
	TypeCatalogRequest  MessageType = "catalog_req"
	TypeCatalogResponse MessageType = "catalog_resp"
	TypeSwitchRequest   MessageType = "switch_req"
	TypeSwitchResponse  MessageType = "switch_resp"
)

var (
	ErrBridgeFrameTooLarge  = errors.New("bridge frame exceeds size limit")
	ErrBridgeInvalidFrame   = errors.New("invalid bridge frame")
	ErrBridgeAuthFailed     = errors.New("bridge authentication failed")
	ErrBridgeSwitchRejected = errors.New("bridge switch rejected")
	ErrBridgeClosed         = errors.New("bridge closed")
	ErrBridgeSocketRequired = errors.New("bridge socket path and capability are required")
	ErrBridgeSocketExists   = errors.New("bridge socket path already exists")
	ErrBridgeBusy           = errors.New("bridge connection limit reached")
	ErrBridgeStaleCatalog   = errors.New("bridge catalog generation is stale")
)

type BridgeServerConfig struct {
	SocketPath   string
	Token        string
	ExpiresAt    time.Time
	OriginID     string
	OriginLabel  string
	ListSessions func() ([]OriginSession, error)
	Now          func() time.Time
}

type BridgeServer struct {
	listener      net.Listener
	dir           *privatefs.Dir
	socketPath    string
	socketName    string
	token         string
	expiresAt     time.Time
	originID      string
	originLabel   string
	listSessions  func() ([]OriginSession, error)
	now           func() time.Time
	switchTarget  atomic.Pointer[OriginSession]
	switchNotify  chan struct{}
	switchOnce    sync.Once
	generation    atomic.Uint64
	closed        atomic.Bool
	handshakes    chan struct{}
	connectionsMu sync.Mutex
	connections   map[net.Conn]struct{}
	wg            sync.WaitGroup
	closeOnce     sync.Once
	logger        zerolog.Logger
	closeErr      error
}

type MessageType string

type OriginSession struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	WindowCount         int       `json:"windowCount"`
	Attached            bool      `json:"attached"`
	AgentTotal          int       `json:"agentTotal"`
	AgentActive         int       `json:"agentActive"`
	AgentWaiting        int       `json:"agentWaiting"`
	TmuxServerPID       int       `json:"tmuxServerPid"`
	TmuxServerStartedAt time.Time `json:"tmuxServerStartedAt"`
	SessionCreatedAt    time.Time `json:"sessionCreatedAt"`
}

type Message struct {
	Type                  MessageType     `json:"type"`
	Token                 string          `json:"token"`
	OK                    bool            `json:"ok"`
	Error                 string          `json:"error"`
	OriginID              string          `json:"originId"`
	OriginLabel           string          `json:"originLabel"`
	CatalogGeneration     uint64          `json:"catalogGeneration"`
	Sessions              []OriginSession `json:"sessions"`
	TargetID              string          `json:"targetId"`
	TargetServerPID       int             `json:"targetServerPid"`
	TargetServerStartedAt time.Time       `json:"targetServerStartedAt"`
	TargetCreatedAt       time.Time       `json:"targetCreatedAt"`
}

func WriteMessage(writer io.Writer, message Message) error {
	if writer == nil {
		return ErrBridgeInvalidFrame
	}

	if err := validateMessage(message); err != nil {
		return err
	}

	encoded, err := json.Marshal(message)
	if err != nil {
		return oops.In("remote").With("operation", "write_bridge_message").Wrap(err)
	}

	if len(encoded)+1 > BridgeMaxFrameBytes {
		return ErrBridgeFrameTooLarge
	}

	encoded = append(encoded, '\n')

	written, err := writer.Write(encoded)
	if err != nil {
		return oops.In("remote").With("operation", "write_bridge_message").Wrap(err)
	}

	if written != len(encoded) {
		return oops.In("remote").With("operation", "write_bridge_message").Wrap(io.ErrShortWrite)
	}

	return nil
}

func ReadMessage(reader *bufio.Reader) (Message, error) {
	if reader == nil {
		return Message{}, ErrBridgeInvalidFrame
	}

	line, err := readBridgeFrame(reader)
	if err != nil {
		return Message{}, err
	}

	trimmed := bytes.TrimRight(line, "\r\n")
	if len(trimmed) == 0 {
		return Message{}, ErrBridgeInvalidFrame
	}

	message, err := decodeStrict[Message](trimmed)
	if err != nil {
		return Message{}, ErrBridgeInvalidFrame
	}

	if err := validateMessage(message); err != nil {
		return Message{}, err
	}

	return message, nil
}

func NewBridgeServer(ctx context.Context, cfg BridgeServerConfig) (*BridgeServer, error) {
	if cfg.SocketPath == "" || cfg.Token == "" || cfg.ExpiresAt.IsZero() {
		return nil, oops.In("remote").With("operation", "new_bridge_server").Wrap(ErrBridgeSocketRequired)
	}

	if !validSocketPath(cfg.SocketPath) {
		return nil, oops.In("remote").With("operation", "new_bridge_server").Wrap(ErrUnsafePath)
	}

	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	if !now().Before(cfg.ExpiresAt) {
		return nil, oops.In("remote").With("operation", "new_bridge_server").Wrap(ErrCapabilityExpired)
	}

	dirPath := filepath.Dir(cfg.SocketPath)

	dir, err := openPrivateDir(dirPath, true)
	if err != nil {
		return nil, oops.In("remote").With("operation", "new_bridge_server").Wrap(err)
	}

	socketName := filepath.Base(cfg.SocketPath)

	if err := refuseExistingSocket(dir, socketName); err != nil {
		_ = dir.Close()

		return nil, oops.In("remote").With("operation", "new_bridge_server").Wrap(err)
	}

	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(ctx, "unix", cfg.SocketPath)
	if err != nil {
		_ = dir.Close()

		return nil, oops.In("remote").With("operation", "new_bridge_server").Wrap(err)
	}

	if err := os.Chmod(cfg.SocketPath, privateFileMode); err != nil {
		_ = listener.Close()
		_ = dir.Close()

		return nil, oops.In("remote").With("operation", "new_bridge_server").Wrap(err)
	}

	server := &BridgeServer{
		listener:      listener,
		dir:           dir,
		socketPath:    cfg.SocketPath,
		socketName:    socketName,
		token:         cfg.Token,
		expiresAt:     cfg.ExpiresAt,
		originID:      cfg.OriginID,
		originLabel:   cfg.OriginLabel,
		listSessions:  cfg.ListSessions,
		now:           now,
		switchTarget:  atomic.Pointer[OriginSession]{},
		switchNotify:  make(chan struct{}),
		switchOnce:    sync.Once{},
		generation:    atomic.Uint64{},
		closed:        atomic.Bool{},
		handshakes:    make(chan struct{}, bridgeMaxHandshakes),
		connectionsMu: sync.Mutex{},
		connections:   make(map[net.Conn]struct{}),
		wg:            sync.WaitGroup{},
		closeOnce:     sync.Once{},
		closeErr:      nil,
		logger:        zerolog.Ctx(ctx).With().Str("component", "remote_bridge").Logger(),
	}

	server.wg.Add(1)

	go server.serve()

	return server, nil
}

func (server *BridgeServer) SwitchTarget() (OriginSession, bool) {
	target := server.switchTarget.Load()
	if target == nil {
		return OriginSession{ID: "", Name: "", WindowCount: 0, Attached: false, AgentTotal: 0, AgentActive: 0, AgentWaiting: 0, TmuxServerPID: 0, TmuxServerStartedAt: time.Time{}, SessionCreatedAt: time.Time{}}, false
	}

	return *target, true
}

func (server *BridgeServer) SwitchRequested() <-chan struct{} {
	return server.switchNotify
}

func (server *BridgeServer) Close() error {
	server.closeOnce.Do(func() {
		server.closed.Store(true)

		if err := server.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			server.closeErr = oops.In("remote").With("operation", "close_bridge_server").Wrap(err)
		}

		server.connectionsMu.Lock()

		for connection := range server.connections {
			if err := connection.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				server.closeErr = errors.Join(server.closeErr, oops.In("remote").With("operation", "close_bridge_server").Wrap(err))
			}
		}
		server.connectionsMu.Unlock()

		server.wg.Wait()

		if err := server.unlinkOwnedSocket(); err != nil {
			server.closeErr = errors.Join(server.closeErr, err)
		}

		if err := server.dir.Close(); err != nil {
			server.closeErr = errors.Join(server.closeErr, err)
		}
	})

	return server.closeErr
}

func (server *BridgeServer) unlinkOwnedSocket() error {
	var stat unix.Stat_t

	if err := unix.Lstat(server.socketPath, &stat); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}

		return oops.In("remote").With("operation", "unlink_bridge_socket").Wrap(err)
	}

	if stat.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return oops.In("remote").With("operation", "unlink_bridge_socket").Wrap(ErrUnsafePath)
	}

	if int64(stat.Uid) != int64(os.Geteuid()) {
		return oops.In("remote").With("operation", "unlink_bridge_socket").Wrap(ErrUnsafePath)
	}

	return removeFile(server.dir, server.socketName)
}

func (server *BridgeServer) serve() {
	defer server.wg.Done()

	for {
		connection, err := server.listener.Accept()
		if err != nil {
			if !server.closed.Load() {
				server.logger.Error().Str("operation", "accept").Str("status", "failed").Str("reason", "listener_failed").
					Func(logging.Cause(err)).Msg("bridge listener stopped unexpectedly")
			}

			return
		}

		if server.closed.Load() {
			_ = connection.Close()

			continue
		}

		if !server.admit(connection) {
			server.logger.Warn().Str("operation", "accept").Str("status", "skipped").Str("reason", "capacity").
				Msg("bridge connection refused at capacity")

			_ = connection.Close()

			continue
		}

		server.wg.Add(1)

		go func(accepted net.Conn) {
			defer server.wg.Done()

			defer func() {
				_ = accepted.Close()

				server.connectionsMu.Lock()
				delete(server.connections, accepted)
				server.connectionsMu.Unlock()
			}()

			server.handleConnection(accepted)
		}(connection)
	}
}

func (server *BridgeServer) admit(connection net.Conn) bool {
	select {
	case server.handshakes <- struct{}{}:
	default:
		return false
	}

	server.connectionsMu.Lock()
	defer server.connectionsMu.Unlock()

	if len(server.connections) >= bridgeMaxConnections {
		<-server.handshakes

		return false
	}

	server.connections[connection] = struct{}{}

	return true
}

func (server *BridgeServer) handleConnection(connection net.Conn) {
	reader := bufio.NewReaderSize(connection, BridgeMaxFrameBytes)

	_ = connection.SetDeadline(time.Now().Add(bridgeHandshakeTimeout))

	authMessage, err := ReadMessage(reader)

	<-server.handshakes

	if err != nil || authMessage.Type != TypeAuthRequest || !server.authenticated(authMessage.Token) {
		_ = server.reply(connection, err, Message{
			Type: TypeAuthResponse, Token: "", OK: false, Error: "unauthorized",
			OriginID: "", OriginLabel: "", CatalogGeneration: 0, Sessions: nil, TargetID: "",
			TargetServerPID: 0, TargetServerStartedAt: time.Time{}, TargetCreatedAt: time.Time{},
		})

		return
	}

	if err := server.reply(connection, nil, Message{
		Type: TypeAuthResponse, Token: "", OK: true, Error: "",
		OriginID: "", OriginLabel: "", CatalogGeneration: 0, Sessions: nil, TargetID: "",
		TargetServerPID: 0, TargetServerStartedAt: time.Time{}, TargetCreatedAt: time.Time{},
	}); err != nil {
		return
	}

	for range bridgeMaxRequests {
		_ = connection.SetDeadline(time.Now().Add(bridgeRequestTimeout))

		message, err := ReadMessage(reader)
		if err != nil {
			return
		}

		switch message.Type {
		case TypeCatalogRequest:
			server.handleCatalogRequest(connection)
		case TypeSwitchRequest:
			server.handleSwitchRequest(connection, message)
		case TypeAuthRequest, TypeAuthResponse, TypeCatalogResponse, TypeSwitchResponse:
			return
		}
	}
}

func (server *BridgeServer) authenticated(token string) bool {
	if !server.now().Before(server.expiresAt) {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(server.token), []byte(token)) == 1
}

func (server *BridgeServer) handleCatalogRequest(connection net.Conn) {
	sessions, err := server.listSessions()
	if err != nil {
		_ = server.reply(connection, err, Message{
			Type: TypeCatalogResponse, Token: "", OK: false, Error: "catalog_failed",
			OriginID: "", OriginLabel: "", CatalogGeneration: 0, Sessions: nil, TargetID: "",
			TargetServerPID: 0, TargetServerStartedAt: time.Time{}, TargetCreatedAt: time.Time{},
		})

		return
	}

	generation := server.generation.Add(1)

	_ = server.reply(connection, nil, Message{
		Type: TypeCatalogResponse, Token: "", OK: true, Error: "",
		OriginID: server.originID, OriginLabel: server.originLabel, CatalogGeneration: generation,
		Sessions: sessions, TargetID: "",
		TargetServerPID: 0, TargetServerStartedAt: time.Time{}, TargetCreatedAt: time.Time{},
	})
}

func (server *BridgeServer) handleSwitchRequest(connection net.Conn, message Message) {
	if message.TargetID == "" {
		_ = server.reply(connection, nil, Message{
			Type: TypeSwitchResponse, Token: "", OK: false, Error: "empty_target",
			OriginID: "", OriginLabel: "", CatalogGeneration: 0, Sessions: nil, TargetID: "",
			TargetServerPID: 0, TargetServerStartedAt: time.Time{}, TargetCreatedAt: time.Time{},
		})

		return
	}

	if message.CatalogGeneration == 0 || message.CatalogGeneration != server.generation.Load() {
		_ = server.reply(connection, nil, Message{
			Type: TypeSwitchResponse, Token: "", OK: false, Error: "catalog_failed",
			OriginID: "", OriginLabel: "", CatalogGeneration: 0, Sessions: nil, TargetID: "",
			TargetServerPID: 0, TargetServerStartedAt: time.Time{}, TargetCreatedAt: time.Time{},
		})

		return
	}

	sessions, err := server.listSessions()
	if err != nil {
		_ = server.reply(connection, err, Message{
			Type: TypeSwitchResponse, Token: "", OK: false, Error: "catalog_failed",
			OriginID: "", OriginLabel: "", CatalogGeneration: 0, Sessions: nil, TargetID: "",
			TargetServerPID: 0, TargetServerStartedAt: time.Time{}, TargetCreatedAt: time.Time{},
		})

		return
	}

	for _, session := range sessions {
		if session.ID != message.TargetID {
			continue
		}

		if session.TmuxServerPID != message.TargetServerPID ||
			!session.TmuxServerStartedAt.Equal(message.TargetServerStartedAt) ||
			!session.SessionCreatedAt.Equal(message.TargetCreatedAt) {
			_ = server.reply(connection, nil, Message{
				Type: TypeSwitchResponse, Token: "", OK: false, Error: "target_changed",
				OriginID: "", OriginLabel: "", CatalogGeneration: 0, Sessions: nil, TargetID: "",
				TargetServerPID: 0, TargetServerStartedAt: time.Time{}, TargetCreatedAt: time.Time{},
			})

			return
		}

		target := session
		server.switchTarget.Store(&target)
		server.switchOnce.Do(func() { close(server.switchNotify) })

		_ = server.reply(connection, nil, Message{
			Type: TypeSwitchResponse, Token: "", OK: true, Error: "",
			OriginID: "", OriginLabel: "", CatalogGeneration: 0, Sessions: nil, TargetID: "",
			TargetServerPID: 0, TargetServerStartedAt: time.Time{}, TargetCreatedAt: time.Time{},
		})

		return
	}

	_ = server.reply(connection, nil, Message{
		Type: TypeSwitchResponse, Token: "", OK: false, Error: "unknown_target",
		OriginID: "", OriginLabel: "", CatalogGeneration: 0, Sessions: nil, TargetID: "",
		TargetServerPID: 0, TargetServerStartedAt: time.Time{}, TargetCreatedAt: time.Time{},
	})
}

func (server *BridgeServer) reply(connection net.Conn, cause error, message Message) error {
	if !message.OK {
		server.logger.Warn().Str("operation", string(message.Type)).Str("status", "rejected").Str("reason", message.Error).
			Func(logging.Cause(cause)).Msg("bridge request rejected")
	}

	err := WriteMessage(connection, message)
	if err != nil {
		server.logger.Warn().Str("operation", string(message.Type)).Str("status", "failed").Str("reason", "write_failed").
			Func(logging.Cause(err)).Msg("bridge response was not delivered")
	}

	return err
}

func validateMessage(message Message) error {
	switch message.Type {
	case TypeAuthRequest, TypeAuthResponse, TypeCatalogRequest, TypeCatalogResponse, TypeSwitchRequest, TypeSwitchResponse:
	default:
		return ErrBridgeInvalidFrame
	}

	if err := validateMessageText(message); err != nil {
		return err
	}

	return validateMessageSessions(message.Sessions)
}

func validateMessageText(message Message) error {
	if !ValidText(message.Token) || !ValidText(message.Error) ||
		!ValidIdentifier(message.OriginID) && message.OriginID != "" ||
		!ValidText(message.OriginLabel) || !ValidText(message.TargetID) {
		return ErrBridgeInvalidFrame
	}

	if message.Error != "" && !validBridgeErrorCode(message.Error) {
		return ErrBridgeInvalidFrame
	}

	return nil
}

func validateMessageSessions(sessions []OriginSession) error {
	if len(sessions) > MaxSessions {
		return ErrBridgeInvalidFrame
	}

	for _, session := range sessions {
		if !ValidIdentifier(session.ID) || !ValidIdentifier(session.Name) ||
			session.WindowCount < 0 || session.AgentTotal < 0 ||
			session.AgentActive < 0 || session.AgentWaiting < 0 {
			return ErrBridgeInvalidFrame
		}
	}

	return nil
}

func validBridgeErrorCode(code string) bool {
	switch code {
	case "unauthorized", "empty_target", "unknown_target", "catalog_failed", "switch_rejected", "target_changed", "busy", "invalid_request":
		return true
	default:
		return false
	}
}

func readBridgeFrame(reader *bufio.Reader) ([]byte, error) {
	frame := make([]byte, 0, bridgeInitialFrameCapacity)

	for {
		part, err := reader.ReadSlice('\n')
		if len(part) > BridgeMaxFrameBytes-len(frame) {
			return nil, ErrBridgeFrameTooLarge
		}

		frame = append(frame, part...)

		if err == nil {
			return frame, nil
		}

		if len(frame) >= BridgeMaxFrameBytes {
			return nil, ErrBridgeFrameTooLarge
		}

		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}

		if errors.Is(err, io.EOF) && len(frame) > 0 {
			return nil, ErrBridgeInvalidFrame
		}

		return nil, oops.In("remote").With("operation", "read_bridge_frame").Wrap(err)
	}
}

func refuseExistingSocket(dir *privatefs.Dir, name string) error {
	if !filepath.IsAbs(name) && filepath.Base(name) == name && name != "." && name != ".." {
		names, err := dir.Names()
		if err != nil {
			return oops.In("remote").With("operation", "refuse_existing_socket").Wrap(err)
		}

		if slices.Contains(names, name) {
			return oops.In("remote").With("operation", "refuse_existing_socket").With("socket_name", name).Wrap(ErrBridgeSocketExists)
		}

		return nil
	}

	return oops.In("remote").With("operation", "refuse_existing_socket").Wrap(ErrUnsafePath)
}
