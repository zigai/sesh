package config

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const sessionPrefixAlt = "alt"

// The picker handles these keys itself, before any action binding.
var (
	PickerSelectKeys    = []string{"enter"}
	PickerCloseKeys     = []string{"esc", "ctrl+c", "ctrl+o"}
	PickerHandoffKeys   = []string{"ctrl+p"}
	PickerStepUpKeys    = []string{"up"}
	PickerStepDownKeys  = []string{"down", "ctrl+n"}
	PickerGroupUpKeys   = []string{"ctrl+up"}
	PickerGroupDownKeys = []string{"ctrl+down"}

	pickerModifiers = []string{"ctrl", "alt", "shift", "meta", "hyper", "super"}
)

// PickerKeyName normalizes a picker key to the form the terminal reports it in, so differently
// written forms of one key compare equal: modifiers in the terminal's order and lower case,
// named keys lower case, and alt with a shifted letter written as the upper case letter.
func PickerKeyName(value string) string {
	held, base := splitPickerModifiers(strings.TrimSpace(value))
	single := utf8.RuneCountInString(base) == 1

	switch {
	case single && len(held) == 2 && held["alt"] && held["shift"]:
		delete(held, "shift")

		base = strings.ToUpper(base)
	case single && (len(held) == 0 || (len(held) == 1 && held["alt"])):
	default:
		base = strings.ToLower(base)
	}

	var name strings.Builder

	for _, modifier := range pickerModifiers {
		if held[modifier] {
			name.WriteString(modifier)
			name.WriteString("+")
		}
	}

	name.WriteString(base)

	return name.String()
}

// PickerReservedKeys returns every key the picker handles before action bindings: its fixed
// keys, the configured navigation keys and the configured session keys.
func PickerReservedKeys(keys KeyConfig) []string {
	reserved := []string{keys.Help, keys.CycleMode, keys.AgentSort, keys.PageUp, keys.PageDown, keys.Top, keys.Bottom}

	if keys.SessionKeysHoldAlt() {
		for _, letter := range keys.Sessions {
			reserved = append(reserved, keys.PickerSessionKey(letter))
		}
	} else {
		reserved = append(reserved, keys.SessionPrefix)
	}

	for _, fixed := range [][]string{
		PickerSelectKeys, PickerCloseKeys, PickerHandoffKeys,
		PickerStepUpKeys, PickerStepDownKeys, PickerGroupUpKeys, PickerGroupDownKeys,
	} {
		reserved = append(reserved, fixed...)
	}

	return reserved
}

// ValidSessionKey reports whether key is a single lowercase ASCII letter.
func ValidSessionKey(key string) bool {
	return len(key) == 1 && key[0] >= 'a' && key[0] <= 'z'
}

// SessionKeysHoldAlt reports whether session letters are typed with alt held instead of after the
// session prefix key.
func (keys KeyConfig) SessionKeysHoldAlt() bool {
	return PickerKeyName(keys.SessionPrefix) == sessionPrefixAlt
}

// PickerSessionKey returns the keys that reach the session assigned letter: alt held with the
// letter, or the prefix key followed by the letter.
func (keys KeyConfig) PickerSessionKey(letter string) string {
	if keys.SessionKeysHoldAlt() {
		return sessionPrefixAlt + "+" + letter
	}

	return PickerKeyName(keys.SessionPrefix) + " " + letter
}

// ValidAgentKey reports whether key is a single lowercase ASCII letter or digit.
func ValidAgentKey(key rune) bool {
	return (key >= 'a' && key <= 'z') || (key >= '0' && key <= '9')
}

// PickerKeyTypes reports whether a normalized picker key types into the search field, so binding
// it would make the character impossible to search for.
func PickerKeyTypes(key string) bool {
	if key == "space" {
		return true
	}

	character, size := utf8.DecodeRuneInString(key)

	return size == len(key) && size > 0 && unicode.IsPrint(character)
}

func splitPickerModifiers(value string) (map[string]bool, string) {
	held := make(map[string]bool, len(pickerModifiers))

	for {
		modifier, rest, found := strings.Cut(value, "+")
		if !found || rest == "" || !slices.ContainsFunc(pickerModifiers, func(name string) bool { return strings.EqualFold(name, modifier) }) {
			return held, value
		}

		held[strings.ToLower(modifier)] = true
		value = rest
	}
}
