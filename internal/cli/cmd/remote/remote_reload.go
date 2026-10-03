//go:build linux || darwin

package remote

import (
	"bytes"
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/remote"
)

const remoteConfigReloadInterval = 250 * time.Millisecond

type remoteMonitorReloads struct {
	configs <-chan config.RemoteConfig
	cancel  context.CancelFunc
	done    <-chan struct{}
	once    sync.Once
}

func startRemoteMonitorReloads(ctx context.Context, cmd *cobra.Command) (*remoteMonitorReloads, error) {
	path, load, enabled, err := remoteMonitorReloadSource(cmd)
	if err != nil {
		return nil, err
	}

	reloads := &remoteMonitorReloads{configs: nil, cancel: nil, done: nil, once: sync.Once{}}
	if !enabled {
		return reloads, nil
	}

	watchContext, cancel := context.WithCancel(ctx)

	observed, err := remote.WatchRemoteConfig(watchContext, path, remoteConfigReloadInterval, load)
	if err != nil {
		cancel()

		return nil, remoteCLIError("watch_remote_config", err)
	}

	configs := make(chan config.RemoteConfig, 1)
	done := make(chan struct{})

	reloads.configs = configs
	reloads.cancel = cancel
	reloads.done = done

	go bridgeRemoteMonitorReloads(watchContext, observed, configs, done)

	return reloads, nil
}

func remoteMonitorReloadSource(cmd *cobra.Command) (string, func([]byte) (config.RemoteConfig, error), bool, error) {
	noConfig, err := cmd.Flags().GetBool("no-config")
	if err != nil {
		return "", nil, false, remoteCLIError("read_no_config_flag", err)
	}

	configuredPath, err := cmd.Flags().GetString("config")
	if err != nil {
		return "", nil, false, remoteCLIError("read_config_flag", err)
	}

	if noConfig || configuredPath == "-" {
		return "", nil, false, nil
	}

	if configuredPath == "" {
		return config.DefaultConfigPath(), loadRemoteConfigBytes, true, nil
	}

	path, err := filepath.Abs(filepath.Clean(configuredPath))
	if err != nil {
		return "", nil, false, remoteCLIError("resolve_remote_config_path", err)
	}

	return path, loadRemoteConfigBytes, true, nil
}

func loadRemoteConfigBytes(content []byte) (config.RemoteConfig, error) {
	cfg, err := config.Load(config.WithExplicitPath("-"), config.WithInput(bytes.NewReader(content)))
	if err != nil {
		return config.RemoteConfig{}, remoteCLIError("reload_remote_config", err)
	}

	return cfg.Remote, nil
}

func bridgeRemoteMonitorReloads(
	ctx context.Context,
	observed <-chan remote.ConfigReload,
	configs chan config.RemoteConfig,
	done chan struct{},
) {
	defer close(done)
	defer close(configs)

	failed := false

	for reload := range observed {
		if reload.Err != nil {
			if !failed {
				zerolog.Ctx(ctx).Warn().
					Str("component", "remote_monitor").
					Str("operation", "reload_config").
					Str("status", "degraded").
					Str("reason", "invalid_configuration").
					Msg("remote monitor configuration reload rejected")
			}

			failed = true

			continue
		}

		if failed {
			zerolog.Ctx(ctx).Info().Str("component", "remote_monitor").Str("operation", "reload_config").
				Str("status", "recovered").Str("reason", "configuration_valid").Msg("remote monitor configuration reload recovered")
		}

		failed = false

		enqueueRemoteConfig(ctx, configs, reload.Remote)
	}
}

func enqueueRemoteConfig(ctx context.Context, configs chan config.RemoteConfig, remoteConfig config.RemoteConfig) {
	select {
	case configs <- remoteConfig:
		return
	default:
	}

	select {
	case <-configs:
	default:
	}

	select {
	case configs <- remoteConfig:
	case <-ctx.Done():
	}
}

func (reloads *remoteMonitorReloads) close() {
	if reloads == nil {
		return
	}

	reloads.once.Do(func() {
		if reloads.cancel != nil {
			reloads.cancel()
		}

		if reloads.done != nil {
			<-reloads.done
		}
	})
}
