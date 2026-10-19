//go:build linux || darwin

package engine

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const restoreTargetBirthPrefix = "birth="

// ActiveRestores lists the restore operations a repository still records as active, the ones that
// hold back automatic saves and startup restores until they finish or are abandoned.
func ActiveRestores(ctx context.Context, repository *store.Repository) ([]OperationStatus, error) {
	if repository == nil {
		return nil, oops.In("snapshot").Code("repository_required").With("operation", "list_active_restores").Wrap(ErrRepositoryRequired)
	}

	catalog, err := repository.Inspect(ctx)
	if err != nil {
		return nil, oops.In("snapshot").Code("inspect_failed").With("operation", "list_active_restores").Wrap(err)
	}

	var active []OperationStatus

	for _, op := range catalog.Operations {
		if op.State == "active" && validateRestoreOperation(op) == nil {
			active = append(active, operationStatus(op))
		}
	}

	return active, nil
}

// RestoreTargetedServer reports whether a restore operation, in any state, was admitted against
// the tmux daemon now serving server. The socket, daemon PID and daemon start time recorded in a
// restore target identify one server instance.
func RestoreTargetedServer(ctx context.Context, repository *store.Repository, server *gotmux.Server) (bool, error) {
	if repository == nil {
		return false, oops.In("snapshot").Code("repository_required").With("operation", "find_server_restore").Wrap(ErrRepositoryRequired)
	}

	live, err := observeRestoreDaemonIdentity(ctx, server)
	if err != nil {
		return false, oops.In("snapshot").Code("daemon_identity_unavailable").With("operation", "find_server_restore").Wrap(err)
	}

	catalog, err := repository.Inspect(ctx)
	if err != nil {
		return false, oops.In("snapshot").Code("inspect_failed").With("operation", "find_server_restore").Wrap(err)
	}

	instance := restoreTargetInstance(server.Endpoint().SocketPath, live.Server)

	for _, op := range catalog.Operations {
		if validateRestoreOperation(op) == nil && strings.HasPrefix(string(op.Target), instance) {
			return true, nil
		}
	}

	return false, nil
}

// SettleDeadRestores abandons every active restore whose target tmux daemon is provably gone:
// the daemon recorded at admission no longer runs, because the machine rebooted or the process
// exited or was replaced. Such a restore can never resume, since resuming requires that exact
// daemon. Abandonment is bookkeeping only: no tmux object is touched and nothing is rolled back.
// A restore whose daemon may still run, or whose daemon cannot be checked, stays active. It
// returns the abandoned operations.
func SettleDeadRestores(ctx context.Context, repository *store.Repository) ([]OperationStatus, error) {
	active, err := ActiveRestores(ctx, repository)
	if err != nil {
		return nil, err
	}

	var settled []OperationStatus

	for _, op := range active {
		if !op.TargetDaemonGone(ctx) {
			continue
		}

		status, abandonErr := abandonRestore(ctx, repository, op.OperationID, "target_daemon_gone")
		if abandonErr != nil {
			return settled, abandonErr
		}

		settled = append(settled, status)
	}

	return settled, nil
}

// TargetDaemonGone reports whether the tmux daemon this restore targeted provably no longer runs,
// so the restore can never resume. SettleDeadRestores abandons exactly these.
func (s OperationStatus) TargetDaemonGone(ctx context.Context) bool {
	return restoreTargetDaemonGone(ctx, s.Target)
}

func restoreTargetDaemonGone(ctx context.Context, target string) bool {
	pid, birth, ok := parseRestoreTargetDaemon(target)
	if !ok {
		return false
	}

	recorded, err := process.ParseIdentity(birth)
	if err != nil {
		return false
	}

	current, err := process.ReadIdentity(ctx, pid)
	if errors.Is(err, process.ErrProcessVanished) || errors.Is(err, process.ErrProcessZombie) {
		return true
	}

	if err != nil {
		return false
	}

	return current != recorded
}

func parseRestoreTargetDaemon(target string) (int, string, bool) {
	rest, ok := strings.CutPrefix(target, restoreTargetPrefix)
	if !ok {
		return 0, "", false
	}

	_, afterEndpoint, _ := strings.Cut(rest, "\n")
	pidLine, _, _ := strings.Cut(afterEndpoint, "\n")

	pid, err := strconv.Atoi(pidLine)
	if err != nil || pid <= 0 {
		return 0, "", false
	}

	for line := range strings.SplitSeq(rest, "\n") {
		if birth, found := strings.CutPrefix(line, restoreTargetBirthPrefix); found {
			return pid, birth, true
		}
	}

	return 0, "", false
}
