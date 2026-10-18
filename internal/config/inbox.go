package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
)

const (
	InboxGroupState   = "state"
	InboxGroupProject = "project"

	InboxDone     = "done"
	InboxApprove  = "approve"
	InboxInput    = "input"
	InboxWaiting  = "waiting"
	InboxLimited  = "limited"
	InboxUnmerged = "unmerged"
	InboxRunning  = "running"
	InboxSnoozed  = "snoozed"
	InboxAsleep   = "asleep"
	InboxIdle     = "idle"
	InboxClosed   = "closed"

	defaultClosedShown = 3
)

var (
	errInboxGroup         = errors.New(`inbox.group must be "state" or "project"`)
	errInboxSection       = errors.New("unknown inbox section")
	errInboxSectionTwice  = errors.New("inbox section is listed twice")
	errInboxSectionsEmpty = errors.New("inbox.sections must list at least one section")
	errInboxLabelInvalid  = errors.New("inbox section label must be non-empty text")
	errInboxSnoozeEmpty   = errors.New("inbox.snooze must list at least one choice")
	errInboxSnoozeTwice   = errors.New("inbox snooze choice is listed twice")
	errInboxSnoozeInvalid = errors.New("inbox snooze choice is not a duration, a time of day or next message")
	errInboxClosedShown   = errors.New("inbox.closed_shown must not be negative")
	inboxSectionNames     = []string{
		InboxDone, InboxApprove, InboxInput, InboxWaiting, InboxLimited, InboxUnmerged, InboxRunning, InboxSnoozed, InboxAsleep, InboxIdle, InboxClosed,
	}
	defaultInboxDimmed     = []string{InboxRunning, InboxSnoozed, InboxAsleep, InboxClosed}
	defaultInboxSnoozeList = []string{SnoozeNextMessage, "1h", "4h", "tomorrow 09:00"}
)

// InboxConfig configures the inbox view. Sections lists the shown sections in order; a section
// left out is hidden. Labels renames sections, Dim lists the sections whose rows are dimmed, and
// Snooze lists the choices the snooze action offers. ClosedShown is how many closed rows a project
// shows before a row that expands the rest; zero shows them all.
type InboxConfig struct {
	Group       string            `toml:"group"`
	Sections    []string          `toml:"sections"`
	Labels      map[string]string `toml:"labels"`
	Dim         []string          `toml:"dim"`
	Snooze      []string          `toml:"snooze"`
	ClosedShown int               `toml:"closed_shown"`
}

// InboxSections returns every inbox section name in the default order.
func InboxSections() []string {
	return slices.Clone(inboxSectionNames)
}

func DefaultInboxConfig() InboxConfig {
	return InboxConfig{
		Group: InboxGroupState, Sections: InboxSections(), Labels: nil,
		Dim: slices.Clone(defaultInboxDimmed), Snooze: slices.Clone(defaultInboxSnoozeList), ClosedShown: defaultClosedShown,
	}
}

func (c InboxConfig) Clone() InboxConfig {
	return InboxConfig{
		Group: c.Group, Sections: slices.Clone(c.Sections), Labels: maps.Clone(c.Labels),
		Dim: slices.Clone(c.Dim), Snooze: slices.Clone(c.Snooze), ClosedShown: c.ClosedShown,
	}
}

// Label is the heading of a section: its configured label, else its name.
func (c InboxConfig) Label(section string) string {
	if label := c.Labels[section]; label != "" {
		return label
	}

	return section
}

func ValidateInboxConfig(inbox InboxConfig) error {
	if inbox.Group != InboxGroupState && inbox.Group != InboxGroupProject {
		return errInboxGroup
	}

	if len(inbox.Sections) == 0 {
		return errInboxSectionsEmpty
	}

	if inbox.ClosedShown < 0 {
		return errInboxClosedShown
	}

	if err := validateInboxSectionList("inbox.sections", inbox.Sections); err != nil {
		return err
	}

	if err := validateInboxSectionList("inbox.dim", inbox.Dim); err != nil {
		return err
	}

	for _, section := range slices.Sorted(maps.Keys(inbox.Labels)) {
		if !slices.Contains(inboxSectionNames, section) {
			return fmt.Errorf("invalid inbox.labels.%s: %w", section, errInboxSection)
		}

		if label := inbox.Labels[section]; label == "" || containsControlCharacter(label) {
			return fmt.Errorf("invalid inbox.labels.%s: %w", section, errInboxLabelInvalid)
		}
	}

	return validateSnoozeChoices(inbox.Snooze)
}

func validateInboxSectionList(name string, sections []string) error {
	seen := make(map[string]bool, len(sections))

	for _, section := range sections {
		if !slices.Contains(inboxSectionNames, section) {
			return fmt.Errorf("invalid %s %q: %w", name, section, errInboxSection)
		}

		if seen[section] {
			return fmt.Errorf("invalid %s %q: %w", name, section, errInboxSectionTwice)
		}

		seen[section] = true
	}

	return nil
}

func validateSnoozeChoices(choices []string) error {
	if len(choices) == 0 {
		return errInboxSnoozeEmpty
	}

	seen := make(map[string]bool, len(choices))

	for _, choice := range choices {
		if _, err := ParseSnooze(choice); err != nil {
			return fmt.Errorf("invalid inbox.snooze %q: %w", choice, err)
		}

		if seen[choice] {
			return fmt.Errorf("invalid inbox.snooze %q: %w", choice, errInboxSnoozeTwice)
		}

		seen[choice] = true
	}

	return nil
}
