package config

import (
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	pickerSlotCount    = 10
	pickerSlotKeyCount = 3
)

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

// PickerSlotKeys returns the keys that open the visible row of one slot: the configured key,
// then alt and ctrl with the slot's digit.
func PickerSlotKeys(configured, digit string) []string {
	keys := make([]string, 0, pickerSlotKeyCount)
	if configured = strings.TrimSpace(configured); configured != "" {
		keys = append(keys, configured)
	}

	for _, key := range []string{"alt+" + digit, "ctrl+" + digit} {
		if !strings.EqualFold(key, configured) {
			keys = append(keys, key)
		}
	}

	return keys
}

// PickerReservedKeys returns every key the picker handles before action bindings: its fixed
// keys, the configured navigation keys, the slot keys and the configured session keys.
func PickerReservedKeys(keys KeyConfig) []string {
	reserved := []string{keys.Help, keys.CycleMode, keys.AgentSort, keys.PageUp, keys.PageDown, keys.Top, keys.Bottom}

	for _, letter := range keys.Sessions {
		reserved = append(reserved, PickerSessionKey(letter))
	}

	for _, fixed := range [][]string{
		PickerSelectKeys, PickerCloseKeys, PickerHandoffKeys,
		PickerStepUpKeys, PickerStepDownKeys, PickerGroupUpKeys, PickerGroupDownKeys,
	} {
		reserved = append(reserved, fixed...)
	}

	for index, configured := range pickerSlotConfig(keys) {
		reserved = append(reserved, PickerSlotKeys(configured, strconv.Itoa((index+1)%pickerSlotCount))...)
	}

	return reserved
}

// ValidSessionKey reports whether key is a single lowercase ASCII letter.
func ValidSessionKey(key string) bool {
	return len(key) == 1 && key[0] >= 'a' && key[0] <= 'z'
}

// PickerSessionKey returns the picker key that opens the session assigned letter.
func PickerSessionKey(letter string) string {
	return "alt+" + letter
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

func pickerSlotConfig(keys KeyConfig) []string {
	return []string{
		keys.Select1, keys.Select2, keys.Select3, keys.Select4, keys.Select5,
		keys.Select6, keys.Select7, keys.Select8, keys.Select9, keys.Select0,
	}
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
