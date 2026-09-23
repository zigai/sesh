//go:build linux || darwin

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/samber/oops"
	"github.com/zigai/aht/pkg/aht"
	"github.com/zigai/aht/pkg/registry"
)

type SessionSource interface {
	List(ctx context.Context, filter aht.Filter) ([]aht.Session, error)
}

type SnapshotSource interface {
	SessionSource

	Watch(ctx context.Context, filter aht.Filter, yield func(aht.StateSnapshot) error) error
}

type AHTStore struct {
	client *aht.Client
}

func NewAHTStore(statePath string) *AHTStore {
	return &AHTStore{client: aht.New(aht.Config{StorePath: statePath, SocketPath: "", Mode: aht.ModeAuto})}
}

func (s *AHTStore) StorePath() string {
	if s == nil || s.client == nil {
		return ""
	}

	return s.client.StorePath()
}

func (s *AHTStore) SocketPath() string {
	if s == nil || s.client == nil {
		return ""
	}

	return s.client.SocketPath()
}

func (s *AHTStore) List(ctx context.Context, filter aht.Filter) ([]aht.Session, error) {
	if s == nil || s.client == nil {
		return nil, oops.In("agents").Code("registry_unavailable").With("operation", "list_sessions").Wrap(ErrRegistryUnavailable)
	}

	if err := ctx.Err(); err != nil {
		return nil, oops.In("agents").Code("operation_canceled").With("operation", "list_sessions").Wrap(err)
	}

	sessions, err := s.client.List(ctx, filter)
	if err != nil {
		return nil, classifyRegistryError(err, "list_sessions")
	}

	return sessions, nil
}

func (s *AHTStore) Watch(ctx context.Context, filter aht.Filter, yield func(aht.StateSnapshot) error) error {
	if s == nil || s.client == nil {
		return oops.In("agents").Code("registry_unavailable").With("operation", "watch_sessions").Wrap(ErrRegistryUnavailable)
	}

	if err := s.client.Watch(ctx, filter, yield); err != nil {
		return classifyRegistryError(err, "watch_sessions")
	}

	return nil
}

func liveSessionsFilter() aht.Filter {
	var filter aht.Filter

	filter.Presence = aht.PresenceLive

	return filter
}

func allSessionsFilter() aht.Filter {
	var filter aht.Filter

	return filter
}

func classifyRegistryError(err error, operation string) error {
	if err == nil {
		return nil
	}

	builder := oops.In("agents").With("operation", operation)

	if _, ok := errors.AsType[*registry.UnsupportedSchemaError](err); ok {
		return builder.Code("unsupported_schema").Wrap(errors.Join(ErrRegistrySchema, err))
	}

	_, syntaxErr := errors.AsType[*json.SyntaxError](err)
	_, typeErr := errors.AsType[*json.UnmarshalTypeError](err)

	if syntaxErr || typeErr || errors.Is(err, io.ErrUnexpectedEOF) {
		return builder.Code("corrupt_state").Wrap(errors.Join(ErrRegistryCorrupt, err))
	}

	switch {
	case errors.Is(err, aht.ErrUnavailable):
		return builder.Code("registry_unavailable").Wrap(errors.Join(ErrRegistryUnavailable, err))
	case errors.Is(err, registry.ErrCorruptStore):
		return builder.Code("corrupt_state").Wrap(errors.Join(ErrRegistryCorrupt, err))
	case errors.Is(err, registry.ErrStoreTooLarge):
		return builder.Code("state_too_large").Wrap(errors.Join(ErrRegistryTooLarge, err))
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return builder.Code("operation_canceled").Wrap(err)
	default:
		return builder.Code("read_failed").Wrap(err)
	}
}
