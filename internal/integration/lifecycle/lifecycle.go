package lifecycle

import "time"

const (
	EventShutdown  EventKind = "shutdown"
	EventSleep     EventKind = "sleep"
	EventLogout    EventKind = "logout"
	EventTerminate EventKind = "terminate"
)

type EventKind string

type Event struct {
	Kind     EventKind
	Cycle    uint64
	Deadline time.Time
	Canceled bool
}
