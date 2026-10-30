package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

const (
	InboxGroupState   = "state"
	InboxGroupProject = "project"

	InboxReady    = "ready"
	InboxFailed   = "failed"
	InboxApproval = "approval"
	InboxQuestion = "question"
	InboxNeedsYou = "needs_you"
	InboxLimited  = "limited"
	InboxToMerge  = "to_merge"
	InboxWorking  = "working"
	InboxSnoozed  = "snoozed"
	InboxPaused   = "paused"
	InboxSeen     = "seen"
	InboxClosed   = "closed"
	InboxArchived = "archived"

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
	defaultInboxSections  = []string{
		InboxReady, InboxFailed, InboxApproval, InboxQuestion, InboxNeedsYou, InboxLimited, InboxToMerge, InboxWorking, InboxSnoozed, InboxPaused, InboxSeen, InboxClosed,
	}
	inboxSectionNames      = append(slices.Clone(defaultInboxSections), InboxArchived)
	defaultInboxDimmed     = []string{InboxWorking, InboxSnoozed, InboxPaused, InboxClosed, InboxArchived}
	defaultInboxCollapsed  = []string{InboxClosed, InboxArchived}
	defaultInboxSnoozeList = []string{SnoozeNextMessage, "1h", "4h", "tomorrow 09:00"}
)

type InboxConfig struct {
	Group string `toml:"group"`
	// Sections are shown in order; a section left out is hidden.
	Sections []string          `toml:"sections"`
	Labels   map[string]string `toml:"labels"`
	Dim      []string          `toml:"dim"`
	// Collapsed sections show one heading with their row count until it is opened.
	Collapsed []string `toml:"collapsed"`
	Snooze    []string `toml:"snooze"`
	// ClosedShown is how many closed rows a project shows before a row that expands the rest; zero
	// shows them all.
	ClosedShown int `toml:"closed_shown"`
}

func InboxSections() []string {
	return slices.Clone(defaultInboxSections)
}

func DefaultInboxConfig() InboxConfig {
	return InboxConfig{
		Group: InboxGroupState, Sections: InboxSections(), Labels: nil,
		Dim: slices.Clone(defaultInboxDimmed), Collapsed: slices.Clone(defaultInboxCollapsed), Snooze: slices.Clone(defaultInboxSnoozeList), ClosedShown: defaultClosedShown,
	}
}

func (c InboxConfig) Clone() InboxConfig {
	return InboxConfig{
		Group: c.Group, Sections: slices.Clone(c.Sections), Labels: maps.Clone(c.Labels),
		Dim: slices.Clone(c.Dim), Collapsed: slices.Clone(c.Collapsed), Snooze: slices.Clone(c.Snooze), ClosedShown: c.ClosedShown,
	}
}

func (c InboxConfig) Label(section string) string {
	if label := c.Labels[section]; label != "" {
		return label
	}

	heading := strings.ReplaceAll(section, "_", " ")

	return strings.ToUpper(heading[:1]) + heading[1:]
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

	if err := validateInboxSectionList("inbox.collapsed", inbox.Collapsed); err != nil {
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
