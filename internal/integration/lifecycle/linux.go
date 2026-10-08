//go:build linux

package lifecycle

const (
	LogindShutdownInhibitorWhat = "shutdown"
	LogindSleepInhibitorWhat    = "sleep"
	LogindInhibitorMode         = "delay"
	LogindInhibitorWho          = "sesh"
	LogindInhibitorWhy          = "run one bounded final tmux snapshot save before shutdown or sleep"
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

func Inhibitors() [2]InhibitorSpec {
	return [2]InhibitorSpec{ShutdownInhibitor(), SleepInhibitor()}
}
