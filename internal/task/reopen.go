package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
)

var (
	ErrTaskFinishing        = errors.New("task is being finished")
	ErrTaskStalled          = errors.New("task stopped part way; finish it")
	ErrTaskDirectoryMissing = errors.New("task directory no longer exists")
)

func (s *Service) Reopen(ctx context.Context, place Place, id string) (Record, error) {
	record, err := s.record(ctx, id)
	if err != nil {
		return record, taskError("reopen_task", err)
	}

	located, err := locateTasks(ctx, s.server)
	if _, present := located[id]; err != nil || present {
		return record, taskError("reopen_task", err)
	}

	if err := reopenable(ctx, record); err != nil {
		return record, taskError("reopen_task", err)
	}

	launch, err := s.reopenLaunch(record)
	if err != nil {
		return record, taskError("reopen_task", err)
	}

	if err := s.openTaskWindow(ctx, place, record, launch, "", place.Project == ""); err != nil {
		return record, taskError("reopen_task", err)
	}

	record.State = StateActive

	return record, taskError("reopen_task", s.put(context.WithoutCancel(ctx), record))
}

func reopenable(ctx context.Context, record Record) error {
	switch {
	case Stalled(ctx, record):
		return ErrTaskStalled
	case record.State == StateCreating:
		return ErrTaskStarting
	case record.State == StateFinishing:
		return ErrTaskFinishing
	}

	if _, err := os.Stat(record.Directory); err != nil {
		return fmt.Errorf("%w: %w", ErrTaskDirectoryMissing, err)
	}

	return nil
}

func (s *Service) reopenLaunch(record Record) ([]string, error) {
	if (record.State == StateSleeping || record.State == StateWaking) && len(record.Resume.Command) > 0 {
		return slices.Clone(record.Resume.Command), nil
	}

	offered, err := s.harness(record.Harness)
	if err != nil {
		return nil, err
	}

	return slices.Clone(offered.Command), nil
}
