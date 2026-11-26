//go:build linux

package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
)

const (
	systemdBusName      = "org.freedesktop.systemd1"
	systemdObjectPath   = "/org/freedesktop/systemd1"
	systemdManagerIface = "org.freedesktop.systemd1.Manager"
)

var ErrUserManagerEnvironment = errors.New("systemd user manager environment is not a string list")

// LoginEnvironment reads the environment the systemd user manager hands to the services it
// starts. Desktop sessions import their per-login variables there, so it describes the current
// login even for a process the manager started before the desktop came up.
func LoginEnvironment(ctx context.Context) (map[string]string, error) {
	session, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("connect session bus: %w", err)
	}

	environment, readErr := userManagerEnvironment(ctx, session)

	if err := errors.Join(readErr, session.Close()); err != nil {
		return nil, err
	}

	return environment, nil
}

func userManagerEnvironment(ctx context.Context, session *dbus.Conn) (map[string]string, error) {
	var property dbus.Variant
	if err := session.Object(systemdBusName, dbus.ObjectPath(systemdObjectPath)).
		CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, systemdManagerIface, "Environment").
		Store(&property); err != nil {
		return nil, fmt.Errorf("read systemd user manager environment: %w", err)
	}

	assignments, ok := property.Value().([]string)
	if !ok {
		return nil, ErrUserManagerEnvironment
	}

	environment := make(map[string]string, len(assignments))

	for _, assignment := range assignments {
		name, value, ok := strings.Cut(assignment, "=")
		if ok && name != "" {
			environment[name] = value
		}
	}

	return environment, nil
}
