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

//nolint:gosec // G101: D-Bus names, paths and state labels, not credentials.
const (
	SecretServiceBusName = "org.freedesktop.secrets"

	SecretServiceOwned         SecretServiceState = "owned"
	SecretServiceNoDesktop     SecretServiceState = "no_graphical_session"
	SecretServiceAwaitingLogin SecretServiceState = "awaiting_login"
	SecretServiceTimedOut      SecretServiceState = "timed_out"

	secretDefaultCollectionPath = "/org/freedesktop/secrets/aliases/default"
	secretCollectionIface       = "org.freedesktop.Secret.Collection"
	dbusPropertiesIface         = "org.freedesktop.DBus.Properties"
	dbusPropertiesChangedMember = "PropertiesChanged"
	dbusPropertiesChangedSignal = dbusPropertiesIface + "." + dbusPropertiesChangedMember

	displayManagerUnitName = "display-manager.service"
	systemdUnitIface       = "org.freedesktop.systemd1.Unit"

	logindSessionIface     = "org.freedesktop.login1.Session"
	logindSessionNewMember = "SessionNew"
	logindSessionNewSignal = LogindManagerIface + "." + logindSessionNewMember
	sessionNewFields       = 2
	secretSignalBuffer     = 8
	secretPollInterval     = 250 * time.Millisecond
	loginEnvWaitCap        = 5 * time.Second
)

const (
	desktopLoginNone desktopLoginKind = iota
	desktopLoginGraphical
	desktopLoginAwaiting
)

var (
	ErrSessionBusClosed = errors.New("session bus connection closed")
	ErrSystemBusClosed  = errors.New("system bus connection closed")

	graphicalSessionTypes = []string{"x11", "wayland", "mir"}
	activeUnitStates      = []string{"active", "activating"}
)

type SecretServiceState string

type desktopLoginKind int

type logindSession struct {
	ID   string
	UID  uint32
	User string
	Seat string
	Path dbus.ObjectPath
}

type logindSessionUser struct {
	UID  uint32
	Path dbus.ObjectPath
}

type loginPresence struct {
	graphical      bool
	interactiveTTY bool
	hasGreeter     bool
}

// WaitForSecretService waits up to limit for an unlocked session-bus Secret Service
// and desktop login environment when the user has a graphical login session. When the
// user is lingering at boot behind a display manager with no interactive login yet, it
// reports SecretServiceAwaitingLogin so startup restore can defer until login.
func WaitForSecretService(ctx context.Context, limit time.Duration) (SecretServiceState, error) {
	session, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("connect session bus: %w", err)
	}

	system, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return "", errors.Join(fmt.Errorf("connect system bus: %w", err), session.Close())
	}

	loginKind, loginErr := classifyDesktopLogin(ctx, system, os.Getuid())
	if err := errors.Join(loginErr, system.Close()); err != nil {
		return "", errors.Join(err, session.Close())
	}

	switch loginKind {
	case desktopLoginNone:
		return SecretServiceNoDesktop, closeSessionBus(session)
	case desktopLoginAwaiting:
		unlocked, checkErr := secretServiceUnlocked(ctx, session)
		if checkErr == nil && unlocked {
			return SecretServiceOwned, closeSessionBus(session)
		}

		return SecretServiceAwaitingLogin, closeSessionBus(session)
	case desktopLoginGraphical:
		waitCtx, cancel := context.WithTimeout(ctx, limit)
		defer cancel()

		state, waitErr := waitForSecretService(waitCtx, session)

		return state, errors.Join(waitErr, closeSessionBus(session))
	default:
		return SecretServiceNoDesktop, closeSessionBus(session)
	}
}

// WatchUserLogins invokes onLogin whenever logind opens a new interactive session
// (graphical or TTY) for uid, returning when ctx is canceled or the bus closes.
func WatchUserLogins(ctx context.Context, uid int, onLogin func(context.Context)) error {
	system, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("connect system bus: %w", err)
	}

	watchErr := watchUserLogins(ctx, system, uid, onLogin)

	return errors.Join(watchErr, system.Close())
}

func closeSessionBus(session *dbus.Conn) error {
	if err := session.Close(); err != nil {
		return fmt.Errorf("close session bus: %w", err)
	}

	return nil
}

func classifyDesktopLogin(
	ctx context.Context,
	system *dbus.Conn,
	uid int,
) (desktopLoginKind, error) {
	presence, err := inspectLoginPresence(ctx, system, uid)
	if err != nil {
		return desktopLoginNone, err
	}

	if presence.graphical {
		return desktopLoginGraphical, nil
	}

	if presence.interactiveTTY {
		return desktopLoginNone, nil
	}

	if presence.hasGreeter || displayManagerActive(ctx, system) {
		return desktopLoginAwaiting, nil
	}

	return desktopLoginNone, nil
}

func inspectLoginPresence(
	ctx context.Context,
	system *dbus.Conn,
	uid int,
) (loginPresence, error) {
	var sessions []logindSession

	manager := system.Object(LogindBusName, dbus.ObjectPath(LogindObjectPath))
	if err := manager.CallWithContext(ctx, LogindManagerIface+".ListSessions", 0).
		Store(&sessions); err != nil {
		return loginPresence{}, fmt.Errorf("list logind sessions: %w", err)
	}

	var presence loginPresence

	for _, session := range sessions {
		sessionClass, sessionType, ok, err := readSessionKind(ctx, system, session.Path)
		if err != nil {
			return loginPresence{}, err
		}

		if !ok {
			continue
		}

		if sessionClass == "greeter" {
			presence.hasGreeter = true
		}

		if int64(session.UID) != int64(uid) {
			continue
		}

		switch {
		case isGraphicalUserSession(sessionClass, sessionType):
			presence.graphical = true
		case isInteractiveTTYSession(sessionClass, sessionType):
			presence.interactiveTTY = true
		}
	}

	return presence, nil
}

func readSessionKind(
	ctx context.Context,
	system *dbus.Conn,
	path dbus.ObjectPath,
) (string, string, bool, error) {
	object := system.Object(LogindBusName, path)

	var typeVariant dbus.Variant
	if err := object.CallWithContext(ctx, dbusPropertiesIface+".Get", 0, logindSessionIface, "Type").
		Store(&typeVariant); err != nil {
		if isDBusMissingObject(err) {
			return "", "", false, nil
		}

		return "", "", false, fmt.Errorf("read logind session type: %w", err)
	}

	var classVariant dbus.Variant
	if err := object.CallWithContext(ctx, dbusPropertiesIface+".Get", 0, logindSessionIface, "Class").
		Store(&classVariant); err != nil {
		if isDBusMissingObject(err) {
			return "", "", false, nil
		}

		return "", "", false, fmt.Errorf("read logind session class: %w", err)
	}

	sessionType, _ := typeVariant.Value().(string)
	sessionClass, _ := classVariant.Value().(string)

	return sessionClass, sessionType, true, nil
}

func readSessionUID(
	ctx context.Context,
	system *dbus.Conn,
	path dbus.ObjectPath,
) (uint32, bool, error) {
	var userVariant dbus.Variant
	if err := system.Object(LogindBusName, path).
		CallWithContext(ctx, dbusPropertiesIface+".Get", 0, logindSessionIface, "User").
		Store(&userVariant); err != nil {
		if isDBusMissingObject(err) {
			return 0, false, nil
		}

		return 0, false, fmt.Errorf("read logind session user: %w", err)
	}

	switch value := userVariant.Value().(type) {
	case logindSessionUser:
		return value.UID, true, nil
	case []any:
		if len(value) > 0 {
			if uid, ok := value[0].(uint32); ok {
				return uid, true, nil
			}
		}
	}

	return 0, false, nil
}

func displayManagerActive(ctx context.Context, system *dbus.Conn) bool {
	var unitPath dbus.ObjectPath
	if err := system.Object(systemdBusName, dbus.ObjectPath(systemdObjectPath)).
		CallWithContext(ctx, systemdManagerIface+".GetUnit", 0, displayManagerUnitName).
		Store(&unitPath); err != nil || unitPath == "" {
		return false
	}

	var stateVariant dbus.Variant
	if err := system.Object(systemdBusName, unitPath).
		CallWithContext(ctx, dbusPropertiesIface+".Get", 0, systemdUnitIface, "ActiveState").
		Store(&stateVariant); err != nil {
		return false
	}

	state, ok := stateVariant.Value().(string)

	return ok && slices.Contains(activeUnitStates, state)
}

func isGraphicalUserSession(sessionClass string, sessionType string) bool {
	if sessionClass == "greeter" {
		return false
	}

	return slices.Contains(graphicalSessionTypes, sessionType)
}

func isInteractiveTTYSession(sessionClass string, sessionType string) bool {
	return sessionClass == "user" && sessionType == "tty"
}

func isInteractiveSession(sessionClass string, sessionType string) bool {
	return isGraphicalUserSession(sessionClass, sessionType) ||
		isInteractiveTTYSession(sessionClass, sessionType)
}

func isDBusMissingObject(err error) bool {
	dbusErr, ok := errors.AsType[dbus.Error](err)
	if !ok {
		return false
	}

	return dbusErr.Name == "org.freedesktop.DBus.Error.UnknownObject" ||
		dbusErr.Name == "org.freedesktop.DBus.Error.UnknownProperty" ||
		dbusErr.Name == "org.freedesktop.DBus.Error.UnknownInterface" ||
		dbusErr.Name == "org.freedesktop.DBus.Error.ServiceUnknown" ||
		dbusErr.Name == "org.freedesktop.DBus.Error.NameHasNoOwner"
}

func waitForSecretService(ctx context.Context, session *dbus.Conn) (SecretServiceState, error) {
	signals := make(chan *dbus.Signal, secretSignalBuffer)
	session.Signal(signals)

	defer session.RemoveSignal(signals)

	// Subscribe before the first check so an ownership or unlock transition is not missed.
	if err := session.AddMatchSignalContext(
		ctx,
		dbus.WithMatchSender("org.freedesktop.DBus"),
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, SecretServiceBusName),
	); err != nil {
		return "", fmt.Errorf("subscribe to secret service owner changes: %w", err)
	}

	if err := session.AddMatchSignalContext(
		ctx,
		dbus.WithMatchInterface(dbusPropertiesIface),
		dbus.WithMatchMember(dbusPropertiesChangedMember),
		dbus.WithMatchArg(0, secretCollectionIface),
	); err != nil {
		return "", fmt.Errorf("subscribe to secret collection changes: %w", err)
	}

	unlocked, err := secretServiceUnlocked(ctx, session)
	if err != nil {
		return "", err
	}

	if !unlocked {
		state, waitErr := waitUntilSecretServiceUnlocked(ctx, session, signals)
		if waitErr != nil || state != SecretServiceOwned {
			return state, waitErr
		}
	}

	if err := waitForLoginDisplay(ctx, session); err != nil {
		return "", err
	}

	return SecretServiceOwned, nil
}

func waitUntilSecretServiceUnlocked(
	ctx context.Context,
	session *dbus.Conn,
	signals <-chan *dbus.Signal,
) (SecretServiceState, error) {
	ticker := time.NewTicker(secretPollInterval)
	defer ticker.Stop()

	for {
		shouldCheck, state, err := nextSecretServiceTrigger(ctx, signals, ticker.C)
		if err != nil || state != "" {
			return state, err
		}

		if !shouldCheck {
			continue
		}

		unlocked, err := secretServiceUnlocked(ctx, session)
		if err != nil {
			return "", err
		}

		if unlocked {
			return SecretServiceOwned, nil
		}
	}
}

func nextSecretServiceTrigger(
	ctx context.Context,
	signals <-chan *dbus.Signal,
	ticks <-chan time.Time,
) (bool, SecretServiceState, error) {
	select {
	case <-ctx.Done():
		if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			return false, SecretServiceTimedOut, nil
		}

		return false, "", fmt.Errorf("wait for secret service: %w", context.Cause(ctx))
	case signal, ok := <-signals:
		if !ok {
			return false, "", ErrSessionBusClosed
		}

		return nameClaimed(signal) || collectionPropertiesChanged(signal), "", nil
	case <-ticks:
		return true, "", nil
	}
}

func secretServiceUnlocked(ctx context.Context, session *dbus.Conn) (bool, error) {
	var owned bool
	if err := session.BusObject().
		CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, SecretServiceBusName).
		Store(&owned); err != nil {
		return false, fmt.Errorf("check secret service owner: %w", err)
	}

	if !owned {
		return false, nil
	}

	var lockedVariant dbus.Variant
	if err := session.Object(SecretServiceBusName, dbus.ObjectPath(secretDefaultCollectionPath)).
		CallWithContext(ctx, dbusPropertiesIface+".Get", 0, secretCollectionIface, "Locked").
		Store(&lockedVariant); err != nil {
		if isDBusMissingObject(err) {
			return false, nil
		}

		return false, fmt.Errorf("check default secret collection lock state: %w", err)
	}

	locked, ok := lockedVariant.Value().(bool)

	return ok && !locked, nil
}

func waitForLoginDisplay(ctx context.Context, session *dbus.Conn) error {
	env, err := userManagerEnvironment(ctx, session)
	if err != nil || hasDisplayVariable(env) {
		return nil
	}

	waitCtx, cancel := context.WithTimeout(ctx, loginEnvWaitCap)
	defer cancel()

	ticker := time.NewTicker(secretPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil && !errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
				return fmt.Errorf("wait for login environment: %w", context.Cause(ctx))
			}

			return nil
		case <-ticker.C:
			env, err := userManagerEnvironment(waitCtx, session)
			if err != nil || hasDisplayVariable(env) {
				return nil
			}
		}
	}
}

func hasDisplayVariable(env map[string]string) bool {
	return env["WAYLAND_DISPLAY"] != "" || env["DISPLAY"] != ""
}

func watchUserLogins(
	ctx context.Context,
	system *dbus.Conn,
	uid int,
	onLogin func(context.Context),
) error {
	signals := make(chan *dbus.Signal, secretSignalBuffer)
	system.Signal(signals)

	defer system.RemoveSignal(signals)

	if err := system.AddMatchSignalContext(
		ctx,
		dbus.WithMatchSender(LogindBusName),
		dbus.WithMatchObjectPath(dbus.ObjectPath(LogindObjectPath)),
		dbus.WithMatchInterface(LogindManagerIface),
		dbus.WithMatchMember(logindSessionNewMember),
	); err != nil {
		return fmt.Errorf("subscribe to logind new sessions: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("watch user logins: %w", context.Cause(ctx))
		case signal, ok := <-signals:
			if !ok {
				return ErrSystemBusClosed
			}

			path, matched := sessionNewPath(signal)
			if !matched {
				continue
			}

			if sessionMatchesInteractiveLogin(ctx, system, path, uid) {
				onLogin(ctx)
			}
		}
	}
}

func sessionMatchesInteractiveLogin(
	ctx context.Context,
	system *dbus.Conn,
	path dbus.ObjectPath,
	uid int,
) bool {
	sessionUID, uidOK, err := readSessionUID(ctx, system, path)
	if err != nil || !uidOK || int64(sessionUID) != int64(uid) {
		return false
	}

	sessionClass, sessionType, kindOK, err := readSessionKind(ctx, system, path)
	if err != nil || !kindOK {
		return false
	}

	return isInteractiveSession(sessionClass, sessionType)
}

func sessionNewPath(signal *dbus.Signal) (dbus.ObjectPath, bool) {
	if signal == nil || signal.Name != logindSessionNewSignal ||
		len(signal.Body) != sessionNewFields {
		return "", false
	}

	path, ok := signal.Body[1].(dbus.ObjectPath)

	return path, ok && path != ""
}

func nameClaimed(signal *dbus.Signal) bool {
	if signal == nil || signal.Name != nameOwnerChangedSignal ||
		len(signal.Body) != nameOwnerChangedFields {
		return false
	}

	name, nameOK := signal.Body[0].(string)
	owner, ownerOK := signal.Body[2].(string)

	return nameOK && ownerOK && name == SecretServiceBusName && owner != ""
}

func collectionPropertiesChanged(signal *dbus.Signal) bool {
	if signal == nil || signal.Name != dbusPropertiesChangedSignal || len(signal.Body) < 1 {
		return false
	}

	iface, ok := signal.Body[0].(string)

	return ok && iface == secretCollectionIface
}
