//go:build linux || darwin

package remote

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"time"

	"github.com/zigai/sesh/internal/config"
)

const configWatchMinInterval = 250 * time.Millisecond

var errNilConfigLoader = errors.New("remote config loader is not configured")

type configGeneration struct {
	content  []byte
	errClass string
}

type configWatchState struct {
	previous    configGeneration
	hasPrevious bool
	pending     configGeneration
	hasPending  bool
	seenFile    bool
}

type ConfigReload struct {
	Remote config.RemoteConfig
	Err    error
}

func WatchRemoteConfig(
	ctx context.Context,
	path string,
	interval time.Duration,
	load func([]byte) (config.RemoteConfig, error),
) (<-chan ConfigReload, error) {
	if load == nil {
		return nil, errNilConfigLoader
	}

	reloads := make(chan ConfigReload, 1)

	go runConfigWatch(ctx, path, max(interval, configWatchMinInterval), load, reloads)

	return reloads, nil
}

func runConfigWatch(
	ctx context.Context,
	path string,
	interval time.Duration,
	load func([]byte) (config.RemoteConfig, error),
	reloads chan ConfigReload,
) {
	defer close(reloads)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	state := configWatchState{
		previous: configGeneration{content: nil, errClass: ""}, hasPrevious: false,
		pending: configGeneration{content: nil, errClass: ""}, hasPending: false, seenFile: false,
	}

	deliver := func() {
		content, err := config.ReadSourceFile(path)

		reload, changed := state.next(content, err, load)
		if !changed {
			return
		}

		enqueueReload(ctx, reloads, reload)
	}

	deliver()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			deliver()
		}
	}
}

func (state *configWatchState) next(
	content []byte,
	readErr error,
	load func([]byte) (config.RemoteConfig, error),
) (ConfigReload, bool) {
	var noReload ConfigReload

	if readErr != nil {
		state.hasPending = false

		generation := configGeneration{content: nil, errClass: configWatchErrorClass(readErr)}
		if state.hasPrevious && generation.equal(state.previous) {
			return noReload, false
		}

		state.previous = generation
		state.hasPrevious = true

		if !state.seenFile && errors.Is(readErr, fs.ErrNotExist) {
			return noReload, false
		}

		var emptyRemote config.RemoteConfig

		return ConfigReload{Remote: emptyRemote, Err: readErr}, true
	}

	generation := configGeneration{content: content, errClass: ""}
	if state.hasPrevious && generation.equal(state.previous) {
		return noReload, false
	}

	if !state.hasPending || !generation.equal(state.pending) {
		state.pending = generation
		state.hasPending = true

		return noReload, false
	}

	state.previous = generation
	state.hasPrevious = true
	state.hasPending = false
	state.seenFile = true

	remoteConfig, loadErr := load(content)

	return ConfigReload{Remote: remoteConfig, Err: loadErr}, true
}

func (generation configGeneration) equal(other configGeneration) bool {
	return generation.errClass == other.errClass && bytes.Equal(generation.content, other.content)
}

func configWatchErrorClass(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "not_found"
	case errors.Is(err, fs.ErrPermission):
		return "permission"
	default:
		return "unreadable"
	}
}

func enqueueReload(ctx context.Context, reloads chan ConfigReload, reload ConfigReload) {
	select {
	case reloads <- reload:
		return
	default:
	}

	select {
	case <-reloads:
	default:
	}

	select {
	case reloads <- reload:
	case <-ctx.Done():
	}
}
