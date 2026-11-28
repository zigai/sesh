//go:build linux

package lifecycle

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

type fakeLogindSessions struct {
	sessions []logindSession
}

func (f *fakeLogindSessions) ListSessions() ([]logindSession, *dbus.Error) {
	return f.sessions, nil
}

type fakeLogindSessionProps struct {
	uid         uint32
	sessionType string
	class       string
}

func (f fakeLogindSessionProps) Get(iface string, property string) (dbus.Variant, *dbus.Error) {
	if iface != logindSessionIface {
		return dbus.Variant{}, dbus.MakeFailedError(ErrLogindSignal)
	}

	switch property {
	case "Type":
		return dbus.MakeVariant(f.sessionType), nil
	case "Class":
		return dbus.MakeVariant(f.class), nil
	case "User":
		return dbus.MakeVariant(logindSessionUser{
			UID:  f.uid,
			Path: dbus.ObjectPath("/org/freedesktop/login1/user/_1000"),
		}), nil
	default:
		return dbus.Variant{}, dbus.MakeFailedError(ErrLogindSignal)
	}
}

type fakeSystemdDisplayManager struct {
	unitPath dbus.ObjectPath
	state    string
}

func (f *fakeSystemdDisplayManager) GetUnit(name string) (dbus.ObjectPath, *dbus.Error) {
	if name != displayManagerUnitName || f.unitPath == "" {
		return "", dbus.NewError("org.freedesktop.systemd1.NoSuchUnit", nil)
	}

	return f.unitPath, nil
}

func (f *fakeSystemdDisplayManager) Get(iface string, property string) (dbus.Variant, *dbus.Error) {
	if iface != systemdUnitIface || property != "ActiveState" {
		return dbus.Variant{}, dbus.MakeFailedError(ErrLogindSignal)
	}

	return dbus.MakeVariant(f.state), nil
}

type fakeSecretCollection struct {
	locked atomic.Bool
}

func (f *fakeSecretCollection) Get(iface string, property string) (dbus.Variant, *dbus.Error) {
	if iface != secretCollectionIface || property != "Locked" {
		return dbus.Variant{}, dbus.MakeFailedError(ErrLogindSignal)
	}

	return dbus.MakeVariant(f.locked.Load()), nil
}

type fakeDynamicUserEnv struct {
	mu  sync.Mutex
	env []string
}

func (f *fakeDynamicUserEnv) Get(iface string, property string) (dbus.Variant, *dbus.Error) {
	if iface != systemdManagerIface || property != "Environment" {
		return dbus.Variant{}, dbus.MakeFailedError(ErrLogindSignal)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	return dbus.MakeVariant(append([]string(nil), f.env...)), nil
}

func (f *fakeDynamicUserEnv) set(env []string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.env = append([]string(nil), env...)
}

func TestWaitForSecretServiceReturnsWhenNameIsAlreadyOwnedAndUnlocked(t *testing.T) {
	address := startPrivateBus(t)
	keyring := connectPrivateBus(t, address)
	exportSecretCollection(t, keyring, false)
	claimNamed(t, keyring, SecretServiceBusName)

	state, err := waitForSecretService(t.Context(), connectPrivateBus(t, address))
	if err != nil || state != SecretServiceOwned {
		t.Fatalf("waitForSecretService = %q, %v; want %q", state, err, SecretServiceOwned)
	}
}

func TestWaitForSecretServiceWaitsForUnlockBeforeReturning(t *testing.T) {
	address := startPrivateBus(t)
	keyring := connectPrivateBus(t, address)
	collection := exportSecretCollection(t, keyring, true)
	claimNamed(t, keyring, SecretServiceBusName)

	client := connectPrivateBus(t, address)

	go func() {
		time.Sleep(100 * time.Millisecond)
		collection.locked.Store(false)

		_ = keyring.Emit(
			dbus.ObjectPath("/org/freedesktop/secrets/collection/kdewallet"),
			dbusPropertiesChangedSignal,
			secretCollectionIface,
			map[string]dbus.Variant{"Locked": dbus.MakeVariant(false)},
			[]string{},
		)
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	state, err := waitForSecretService(ctx, client)
	if err != nil || state != SecretServiceOwned {
		t.Fatalf("waitForSecretService = %q, %v; want %q", state, err, SecretServiceOwned)
	}
}

func TestWaitForSecretServiceReturnsWhenNameIsClaimedAndUnlockedLater(t *testing.T) {
	address := startPrivateBus(t)
	keyring := connectPrivateBus(t, address)
	client := connectPrivateBus(t, address)

	go func() {
		time.Sleep(100 * time.Millisecond)

		collection := &fakeSecretCollection{}
		_ = keyring.Export(
			collection,
			dbus.ObjectPath(secretDefaultCollectionPath),
			dbusPropertiesIface,
		)
		_, _ = keyring.RequestName(SecretServiceBusName, dbus.NameFlagDoNotQueue)
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	state, err := waitForSecretService(ctx, client)
	if err != nil || state != SecretServiceOwned {
		t.Fatalf("waitForSecretService = %q, %v; want %q", state, err, SecretServiceOwned)
	}
}

func TestWaitForSecretServiceTimesOutWhileCollectionStaysLocked(t *testing.T) {
	address := startPrivateBus(t)
	keyring := connectPrivateBus(t, address)
	exportSecretCollection(t, keyring, true)
	claimNamed(t, keyring, SecretServiceBusName)

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	state, err := waitForSecretService(ctx, connectPrivateBus(t, address))
	if err != nil || state != SecretServiceTimedOut {
		t.Fatalf("waitForSecretService = %q, %v; want %q", state, err, SecretServiceTimedOut)
	}
}

func TestWaitForSecretServiceWaitsForLoginDisplayVariable(t *testing.T) {
	address := startPrivateBus(t)
	service := connectPrivateBus(t, address)
	exportSecretCollection(t, service, false)
	claimNamed(t, service, SecretServiceBusName)

	manager := &fakeDynamicUserEnv{env: []string{"HOME=/home/user"}}
	if err := service.Export(
		manager,
		dbus.ObjectPath(systemdObjectPath),
		dbusPropertiesIface,
	); err != nil {
		t.Fatalf("export fake user manager: %v", err)
	}

	claimNamed(t, service, systemdBusName)

	go func() {
		time.Sleep(100 * time.Millisecond)
		manager.set([]string{"HOME=/home/user", "WAYLAND_DISPLAY=wayland-0"})
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	client := connectPrivateBus(t, address)

	state, err := waitForSecretService(ctx, client)
	if err != nil || state != SecretServiceOwned {
		t.Fatalf("waitForSecretService = %q, %v; want %q", state, err, SecretServiceOwned)
	}

	env, err := userManagerEnvironment(ctx, client)
	if err != nil || env["WAYLAND_DISPLAY"] != "wayland-0" {
		t.Fatalf(
			"userManagerEnvironment after wait = %+v, %v; want WAYLAND_DISPLAY=wayland-0",
			env,
			err,
		)
	}
}

func TestWaitForSecretServiceTimesOutWhenNameNeverAppears(t *testing.T) {
	address := startPrivateBus(t)

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	state, err := waitForSecretService(ctx, connectPrivateBus(t, address))
	if err != nil || state != SecretServiceTimedOut {
		t.Fatalf("waitForSecretService = %q, %v; want %q", state, err, SecretServiceTimedOut)
	}
}

func TestWaitForSecretServiceReportsCancellation(t *testing.T) {
	address := startPrivateBus(t)
	client := connectPrivateBus(t, address)

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)

	_, err := waitForSecretService(ctx, client)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForSecretService error = %v, want context.Canceled", err)
	}
}

func TestClassifyDesktopLoginDistinguishesGraphicalLingeringAndHeadless(t *testing.T) {
	const uid = 1000

	cases := []struct {
		name           string
		sessions       map[dbus.ObjectPath]logindSession
		props          map[dbus.ObjectPath]fakeLogindSessionProps
		displayManager string
		want           desktopLoginKind
	}{
		{
			name: "own wayland session",
			sessions: map[dbus.ObjectPath]logindSession{
				"/org/freedesktop/login1/session/_31": {
					ID:   "1",
					UID:  uid,
					User: "user",
					Seat: "seat0",
				},
			},
			props: map[dbus.ObjectPath]fakeLogindSessionProps{
				"/org/freedesktop/login1/session/_31": {
					uid:         uid,
					sessionType: "wayland",
					class:       "user",
				},
			},
			displayManager: "active",
			want:           desktopLoginGraphical,
		},
		{
			name: "lingering manager session behind active display manager",
			sessions: map[dbus.ObjectPath]logindSession{
				"/org/freedesktop/login1/session/_31": {ID: "1", UID: uid, User: "user", Seat: ""},
			},
			props: map[dbus.ObjectPath]fakeLogindSessionProps{
				"/org/freedesktop/login1/session/_31": {
					uid:         uid,
					sessionType: "unspecified",
					class:       "manager",
				},
			},
			displayManager: "active",
			want:           desktopLoginAwaiting,
		},
		{
			name: "lingering manager session with greeter session",
			sessions: map[dbus.ObjectPath]logindSession{
				"/org/freedesktop/login1/session/_31": {ID: "1", UID: uid, User: "user", Seat: ""},
				"/org/freedesktop/login1/session/_c1": {
					ID:   "c1",
					UID:  960,
					User: "sddm",
					Seat: "seat0",
				},
			},
			props: map[dbus.ObjectPath]fakeLogindSessionProps{
				"/org/freedesktop/login1/session/_31": {
					uid:         uid,
					sessionType: "unspecified",
					class:       "manager",
				},
				"/org/freedesktop/login1/session/_c1": {
					uid:         960,
					sessionType: "x11",
					class:       "greeter",
				},
			},
			displayManager: "",
			want:           desktopLoginAwaiting,
		},
		{
			name: "non-interactive ssh session behind active display manager still awaits login",
			sessions: map[dbus.ObjectPath]logindSession{
				"/org/freedesktop/login1/session/_31": {ID: "1", UID: uid, User: "user", Seat: ""},
				"/org/freedesktop/login1/session/_32": {ID: "2", UID: uid, User: "user", Seat: ""},
			},
			props: map[dbus.ObjectPath]fakeLogindSessionProps{
				"/org/freedesktop/login1/session/_31": {
					uid:         uid,
					sessionType: "unspecified",
					class:       "manager",
				},
				"/org/freedesktop/login1/session/_32": {
					uid:         uid,
					sessionType: "unspecified",
					class:       "user",
				},
			},
			displayManager: "active",
			want:           desktopLoginAwaiting,
		},
		{
			name: "lingering manager session on headless host with no display manager",
			sessions: map[dbus.ObjectPath]logindSession{
				"/org/freedesktop/login1/session/_31": {ID: "1", UID: uid, User: "user", Seat: ""},
			},
			props: map[dbus.ObjectPath]fakeLogindSessionProps{
				"/org/freedesktop/login1/session/_31": {
					uid:         uid,
					sessionType: "unspecified",
					class:       "manager",
				},
			},
			displayManager: "",
			want:           desktopLoginNone,
		},
		{
			name: "interactive ssh tty session proceeds without desktop wait",
			sessions: map[dbus.ObjectPath]logindSession{
				"/org/freedesktop/login1/session/_32": {ID: "2", UID: uid, User: "user", Seat: ""},
			},
			props: map[dbus.ObjectPath]fakeLogindSessionProps{
				"/org/freedesktop/login1/session/_32": {
					uid:         uid,
					sessionType: "tty",
					class:       "user",
				},
			},
			displayManager: "active",
			want:           desktopLoginNone,
		},
		{
			name: "another user's x11 session without display manager does not trigger desktop wait",
			sessions: map[dbus.ObjectPath]logindSession{
				"/org/freedesktop/login1/session/_33": {
					ID:   "3",
					UID:  uid + 1,
					User: "other",
					Seat: "seat0",
				},
			},
			props: map[dbus.ObjectPath]fakeLogindSessionProps{
				"/org/freedesktop/login1/session/_33": {
					uid:         uid + 1,
					sessionType: "x11",
					class:       "user",
				},
			},
			displayManager: "",
			want:           desktopLoginNone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			address := startPrivateBus(t)
			service := connectPrivateBus(t, address)

			manager := new(fakeLogindSessions)

			for path, session := range tc.sessions {
				session.Path = path
				manager.sessions = append(manager.sessions, session)

				if err := service.Export(tc.props[path], path, dbusPropertiesIface); err != nil {
					t.Fatalf("export fake logind session: %v", err)
				}
			}

			if err := service.Export(manager, LogindObjectPath, LogindManagerIface); err != nil {
				t.Fatalf("export fake logind manager: %v", err)
			}

			claimNamed(t, service, LogindBusName)

			if tc.displayManager != "" {
				dmPath := dbus.ObjectPath("/org/freedesktop/systemd1/unit/sddm_2eservice")
				dm := &fakeSystemdDisplayManager{unitPath: dmPath, state: tc.displayManager}

				if err := service.Export(
					dm,
					dbus.ObjectPath(systemdObjectPath),
					systemdManagerIface,
				); err != nil {
					t.Fatalf("export fake systemd manager: %v", err)
				}

				if err := service.Export(dm, dmPath, dbusPropertiesIface); err != nil {
					t.Fatalf("export fake display manager unit: %v", err)
				}

				claimNamed(t, service, systemdBusName)
			}

			got, err := classifyDesktopLogin(t.Context(), connectPrivateBus(t, address), uid)
			if err != nil || got != tc.want {
				t.Fatalf("classifyDesktopLogin = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestWatchUserLoginsFiresOnlyForTargetUsersInteractiveSessions(t *testing.T) {
	const uid = 1000

	address := startPrivateBus(t)
	service := connectPrivateBus(t, address)

	sessions := map[dbus.ObjectPath]fakeLogindSessionProps{
		"/org/freedesktop/login1/session/_c1": {uid: 960, sessionType: "x11", class: "greeter"},
		"/org/freedesktop/login1/session/_31": {
			uid:         uid,
			sessionType: "unspecified",
			class:       "user",
		},
		"/org/freedesktop/login1/session/_32": {
			uid:         uid + 1,
			sessionType: "wayland",
			class:       "user",
		},
		"/org/freedesktop/login1/session/_33": {uid: uid, sessionType: "wayland", class: "user"},
	}

	for path, props := range sessions {
		if err := service.Export(props, path, dbusPropertiesIface); err != nil {
			t.Fatalf("export fake session %s: %v", path, err)
		}
	}

	claimNamed(t, service, LogindBusName)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var logins atomic.Int32

	loggedIn := make(chan struct{}, 2)

	watchDone := make(chan error, 1)

	go func() {
		watchDone <- watchUserLogins(ctx, connectPrivateBus(t, address), uid, func(context.Context) {
			logins.Add(1)

			loggedIn <- struct{}{}
		})
	}()

	time.Sleep(100 * time.Millisecond)

	for _, event := range []struct {
		id   string
		path dbus.ObjectPath
	}{
		{"c1", "/org/freedesktop/login1/session/_c1"},
		{"1", "/org/freedesktop/login1/session/_31"},
		{"2", "/org/freedesktop/login1/session/_32"},
		{"3", "/org/freedesktop/login1/session/_33"},
	} {
		if err := service.Emit(
			dbus.ObjectPath(LogindObjectPath),
			logindSessionNewSignal,
			event.id,
			event.path,
		); err != nil {
			t.Fatalf("emit SessionNew %s: %v", event.id, err)
		}
	}

	select {
	case <-loggedIn:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for interactive login callback")
	}

	cancel()

	if err := <-watchDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("watchUserLogins error = %v, want context.Canceled", err)
	}

	if got := logins.Load(); got != 1 {
		t.Fatalf("login callback count = %d, want 1", got)
	}
}

func exportSecretCollection(
	t *testing.T,
	conn *dbus.Conn,
	locked bool,
) *fakeSecretCollection {
	t.Helper()

	collection := &fakeSecretCollection{}
	collection.locked.Store(locked)

	if err := conn.Export(
		collection,
		dbus.ObjectPath(secretDefaultCollectionPath),
		dbusPropertiesIface,
	); err != nil {
		t.Fatalf("export fake secret collection: %v", err)
	}

	return collection
}

func claimNamed(t *testing.T, conn *dbus.Conn, name string) {
	t.Helper()

	reply, err := conn.RequestName(name, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("request bus name %s = %v, %v", name, reply, err)
	}
}
