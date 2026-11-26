//go:build linux

package lifecycle

import (
	"context"
	"errors"
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

type fakeLogindSessionType string

func (f fakeLogindSessionType) Get(iface string, property string) (dbus.Variant, *dbus.Error) {
	if iface != logindSessionIface || property != "Type" {
		return dbus.Variant{}, dbus.MakeFailedError(ErrLogindSignal)
	}

	return dbus.MakeVariant(string(f)), nil
}

func TestWaitForSecretServiceReturnsWhenNameIsAlreadyOwned(t *testing.T) {
	address := startPrivateBus(t)
	claimNamed(t, connectPrivateBus(t, address), SecretServiceBusName)

	state, err := waitForSecretService(t.Context(), connectPrivateBus(t, address))
	if err != nil || state != SecretServiceOwned {
		t.Fatalf("waitForSecretService = %q, %v; want %q", state, err, SecretServiceOwned)
	}
}

func TestWaitForSecretServiceReturnsWhenNameIsClaimedLater(t *testing.T) {
	address := startPrivateBus(t)
	keyring := connectPrivateBus(t, address)
	client := connectPrivateBus(t, address)

	go func() {
		time.Sleep(100 * time.Millisecond)

		_, _ = keyring.RequestName(SecretServiceBusName, dbus.NameFlagDoNotQueue)
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	state, err := waitForSecretService(ctx, client)
	if err != nil || state != SecretServiceOwned {
		t.Fatalf("waitForSecretService = %q, %v; want %q", state, err, SecretServiceOwned)
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

func TestUserHasGraphicalSessionCountsOnlyTheUsersGraphicalSessions(t *testing.T) {
	const uid = 1000

	cases := []struct {
		name     string
		sessions map[dbus.ObjectPath]logindSession
		types    map[dbus.ObjectPath]string
		want     bool
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
			types: map[dbus.ObjectPath]string{"/org/freedesktop/login1/session/_31": "wayland"},
			want:  true,
		},
		{
			name: "own ssh session only",
			sessions: map[dbus.ObjectPath]logindSession{
				"/org/freedesktop/login1/session/_32": {ID: "2", UID: uid, User: "user", Seat: ""},
			},
			types: map[dbus.ObjectPath]string{"/org/freedesktop/login1/session/_32": "tty"},
			want:  false,
		},
		{
			name: "another user's x11 session",
			sessions: map[dbus.ObjectPath]logindSession{
				"/org/freedesktop/login1/session/_33": {
					ID:   "3",
					UID:  uid + 1,
					User: "other",
					Seat: "seat0",
				},
			},
			types: map[dbus.ObjectPath]string{"/org/freedesktop/login1/session/_33": "x11"},
			want:  false,
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

				if err := service.Export(
					fakeLogindSessionType(tc.types[path]), path, "org.freedesktop.DBus.Properties",
				); err != nil {
					t.Fatalf("export fake logind session: %v", err)
				}
			}

			if err := service.Export(manager, LogindObjectPath, LogindManagerIface); err != nil {
				t.Fatalf("export fake logind manager: %v", err)
			}

			claimNamed(t, service, LogindBusName)

			got, err := userHasGraphicalSession(t.Context(), connectPrivateBus(t, address), uid)
			if err != nil || got != tc.want {
				t.Fatalf("userHasGraphicalSession = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func claimNamed(t *testing.T, conn *dbus.Conn, name string) {
	t.Helper()

	reply, err := conn.RequestName(name, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("request bus name %s = %v, %v", name, reply, err)
	}
}
