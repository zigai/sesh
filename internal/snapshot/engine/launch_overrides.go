//go:build linux || darwin

package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/zigai/sesh/internal/snapshot/model"
)

var ErrLaunchOverride = errors.New("launch override names no pane of the snapshot")

// LaunchOverrides must decide Override from the manifest alone, so a resumed restore plans the
// same launches. Commit gets every pane to create, after registration and before any launch.
type LaunchOverrides interface {
	Override(ctx context.Context, manifest model.Manifest) (map[string]model.Launch, error)
	Commit(ctx context.Context, panes []string) error
}

type launchOverride struct {
	digest string
}

func overrideLaunches(ctx context.Context, overrides LaunchOverrides, manifest model.Manifest) (model.Manifest, launchOverride, error) {
	none := launchOverride{digest: ""}
	if overrides == nil {
		return manifest, none, nil
	}

	launches, err := overrides.Override(ctx, manifest)
	if err != nil {
		return manifest, none, fmt.Errorf("override launches: %w", err)
	}

	if len(launches) == 0 {
		return manifest, none, nil
	}

	panes := slices.Clone(manifest.Panes)
	replaced := 0

	for i := range panes {
		if launch, ok := launches[panes[i].ID]; ok {
			panes[i].Launch = launch
			replaced++
		}
	}

	if replaced != len(launches) {
		return manifest, none, ErrLaunchOverride
	}

	manifest.Panes = panes

	if err := manifest.Validate(); err != nil {
		return manifest, none, fmt.Errorf("validate overridden launches: %w", err)
	}

	digest, err := launchDigest(launches)
	if err != nil {
		return manifest, none, err
	}

	return manifest, launchOverride{digest: digest}, nil
}

func launchDigest(launches map[string]model.Launch) (string, error) {
	type entry struct {
		Pane   string       `json:"pane"`
		Launch model.Launch `json:"launch"`
	}

	entries := make([]entry, 0, len(launches))
	for _, pane := range slices.Sorted(maps.Keys(launches)) {
		entries = append(entries, entry{Pane: pane, Launch: launches[pane]})
	}

	encoded, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("encode launch overrides: %w", err)
	}

	sum := sha256.Sum256(encoded)

	return hex.EncodeToString(sum[:]), nil
}
