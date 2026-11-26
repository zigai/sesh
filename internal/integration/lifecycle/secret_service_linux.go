//go:build linux

package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/godbus/dbus/v5"
)

//nolint:gosec // G101: D-Bus names and state labels, not credentials.
const (
	SecretServiceBusName = "org.freedesktop.secrets"

	SecretServiceOwned       SecretServiceState = "owned"
	SecretServiceActivatable SecretServiceState = "activatable"
	SecretServiceNoDesktop   SecretServiceState = "no_graphical_session"
	SecretServiceTimedOut    SecretServiceState = "timed_out"

	logindSessionIface = "org.freedesktop.login1.Session"
	secretSignalBuffer = 4
)

var ErrSessionBusClosed = errors.New("session bus connection closed")

var graphicalSessionTypes = []string{"x11", "wayland", "mir"}

type SecretServiceState string

type logindSession struct {
	ID   string
	UID  uint32
	User string
	Seat string
	Path dbus.ObjectPath
}

// WaitForSecretService waits up to limit for the session bus Secret Service
// when the user has a graphical login session. Desktop keyrings started by the
// login manager claim their bus name after the user manager has started units,
// so programs relaunched at boot can otherwise find no keyring and lose their
// stored credentials.
func WaitForSecretService(ctx context.Context, limit time.Duration) (SecretServiceState, error) {
	system, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("connect system bus: %w", err)
	}

	graphical, graphicalErr := userHasGraphicalSession(ctx, system, os.Getuid())

	if err := errors.Join(graphicalErr, system.Close()); err != nil {
		return "", err
	}

	if !graphical {
		return SecretServiceNoDesktop, nil
	}

	session, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("connect session bus: %w", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	state, waitErr := waitForSecretService(waitCtx, session)

	return state, errors.Join(waitErr, session.Close())
}

func userHasGraphicalSession(ctx context.Context, system *dbus.Conn, uid int) (bool, error) {
	var sessions []logindSession

	manager := system.Object(LogindBusName, dbus.ObjectPath(LogindObjectPath))
	if err := manager.CallWithContext(ctx, LogindManagerIface+".ListSessions", 0).
		Store(&sessions); err != nil {
		return false, fmt.Errorf("list logind sessions: %w", err)
	}

	for _, session := range sessions {
		if int64(session.UID) != int64(uid) {
			continue
		}

		var sessionType dbus.Variant
		if err := system.Object(LogindBusName, session.Path).
			CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, logindSessionIface, "Type").
			Store(&sessionType); err != nil {
			return false, fmt.Errorf("read logind session type: %w", err)
		}

		name, ok := sessionType.Value().(string)
		if ok && slices.Contains(graphicalSessionTypes, name) {
			return true, nil
		}
	}

	return false, nil
}

func waitForSecretService(ctx context.Context, session *dbus.Conn) (SecretServiceState, error) {
	signals := make(chan *dbus.Signal, secretSignalBuffer)
	session.Signal(signals)

	defer session.RemoveSignal(signals)

	// Subscribe before the first ownership check so a claim between the two is not missed.
	if err := session.AddMatchSignalContext(
		ctx,
		dbus.WithMatchSender("org.freedesktop.DBus"),
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, SecretServiceBusName),
	); err != nil {
		return "", fmt.Errorf("subscribe to secret service owner changes: %w", err)
	}

	state, err := currentSecretService(ctx, session)
	if err != nil || state != "" {
		return state, err
	}

	for {
		select {
		case <-ctx.Done():
			if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
				return SecretServiceTimedOut, nil
			}

			return "", fmt.Errorf("wait for secret service: %w", context.Cause(ctx))
		case signal, ok := <-signals:
			if !ok {
				return "", ErrSessionBusClosed
			}

			if nameClaimed(signal) {
				return SecretServiceOwned, nil
			}
		}
	}
}

func currentSecretService(ctx context.Context, session *dbus.Conn) (SecretServiceState, error) {
	var owned bool
	if err := session.BusObject().
		CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, SecretServiceBusName).
		Store(&owned); err != nil {
		return "", fmt.Errorf("check secret service owner: %w", err)
	}

	if owned {
		return SecretServiceOwned, nil
	}

	var activatable []string
	if err := session.BusObject().
		CallWithContext(ctx, "org.freedesktop.DBus.ListActivatableNames", 0).
		Store(&activatable); err != nil {
		return "", fmt.Errorf("list activatable session bus names: %w", err)
	}

	if slices.Contains(activatable, SecretServiceBusName) {
		return SecretServiceActivatable, nil
	}

	return "", nil
}

func nameClaimed(signal *dbus.Signal) bool {
	if signal.Name != nameOwnerChangedSignal || len(signal.Body) != nameOwnerChangedFields {
		return false
	}

	name, nameOK := signal.Body[0].(string)
	owner, ownerOK := signal.Body[2].(string)

	return nameOK && ownerOK && name == SecretServiceBusName && owner != ""
}
