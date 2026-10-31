//go:build linux || darwin

package agents

import (
	"context"
	"math"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	"github.com/zigai/aht/v2/pkg/aht"

	"github.com/zigai/sesh/internal/logging"
)

const (
	DefaultInitialBackoff  = 500 * time.Millisecond
	DefaultMaxBackoff      = 5 * time.Second
	DefaultBackoffFactor   = 2.0
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

type watchHealth struct {
	handler      SnapshotHandler
	quiet        bool
	disconnected bool
	degraded     bool
}

func DefaultWatchOptions() WatchOptions {
	return WatchOptions{
		InitialBackoff: DefaultInitialBackoff,
		MaxBackoff:     DefaultMaxBackoff,
		BackoffFactor:  DefaultBackoffFactor,
		Quiet:          false,
	}
}

func (o WatchOptions) normalized() WatchOptions {
	if o.InitialBackoff <= 0 {
		o.InitialBackoff = DefaultInitialBackoff
	}

	if o.MaxBackoff <= 0 {
		o.MaxBackoff = DefaultMaxBackoff
	}

	if math.IsNaN(o.BackoffFactor) || o.BackoffFactor <= 1.0 {
		o.BackoffFactor = DefaultBackoffFactor
	}

	return o
}

func RunWatchLoop(ctx context.Context, source SnapshotSource, handler SnapshotHandler, opts WatchOptions) {
	opts = opts.normalized()
	backoff := opts.InitialBackoff
	health := watchHealth{handler: handler, quiet: opts.Quiet, disconnected: false, degraded: false}

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

		handlerFailed := false
		err := source.Watch(ctx, allSessionsFilter(), func(snapshot aht.StateSnapshot) error {
			backoff = opts.InitialBackoff
			err := health.apply(ctx, snapshot.Sessions)
			handlerFailed = err != nil

			return err
		})

		if ctx.Err() != nil {
			return
		}

		health.disconnect(ctx, err, handlerFailed, backoff)

		timer := time.NewTimer(backoff)

		select {
		case <-ctx.Done():
			timer.Stop()

			return
		case <-timer.C:
			backoff = nextBackoff(backoff, opts.BackoffFactor, opts.MaxBackoff)
		}
	}
}

func (h *watchHealth) apply(ctx context.Context, sessions []aht.Session) error {
	if h.disconnected && !h.quiet {
		zerolog.Ctx(ctx).Info().Str("component", "agents").Str("operation", "watch").
			Str("status", "recovered").Str("reason", "broker_available").Msg("AHT broker watch recovered")
	}

	h.disconnected = false

	err := h.handler.HandleSnapshot(ctx, sessions)
	if err != nil {
		err = oops.In("agents").Code("snapshot_apply_failed").With("operation", "apply_snapshot").Wrap(err)
	}

	if ctx.Err() != nil {
		return err
	}

	if err != nil {
		if !h.degraded {
			zerolog.Ctx(ctx).Warn().Str("component", "agents").Str("operation", "apply_snapshot").
				Str("status", "degraded").Str("reason", "snapshot_apply_failed").Func(logging.Cause(err)).Msg("agent status snapshot could not be applied")
		}

		h.degraded = true

		return err
	}

	if h.degraded && !h.quiet {
		zerolog.Ctx(ctx).Info().Str("component", "agents").Str("operation", "apply_snapshot").
			Str("status", "recovered").Str("reason", "snapshot_applied").Msg("agent status snapshot application recovered")
	}

	h.degraded = false

	return nil
}

func (h *watchHealth) disconnect(ctx context.Context, err error, handlerFailed bool, backoff time.Duration) {
	if err == nil || handlerFailed || h.disconnected {
		return
	}

	h.disconnected = true

	zerolog.Ctx(ctx).Warn().Str("component", "agents").Str("operation", "watch").
		Str("status", "disconnected").Str("reason", "watch_failed").Dur("retry_in", backoff).
		Func(logging.Cause(err)).Msg("AHT broker watch disconnected, will retry")
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
			Func(logging.Cause(err)).Msg("agent window style cleanup failed")

		return oops.In("agents").Code("cleanup_failed").With("operation", "clear_styles").Wrap(err)
	}

	return nil
}

func nextBackoff(current time.Duration, factor float64, limit time.Duration) time.Duration {
	next := float64(current) * factor
	if next >= float64(limit) {
		return limit
	}

	return time.Duration(next)
}
