package tui

import (
	"fmt"
	"time"
)

const (
	hoursPerDay          = 24
	secondsPerMinute     = 60
	minutesPerHour       = 60
	secondsPerHour       = 3600
	secondsPerDay        = 86400
	maxStandardClockHour = 100
)

func FormatActivityAt(t time.Time, now time.Time) string {
	diff := now.Sub(t)
	if diff < time.Minute {
		return "now"
	}

	if diff < time.Hour {
		return fmt.Sprintf("%dm", int(diff.Minutes()))
	}

	if diff < hoursPerDay*time.Hour {
		return fmt.Sprintf("%dh", int(diff.Hours()))
	}

	return fmt.Sprintf("%dd", int(diff.Hours()/hoursPerDay))
}

func FormatClockAt(t time.Time, now time.Time) string {
	if t.IsZero() {
		return ""
	}

	diff := max(now.Sub(t), 0)

	totalSeconds := int64(diff / time.Second)
	seconds := totalSeconds % secondsPerMinute
	minutes := (totalSeconds / secondsPerMinute) % minutesPerHour
	hours := totalSeconds / secondsPerHour

	if hours < maxStandardClockHour {
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
	}

	return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
}

func FormatHoursMinutesAt(t time.Time, now time.Time) string {
	if t.IsZero() {
		return ""
	}

	minutes := max(now.Sub(t), 0) / time.Minute

	return fmt.Sprintf("%02d:%02d", minutes/minutesPerHour, minutes%minutesPerHour)
}

func FormatElapsedAt(t time.Time, now time.Time) string {
	if t.IsZero() {
		return ""
	}

	seconds := max(now.Sub(t), 0) / time.Second
	switch {
	case seconds < secondsPerMinute:
		return fmt.Sprintf("%ds", seconds)
	case seconds < secondsPerHour:
		return fmt.Sprintf("%dm", seconds/secondsPerMinute)
	case seconds < secondsPerDay:
		return fmt.Sprintf("%dh %02dm", seconds/secondsPerHour, (seconds/secondsPerMinute)%minutesPerHour)
	default:
		return fmt.Sprintf("%dd %02dh", seconds/secondsPerDay, (seconds/secondsPerHour)%hoursPerDay)
	}
}
