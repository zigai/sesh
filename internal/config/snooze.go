package config

import (
	"strings"
	"time"
)

const SnoozeNextMessage = "next message"

const (
	snoozeTomorrow  = "tomorrow "
	snoozeClockForm = "15:04"
)

type Snooze struct {
	nextMessage bool
	duration    time.Duration
	atClock     bool
	hour        int
	minute      int
	tomorrow    bool
}

func ParseSnooze(choice string) (Snooze, error) {
	text := strings.TrimSpace(choice)
	if text == SnoozeNextMessage {
		return Snooze{
			nextMessage: true,
			duration:    0,
			atClock:     false,
			hour:        0,
			minute:      0,
			tomorrow:    false,
		}, nil
	}

	rest, tomorrow := strings.CutPrefix(text, snoozeTomorrow)
	if clock, err := time.Parse(snoozeClockForm, strings.TrimSpace(rest)); err == nil {
		return Snooze{
			nextMessage: false,
			duration:    0,
			atClock:     true,
			hour:        clock.Hour(),
			minute:      clock.Minute(),
			tomorrow:    tomorrow,
		}, nil
	}

	duration, err := time.ParseDuration(text)
	if tomorrow || err != nil || duration <= 0 {
		return Snooze{}, errInboxSnoozeInvalid
	}

	return Snooze{
		nextMessage: false,
		duration:    duration,
		atClock:     false,
		hour:        0,
		minute:      0,
		tomorrow:    false,
	}, nil
}

// Until is zero for a snooze that waits for the next message.
func (s Snooze) Until(now time.Time) time.Time {
	switch {
	case s.nextMessage:
		return time.Time{}
	case !s.atClock:
		return now.Add(s.duration)
	}

	year, month, day := now.Date()
	until := time.Date(year, month, day, s.hour, s.minute, 0, 0, now.Location())

	if s.tomorrow || !until.After(now) {
		until = time.Date(year, month, day+1, s.hour, s.minute, 0, 0, now.Location())
	}

	return until
}
