//go:build linux || darwin

package agents

import (
	"context"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	"github.com/zigai/aht/pkg/aht"
)

const (
	DefaultInitialBackoff = 500 * time.Millisecond

	DefaultMaxBackoff = 5 * time.Second

	DefaultBackoffFactor = 2.0

	DefaultShutdownTimeout = 2 * time.Second
)

type SnapshotHandler interface {
	HandleSnapshot(ctx context.Context, sessions []aht.Session) error

	ClearAll(ctx context.Context) error
}

type WatchOptions struct {
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	BackoffFactor  float64
	Quiet          bool
}

func DefaultWatchOptions() WatchOptions {
	return WatchOptions{
		InitialBackoff: DefaultInitialBackoff,
		MaxBackoff:     DefaultMaxBackoff,
		BackoffFactor:  DefaultBackoffFactor,
		Quiet:          false,
	}
}

func RunWatchLoop(ctx context.Context, source SnapshotSource, handler SnapshotHandler, opts WatchOptions) {
	initialBackoff := opts.InitialBackoff
	if initialBackoff <= 0 {
		initialBackoff = DefaultInitialBackoff
	}

	maxBackoff := opts.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = DefaultMaxBackoff
	}

	backoffFactor := opts.BackoffFactor
	if backoffFactor <= 1.0 {
		backoffFactor = DefaultBackoffFactor
	}

	backoff := initialBackoff

	if !opts.Quiet {
		zerolog.Ctx(ctx).Info().
			Str("component", "agents").
			Str("operation", "watch_loop").
			Str("status", "started").
			Msg("agent status watch loop started")
	}

	for {
		if ctx.Err() != nil {
			return
		}

		err := source.Watch(ctx, allSessionsFilter(), func(snapshot aht.StateSnapshot) error {
			backoff = initialBackoff

			return handler.HandleSnapshot(ctx, snapshot.Sessions)
		})

		if ctx.Err() != nil {
			return
		}

		if err != nil {
			zerolog.Ctx(ctx).Warn().
				Str("component", "agents").
				Str("operation", "watch").
				Str("status", "disconnected").
				Str("reason", "watch_failed").
				Dur("retry_in", backoff).
				Msg("AHT broker watch disconnected, will retry")
		}

		timer := time.NewTimer(backoff)

		select {
		case <-ctx.Done():
			timer.Stop()

			return
		case <-timer.C:
			backoff = min(time.Duration(float64(backoff)*backoffFactor), maxBackoff)
		}
	}
}

func RunStatusTracker(ctx context.Context, source SnapshotSource, handler SnapshotHandler, opts WatchOptions, clearOnShutdown bool) error {
	RunWatchLoop(ctx, source, handler, opts)

	if !clearOnShutdown {
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), DefaultShutdownTimeout)
	defer cancel()

	if err := handler.ClearAll(shutdownCtx); err != nil {
		zerolog.Ctx(shutdownCtx).Warn().
			Str("component", "agents").
			Str("operation", "clear_styles").
			Str("status", "uncertain").
			Str("reason", "cleanup_failed").
			Msg("agent window style cleanup failed")

		return oops.In("agents").Code("cleanup_failed").With("operation", "clear_styles").Wrap(err)
	}

	return nil
}
