package model

const (
	ActionKindCreateSession ActionKind = "create-session"
	ActionKindCreateWindow  ActionKind = "create-window"
	ActionKindCreatePane    ActionKind = "create-pane"
	ActionKindLink          ActionKind = "link"
	ActionKindConfigure     ActionKind = "configure"
	ActionKindLaunch        ActionKind = "launch"
	ActionKindVerify        ActionKind = "verify"

	ActionStatePlanned   ActionState = "planned"
	ActionStateIssued    ActionState = "issued"
	ActionStateNotSent   ActionState = "not-sent"
	ActionStateUnknown   ActionState = "unknown"
	ActionStateConfirmed ActionState = "confirmed"
	ActionStateVerified  ActionState = "verified"
)

type ActionKind string

type ActionState string

func (k ActionKind) Valid() bool {
	switch k {
	case ActionKindCreateSession, ActionKindCreateWindow, ActionKindCreatePane,
		ActionKindLink, ActionKindConfigure, ActionKindLaunch, ActionKindVerify:
		return true
	}

	return false
}

func (s ActionState) Valid() bool {
	switch s {
	case ActionStatePlanned, ActionStateIssued, ActionStateNotSent,
		ActionStateUnknown, ActionStateConfirmed, ActionStateVerified:
		return true
	}

	return false
}

func (s ActionState) CanBecome(next ActionState) bool {
	switch s {
	case ActionStatePlanned:
		return next == ActionStateIssued
	case ActionStateIssued:
		return next == ActionStateNotSent || next == ActionStateUnknown || next == ActionStateConfirmed
	case ActionStateNotSent:
		return next == ActionStatePlanned
	case ActionStateUnknown:
		return next == ActionStateConfirmed
	case ActionStateConfirmed:
		return next == ActionStateVerified
	case ActionStateVerified:
		return false
	}

	return false
}
