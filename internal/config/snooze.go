package config

import (
	"strings"
	"time"
)

// SnoozeNextMessage is the snooze choice that lasts until the agent's next message.
const SnoozeNextMessage = "next message"

const (
	snoozeTomorrow  = "tomorrow "
	snoozeClockForm = "15:04"
)

// Snooze is a parsed snooze choice: until the agent's next message, for a duration, or until a
// time of day, today or tomorrow.
type Snooze struct {
	nextMessage bool
	duration    time.Duration
	atClock     bool
	clock       time.Duration
	tomorrow    bool
}

// ParseSnooze reads "next message", a positive duration such as "90m", a time of day such as
// "18:00" (today, or tomorrow once it has passed), or "tomorrow 09:00".
func ParseSnooze(choice string) (Snooze, error) {
	text := strings.TrimSpace(choice)
	if text == SnoozeNextMessage {
		return Snooze{nextMessage: true, duration: 0, atClock: false, clock: 0, tomorrow: false}, nil
	}

	rest, tomorrow := strings.CutPrefix(text, snoozeTomorrow)
	if clock, err := time.Parse(snoozeClockForm, strings.TrimSpace(rest)); err == nil {
		offset := time.Duration(clock.Hour())*time.Hour + time.Duration(clock.Minute())*time.Minute

		return Snooze{nextMessage: false, duration: 0, atClock: true, clock: offset, tomorrow: tomorrow}, nil
	}

	duration, err := time.ParseDuration(text)
	if tomorrow || err != nil || duration <= 0 {
		return Snooze{}, errInboxSnoozeInvalid
	}

	return Snooze{nextMessage: false, duration: duration, atClock: false, clock: 0, tomorrow: false}, nil
}

// Until is when a snooze set at now ends; the zero time for a snooze that waits for the next
// message.
func (s Snooze) Until(now time.Time) time.Time {
	switch {
	case s.nextMessage:
		return time.Time{}
	case !s.atClock:
		return now.Add(s.duration)
	}

	year, month, day := now.Date()
	until := time.Date(year, month, day, 0, 0, 0, 0, now.Location()).Add(s.clock)

	if s.tomorrow || !until.After(now) {
		until = until.AddDate(0, 0, 1)
	}

	return until
}
