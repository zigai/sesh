package model

import (
	"bytes"
	"slices"
	"strings"
)

const (
	scopeGlobalSession = "global-session"
	scopeGlobalWindow  = "global-window"
	scopeSession       = "session"
	scopeWindow        = "window"
)

// CompactInheritedOptions returns a copy without session and window options that tmux reports as
// inherited with the global value; EffectiveSessionOptions and EffectiveWindowOptions restore
// them. User ("@") options are never reported as inherited, so they are kept. Pane lists stay
// complete because they cannot be rebuilt from the window.
func (m Manifest) CompactInheritedOptions() Manifest {
	compact := m

	sessionParent := m.globalOptions(scopeGlobalSession)

	compact.Sessions = make([]Session, len(m.Sessions))
	for index, session := range m.Sessions {
		session.Options = withoutSuppliedOptions(session.Options, sessionParent)
		compact.Sessions[index] = session
	}

	windowParent := m.globalOptions(scopeGlobalWindow)

	compact.Windows = make([]Window, len(m.Windows))
	for index, window := range m.Windows {
		window.Options = withoutSuppliedOptions(window.Options, windowParent)
		compact.Windows[index] = window
	}

	return compact
}

// EffectiveSessionOptions returns the session's options including those it inherits.
func (m Manifest) EffectiveSessionOptions(session Session) []Option {
	return withSuppliedOptions(session.Options, m.globalOptions(scopeGlobalSession), scopeSession)
}

// EffectiveWindowOptions returns the window's options including those it inherits.
func (m Manifest) EffectiveWindowOptions(window Window) []Option {
	return withSuppliedOptions(window.Options, m.globalOptions(scopeGlobalWindow), scopeWindow)
}

// WithInheritedOptions returns a copy whose session and window option lists include every
// inherited option, as tmux reported them.
func (m Manifest) WithInheritedOptions() Manifest {
	expanded := m

	expanded.Sessions = make([]Session, len(m.Sessions))
	for index, session := range m.Sessions {
		session.Options = m.EffectiveSessionOptions(session)
		expanded.Sessions[index] = session
	}

	expanded.Windows = make([]Window, len(m.Windows))
	for index, window := range m.Windows {
		window.Options = m.EffectiveWindowOptions(window)
		expanded.Windows[index] = window
	}

	return expanded
}

func (m Manifest) globalOptions(scope string) map[string]Option {
	options := make(map[string]Option)

	for _, option := range m.Source.Options {
		if option.Scope == scope && !isUserOption(option) {
			options[string(option.Name)] = option
		}
	}

	return options
}

func withoutSuppliedOptions(options []Option, parent map[string]Option) []Option {
	kept := make([]Option, 0, len(options))

	for _, option := range options {
		if supplied, ok := parent[string(option.Name)]; ok && option.Inherited && sameSetting(option, supplied) {
			continue
		}

		kept = append(kept, option)
	}

	return kept
}

// withSuppliedOptions adds the parent options a scope inherits. A local array option, even an
// empty one, replaces the whole parent array, so no parent element of that array is inherited.
func withSuppliedOptions(options []Option, parent map[string]Option, scope string) []Option {
	result := cloneOptions(options)
	local := make(map[string]bool, len(options))

	for _, option := range options {
		local[arrayName(string(option.Name))] = true
	}

	for name, supplied := range parent {
		if local[arrayName(name)] {
			continue
		}

		result = append(result, Option{
			Scope:     scope,
			Name:      cloneBytes(supplied.Name),
			Value:     cloneBytes(supplied.Value),
			Present:   supplied.Present,
			Inherited: true,
		})
	}

	appended := result[len(options):]
	slices.SortFunc(appended, func(left, right Option) int { return bytes.Compare(left.Name, right.Name) })

	return result
}

func arrayName(name string) string {
	base, _, _ := strings.Cut(name, "[")

	return base
}

func sameSetting(option, supplied Option) bool {
	return option.Present == supplied.Present && bytes.Equal(option.Value, supplied.Value)
}

func isUserOption(option Option) bool {
	return bytes.HasPrefix(option.Name, []byte("@"))
}
