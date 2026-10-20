//go:build linux || darwin

package app

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	"github.com/zigai/aht/v2/pkg/aht"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/logging"
	"github.com/zigai/sesh/internal/task"
	"github.com/zigai/sesh/internal/work"
)

var errWorkNotFinished = errors.New("the task's conversations were not finished")

var finishableActivities = []string{
	string(aht.ActivityIdle), string(aht.ActivityInterrupted), string(aht.ActivityFailed),
}

// FinishMergedTasksEvery runs until ctx ends. It finishes a merged task only while its agent is
// idle and no client shows its window, and never retries a task the finish refused.
func (local *Local) FinishMergedTasksEvery(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	finishOnTicks(ctx, ticker.C, local.finishMergedTasks)
}

func finishOnTicks(ctx context.Context, ticks <-chan time.Time, scan func(context.Context, map[string]bool) error) {
	refused := make(map[string]bool)

	var episode logging.Episode

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}

		err := scan(ctx, refused)
		if ctx.Err() != nil {
			return
		}

		switch {
		case !episode.Observe(err):
		case err != nil:
			zerolog.Ctx(ctx).Warn().Str("component", "task").Str("operation", "finish_merged_tasks").
				Str("status", "degraded").Str("reason", "task_scan_failed").Func(logging.Cause(err)).Msg("periodic merged task finish failed")
		default:
			zerolog.Ctx(ctx).Info().Str("component", "task").Str("operation", "finish_merged_tasks").
				Str("status", "recovered").Msg("periodic merged task finish recovered")
		}
	}
}

func (local *Local) finishMergedTasks(ctx context.Context, refused map[string]bool) error {
	service, err := local.taskService()
	if err != nil {
		return err
	}

	tasks, err := task.Inventory(ctx, local.server, task.NewStore(local.taskLedger))
	if err != nil {
		return oops.In("app").With("operation", "finish_merged_tasks").Wrap(err)
	}

	shown, err := local.shownWindows(ctx)
	if err != nil {
		return err
	}

	var failed error

	for _, live := range tasks {
		ready, err := local.readyToFinish(ctx, service, live, shown, refused)
		if err != nil {
			failed = errors.Join(failed, err)
		}

		if ready {
			local.finishMergedTask(ctx, service, live.Record.ID, refused)
		}
	}

	if failed != nil {
		return oops.In("app").With("operation", "refresh_task_prs").Wrap(failed)
	}

	return nil
}

func (local *Local) readyToFinish(
	ctx context.Context, service *task.Service, live task.Live, shown map[gotmux.WindowID]bool, refused map[string]bool,
) (bool, error) {
	record := live.Record

	switch {
	case refused[record.ID], record.State != task.StateActive && record.State != task.StateSleeping,
		live.Present && shown[live.WindowID]:
		return false, nil
	case record.PR.State != task.PRMerged:
		refreshed, _, err := service.RefreshPR(ctx, record.ID)
		if err != nil || refreshed.PR.State != task.PRMerged {
			return false, oops.In("app").With("operation", "refresh_task_pr").Wrap(err)
		}
	}

	return !local.agentBusy(ctx, live), nil
}

func (local *Local) finishMergedTask(ctx context.Context, service *task.Service, id string, refused map[string]bool) {
	record, err := service.Finish(ctx, id, task.FinishOptions{Discard: false, SkipRun: false})
	if ctx.Err() != nil {
		return
	}

	if err == nil {
		err = local.finishTaskWork(ctx, record, work.FinishedByPR)
		if err != nil {
			err = errors.Join(errWorkNotFinished, err)
		}
	} else {
		refused[id] = true
	}

	logMergedTaskFinish(ctx, id, err)
}

func logMergedTaskFinish(ctx context.Context, id string, err error) {
	logger := zerolog.Ctx(ctx)

	if err == nil {
		logger.Info().Str("component", "task").Str("operation", "finish_merged_task").Str("task_id", id).Str("status", "ok").
			Msg("finished a task whose pull request merged")

		return
	}

	reason := "finish_failed"

	switch {
	case errors.Is(err, errWorkNotFinished):
		reason = "work_not_finished"
	case errors.Is(err, task.ErrWouldLoseWork):
		reason = "would_lose_work"
	case errors.Is(err, task.ErrFinishRun):
		reason = "finish_run_failed"
	}

	logger.Warn().Str("component", "task").Str("operation", "finish_merged_task").Str("task_id", id).Str("status", "degraded").
		Str("reason", reason).Func(logging.Cause(err)).Msg("task whose pull request merged was not finished")
}

func (local *Local) shownWindows(ctx context.Context) (map[gotmux.WindowID]bool, error) {
	clients, err := local.server.ClientsWith(ctx, gotmux.QueryOptions{Filter: "", ExtraFields: []string{"window_id"}})
	if err != nil {
		return nil, oops.In("app").With("operation", "list_clients").Wrap(err)
	}

	shown := make(map[gotmux.WindowID]bool, len(clients))

	for _, client := range clients {
		if window, ok := client.Raw("window_id"); ok && !client.Control {
			shown[gotmux.WindowID(window)] = true
		}
	}

	return shown, nil
}

func (local *Local) agentBusy(ctx context.Context, live task.Live) bool {
	if !live.Present || live.AgentPane == "" {
		return false
	}

	finder := paneAgents{source: local.agentSource, socket: agents.NormalizeServerSocket(local.server.Endpoint().SocketPath)}

	found, err := finder.AgentsIn(ctx, live.AgentPane)
	if err != nil {
		// An agent whose activity cannot be read may be working.
		return true
	}

	return slices.ContainsFunc(found, func(agent task.Agent) bool { return !slices.Contains(finishableActivities, agent.Activity) })
}
