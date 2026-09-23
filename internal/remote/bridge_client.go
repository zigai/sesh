//go:build linux || darwin

package remote

import (
	"bufio"
	"context"
	"net"
	"time"

	"github.com/samber/oops"
)

const bridgeClientTimeout = 2 * time.Second

type BridgeCatalog struct {
	OriginID          string
	OriginLabel       string
	CatalogGeneration uint64
	Sessions          []OriginSession
}

type BridgeClient struct {
	SocketPath string
	Token      string
	Timeout    time.Duration
}

func NewBridgeClient(socketPath, token string) *BridgeClient {
	return &BridgeClient{SocketPath: socketPath, Token: token, Timeout: bridgeClientTimeout}
}

func (client *BridgeClient) GetCatalog(ctx context.Context) (BridgeCatalog, error) {
	response, err := client.exchange(ctx, Message{
		Type: TypeCatalogRequest, Token: "", OK: false, Error: "",
		OriginID: "", OriginLabel: "", CatalogGeneration: 0, Sessions: nil, TargetID: "",
		TargetServerPID: 0, TargetServerStartedAt: time.Time{}, TargetCreatedAt: time.Time{},
	})
	if err != nil {
		return BridgeCatalog{}, oops.In("remote").With("operation", "get_catalog").Wrap(err)
	}

	if response.Type != TypeCatalogResponse || !response.OK {
		return BridgeCatalog{}, oops.In("remote").With("operation", "get_catalog").With("error", response.Error).Wrap(ErrBridgeInvalidFrame)
	}

	return BridgeCatalog{
		OriginID:          response.OriginID,
		OriginLabel:       response.OriginLabel,
		CatalogGeneration: response.CatalogGeneration,
		Sessions:          response.Sessions,
	}, nil
}

func (client *BridgeClient) RequestSwitch(ctx context.Context, catalogGeneration uint64, target OriginSession) error {
	response, err := client.exchange(ctx, Message{
		Type:                  TypeSwitchRequest,
		Token:                 "",
		OK:                    false,
		Error:                 "",
		OriginID:              "",
		OriginLabel:           "",
		CatalogGeneration:     catalogGeneration,
		Sessions:              nil,
		TargetID:              target.ID,
		TargetServerPID:       target.TmuxServerPID,
		TargetServerStartedAt: target.TmuxServerStartedAt,
		TargetCreatedAt:       target.SessionCreatedAt,
	})
	if err != nil {
		return oops.In("remote").With("operation", "request_switch").Wrap(err)
	}

	if response.Type != TypeSwitchResponse || !response.OK {
		return oops.In("remote").With("operation", "request_switch").With("error", response.Error).Wrap(ErrBridgeSwitchRejected)
	}

	return nil
}

func (client *BridgeClient) exchange(parent context.Context, request Message) (Message, error) {
	if client == nil || client.SocketPath == "" || client.Token == "" {
		return Message{}, oops.In("remote").With("operation", "bridge_exchange").Wrap(ErrBridgeSocketRequired)
	}

	ctx, cancel := context.WithTimeout(parent, positiveDuration(client.Timeout, bridgeClientTimeout))
	defer cancel()

	var dialer net.Dialer

	connection, err := dialer.DialContext(ctx, "unix", client.SocketPath)
	if err != nil {
		return Message{}, oops.In("remote").With("operation", "bridge_exchange").Wrap(err)
	}
	defer func() { _ = connection.Close() }()

	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return Message{}, oops.In("remote").With("operation", "bridge_exchange").Wrap(err)
		}
	}

	interrupted := make(chan struct{})

	stop := context.AfterFunc(ctx, func() {
		_ = connection.Close()

		close(interrupted)
	})

	defer func() {
		if !stop() {
			<-interrupted
		}
	}()

	reader := bufio.NewReaderSize(connection, BridgeMaxFrameBytes)

	if err := authenticateBridge(connection, reader, client.Token); err != nil {
		return Message{}, err
	}

	if err := WriteMessage(connection, request); err != nil {
		return Message{}, oops.In("remote").With("operation", "bridge_exchange").Wrap(err)
	}

	response, err := ReadMessage(reader)
	if err != nil {
		if ctx.Err() != nil {
			return Message{}, oops.In("remote").With("operation", "bridge_exchange").Wrap(ctx.Err())
		}

		return Message{}, oops.In("remote").With("operation", "bridge_exchange").Wrap(err)
	}

	return response, nil
}

func authenticateBridge(connection net.Conn, reader *bufio.Reader, token string) error {
	if err := WriteMessage(connection, Message{
		Type: TypeAuthRequest, Token: token, OK: false, Error: "",
		OriginID: "", OriginLabel: "", CatalogGeneration: 0, Sessions: nil, TargetID: "",
		TargetServerPID: 0, TargetServerStartedAt: time.Time{}, TargetCreatedAt: time.Time{},
	}); err != nil {
		return oops.In("remote").With("operation", "authenticate_bridge").Wrap(err)
	}

	authResponse, err := ReadMessage(reader)
	if err != nil {
		return oops.In("remote").With("operation", "authenticate_bridge").Wrap(err)
	}

	if authResponse.Type != TypeAuthResponse || !authResponse.OK {
		return oops.In("remote").With("operation", "authenticate_bridge").Wrap(ErrBridgeAuthFailed)
	}

	return nil
}
