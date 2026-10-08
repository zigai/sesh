package lifecycle

import "time"

const (
	EventShutdown EventKind = "shutdown"
	EventSleep    EventKind = "sleep"
)

type EventKind string

type Event struct {
	Kind     EventKind
	Cycle    uint64
	Deadline time.Time
	Canceled bool
}
