//go:build linux

package lifecycle

import (
	"maps"
	"testing"

	"github.com/godbus/dbus/v5"
)

type fakeUserManager []string

func (f fakeUserManager) Get(iface string, property string) (dbus.Variant, *dbus.Error) {
	if iface != systemdManagerIface || property != "Environment" {
		return dbus.Variant{}, dbus.MakeFailedError(ErrUserManagerEnvironment)
	}

	return dbus.MakeVariant([]string(f)), nil
}

func TestUserManagerEnvironmentReadsTheManagersAssignments(t *testing.T) {
	address := startPrivateBus(t)
	service := connectPrivateBus(t, address)

	manager := fakeUserManager{
		"XAUTHORITY=/run/user/1000/xauth_current",
		"SESSION_MANAGER=local/host:@/tmp/.ICE-unix/1,unix/host:/tmp/.ICE-unix/1",
		"EMPTY=",
	}
	if err := service.Export(
		manager,
		systemdObjectPath,
		"org.freedesktop.DBus.Properties",
	); err != nil {
		t.Fatalf("export fake user manager: %v", err)
	}

	claimNamed(t, service, systemdBusName)

	got, err := userManagerEnvironment(t.Context(), connectPrivateBus(t, address))
	if err != nil {
		t.Fatalf("userManagerEnvironment failed: %v", err)
	}

	want := map[string]string{
		"XAUTHORITY":      "/run/user/1000/xauth_current",
		"SESSION_MANAGER": "local/host:@/tmp/.ICE-unix/1,unix/host:/tmp/.ICE-unix/1",
		"EMPTY":           "",
	}
	if !maps.Equal(got, want) {
		t.Fatalf("userManagerEnvironment = %v, want %v", got, want)
	}
}
