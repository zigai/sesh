//go:build linux || darwin

package process

import (
	"bytes"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func selectEnvironment(data []byte, keys []string) ([]model.Variable, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	requested, wanted := deduplicateKeys(keys)

	values, err := parseEnvironEntries(data, wanted)
	if err != nil {
		return nil, err
	}

	result := make([]model.Variable, len(requested))
	for i, key := range requested {
		if val, ok := values[key]; ok {
			result[i] = model.Variable{
				Name:   model.Bytes(bytes.Clone([]byte(key))),
				Value:  model.Bytes(val),
				State:  "set",
				Hidden: false,
			}
		} else {
			result[i] = model.Variable{
				Name:   model.Bytes(bytes.Clone([]byte(key))),
				Value:  nil,
				State:  "absent",
				Hidden: false,
			}
		}
	}

	return result, nil
}

func deduplicateKeys(keys []string) ([]string, map[string]struct{}) {
	requested := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))

	for _, key := range keys {
		if _, exists := seen[key]; !exists {
			seen[key] = struct{}{}
			requested = append(requested, key)
		}
	}

	return requested, seen
}

func parseEnvironEntries(data []byte, wanted map[string]struct{}) (map[string][]byte, error) {
	if len(data) == 0 {
		return make(map[string][]byte), nil
	}

	if data[len(data)-1] != 0 {
		return nil, ErrMalformedEnviron
	}

	entries := bytes.Split(data[:len(data)-1], []byte{0})
	values := make(map[string][]byte, len(wanted))

	for _, entry := range entries {
		nameBytes, valBytes, ok := bytes.Cut(entry, []byte("="))
		if !ok || len(nameBytes) == 0 {
			continue
		}

		name := string(nameBytes)
		if _, isWanted := wanted[name]; isWanted {
			if _, alreadyFound := values[name]; alreadyFound {
				return nil, ErrDuplicateEnvironmentVariable
			}

			values[name] = bytes.Clone(valBytes)
		}
	}

	return values, nil
}
