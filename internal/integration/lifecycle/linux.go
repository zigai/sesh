//go:build linux

package lifecycle

import (
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/samber/oops"
)

const (
	LogindDefaultInhibitDelayMax = 5 * time.Second
	LogindShutdownInhibitorWhat  = "shutdown"
	LogindSleepInhibitorWhat     = "sleep"
	LogindInhibitorMode          = "delay"
	LogindInhibitorWho           = "sesh"
	LogindInhibitorWhy           = "run one bounded final tmux snapshot save before shutdown or sleep"
)

const (
	LogindBusName                  = "org.freedesktop.login1"
	LogindObjectPath               = "/org/freedesktop/login1"
	LogindManagerIface             = "org.freedesktop.login1.Manager"
	LogindInhibitMethod            = "Inhibit"
	LogindPrepareForShutdownSignal = "PrepareForShutdown"
	LogindPrepareForSleepSignal    = "PrepareForSleep"
)

type InhibitorSpec struct {
	What string
	Who  string
	Why  string
	Mode string
}

func ShutdownInhibitor() InhibitorSpec {
	return InhibitorSpec{
		What: LogindShutdownInhibitorWhat,
		Who:  LogindInhibitorWho,
		Why:  LogindInhibitorWhy,
		Mode: LogindInhibitorMode,
	}
}

func SleepInhibitor() InhibitorSpec {
	return InhibitorSpec{
		What: LogindSleepInhibitorWhat,
		Who:  LogindInhibitorWho,
		Why:  LogindInhibitorWhy,
		Mode: LogindInhibitorMode,
	}
}

func LifecycleInhibitors() [2]InhibitorSpec {
	return [2]InhibitorSpec{ShutdownInhibitor(), SleepInhibitor()}
}

func SystemdUserServiceDirectory() (string, error) {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", oops.In("lifecycle").Code("directory_resolve_failed").With("operation", "systemd_user_service_directory").Wrap(err)
		}

		configHome = filepath.Join(home, ".config")
	}

	return filepath.Join(configHome, "systemd", "user"), nil
}

func ObserveFacts(inhibitorAvailable bool) Facts {
	manager := "none"

	if _, err := os.Stat("/run/systemd/system"); err == nil {
		manager = "systemd"
	}

	return Facts{
		GOOS:               runtime.GOOS,
		Manager:            manager,
		InhibitorAvailable: inhibitorAvailable,
		ManagedTmuxService: false,
		SameUserManager:    false,
		StopOrderDeclared:  false,
		SaverAfterTmux:     false,
	}
}

func PlatformGaps() []string {
	return []string{
		"the logind adapter requires a reachable system bus and permission to acquire a delay inhibitor",
		"the logind adapter has been exercised through real shutdown, sleep cycles, and OS-forced inhibitor delay expiry in a disposable guest, but deadlines cannot guarantee that all saves fit InhibitDelayMaxUSec",
		"systemd ExecStop covers only the managed unit's own stop work: a failed stop never vetoes the stop transaction",
		"a user-manager teardown, desktop logout or forced shutdown is not covered by unit ordering alone; logind sleep and shutdown integration requires a running sesh daemon run coordinator",
	}
}
