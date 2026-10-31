//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/privatefs"
)

const (
	attemptOwnersDir       = "attempts"
	attemptOwnerSuffix     = ".lock"
	attemptReasonAbandoned = "abandoned"
)

// attemptOwners holds one flock per pending attempt this process is saving. A pending attempt
// whose lock file nobody holds belongs to a save process that died.
type attemptOwners struct {
	mu   sync.Mutex
	held map[string]*os.File
}

// Release ends this process's ownership of a save attempt. A still-pending attempt is then
// treated as abandoned by the next save or saved-session deletion.
func (r *Repository) Release(id string) error {
	r.owners.mu.Lock()
	file := r.owners.held[id]
	delete(r.owners.held, id)
	r.owners.mu.Unlock()

	if file == nil {
		return nil
	}

	if err := releaseAttemptOwner(r.root, id, file); err != nil {
		return oops.In("snapshot.store").With("operation", "release_attempt", "attempt", id).Wrap(err)
	}

	return nil
}

// SettleAbandonedAttempts marks pending attempts whose save process is gone as failed and
// returns the resulting catalog, so its remaining pending attempts are saves still in flight.
func (r *Repository) SettleAbandonedAttempts(ctx context.Context) (Catalog, error) {
	lock, err := r.root.Lock(ctx, "repository.lock")
	if err != nil {
		return Catalog{}, repositoryError("settle_attempts", oops.In("snapshot.store").With("operation", "lock_catalog_update").Wrapf(err, "lock catalog update"))
	}

	catalog, abandoned, settleErr := r.settleLocked(ctx)
	if err := errors.Join(settleErr, lock.Close()); err != nil {
		return Catalog{}, repositoryError("settle_attempts", err)
	}

	logAbandonedAttempts(ctx, abandoned)

	return catalog, nil
}

func (r *Repository) settleLocked(ctx context.Context) (Catalog, []string, error) {
	catalog, err := r.catalog()
	if err != nil {
		return Catalog{}, nil, err
	}

	abandoned, err := r.resolveAbandoned(&catalog)
	if err != nil || len(abandoned) == 0 {
		return catalog, nil, err
	}

	if err := ctx.Err(); err != nil {
		return Catalog{}, nil, oops.In("snapshot.store").With("operation", "publish_catalog").Wrapf(err, "publish catalog")
	}

	if err := r.publish(ctx, catalog, nil); err != nil {
		return Catalog{}, nil, err
	}

	return catalog, abandoned, nil
}

// resolveAbandoned must run under repository.lock: an owner creates its lock file under the same
// lock before publishing the pending attempt, so an unheld or missing lock file means a dead owner.
func (r *Repository) resolveAbandoned(catalog *Catalog) ([]string, error) {
	dir, err := r.root.Child(attemptOwnersDir, true)
	if err != nil {
		return nil, oops.In("snapshot.store").With("operation", "open_attempt_owners").Wrap(err)
	}

	abandoned, resolveErr := r.markAbandoned(dir, catalog)
	if resolveErr == nil {
		resolveErr = r.sweepAttemptOwners(dir, *catalog)
	}

	return abandoned, errors.Join(resolveErr, dir.Close())
}

func (r *Repository) markAbandoned(dir *privatefs.Dir, catalog *Catalog) ([]string, error) {
	var abandoned []string

	for index, attempt := range catalog.Attempts {
		if attempt.State != attemptStatePending || r.ownsAttempt(attempt.ID) {
			continue
		}

		held, err := attemptOwnerHeld(dir, attempt.ID)
		if err != nil {
			return nil, err
		}

		if held {
			continue
		}

		catalog.Attempts[index].State, catalog.Attempts[index].Reason = attemptStateFailed, attemptReasonAbandoned

		abandoned = append(abandoned, attempt.ID)
	}

	return abandoned, nil
}

func (r *Repository) sweepAttemptOwners(dir *privatefs.Dir, catalog Catalog) error {
	names, err := dir.NamesLimit(maxEntries)
	if err != nil {
		return oops.In("snapshot.store").With("operation", "list_attempt_owners").Wrap(err)
	}

	for _, name := range names {
		id, ok := strings.CutSuffix(name, attemptOwnerSuffix)
		if !ok || r.ownsAttempt(id) {
			continue
		}

		if index := catalog.attemptIndex(id); index >= 0 && catalog.Attempts[index].State == attemptStatePending {
			continue
		}

		if err := removeUnheldAttemptOwner(dir, name); err != nil {
			return err
		}
	}

	return nil
}

func (r *Repository) holdAttempt(id string) error {
	dir, err := r.root.Child(attemptOwnersDir, true)
	if err != nil {
		return oops.In("snapshot.store").With("operation", "open_attempt_owners").Wrap(err)
	}

	file, err := dir.File(id+attemptOwnerSuffix, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return errors.Join(oops.In("snapshot.store").With("operation", "create_attempt_owner").Wrap(err), dir.Close())
	}

	if err := flockAttemptOwner(file); err != nil {
		return errors.Join(
			oops.In("snapshot.store").With("operation", "lock_attempt_owner").Wrap(err),
			file.Close(),
			dir.Remove(id+attemptOwnerSuffix),
			dir.Close(),
		)
	}

	r.owners.mu.Lock()
	if r.owners.held == nil {
		r.owners.held = make(map[string]*os.File)
	}

	r.owners.held[id] = file
	r.owners.mu.Unlock()

	if err := dir.Close(); err != nil {
		return oops.In("snapshot.store").With("operation", "close_attempt_owners").Wrap(err)
	}

	return nil
}

func (r *Repository) ownsAttempt(id string) bool {
	r.owners.mu.Lock()
	defer r.owners.mu.Unlock()

	return r.owners.held[id] != nil
}

func (r *Repository) releaseAttempts() error {
	r.owners.mu.Lock()
	held := r.owners.held
	r.owners.held = nil
	r.owners.mu.Unlock()

	var err error
	for id, file := range held {
		err = errors.Join(err, releaseAttemptOwner(r.root, id, file))
	}

	return err
}

func releaseAttemptOwner(root *privatefs.Dir, id string, file *os.File) error {
	dir, err := root.Child(attemptOwnersDir, false)
	if err != nil {
		return errors.Join(err, file.Close())
	}

	removeErr := dir.Remove(id + attemptOwnerSuffix)
	if errors.Is(removeErr, unix.ENOENT) {
		removeErr = nil
	}

	return errors.Join(removeErr, file.Close(), dir.Close())
}

func attemptOwnerHeld(dir *privatefs.Dir, id string) (bool, error) {
	file, err := dir.File(id+attemptOwnerSuffix, unix.O_RDWR)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}

	if err != nil {
		return false, oops.In("snapshot.store").With("operation", "open_attempt_owner", "attempt", id).Wrap(err)
	}

	lockErr := flockAttemptOwner(file)
	closeErr := file.Close()

	if errors.Is(lockErr, unix.EWOULDBLOCK) {
		if closeErr != nil {
			return true, oops.In("snapshot.store").With("operation", "probe_attempt_owner", "attempt", id).Wrap(closeErr)
		}

		return true, nil
	}

	if err := errors.Join(lockErr, closeErr); err != nil {
		return false, oops.In("snapshot.store").With("operation", "probe_attempt_owner", "attempt", id).Wrap(err)
	}

	return false, nil
}

func removeUnheldAttemptOwner(dir *privatefs.Dir, name string) error {
	file, err := dir.File(name, unix.O_RDWR)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}

	if err != nil {
		return oops.In("snapshot.store").With("operation", "open_attempt_owner").Wrap(err)
	}

	lockErr := flockAttemptOwner(file)
	if errors.Is(lockErr, unix.EWOULDBLOCK) {
		if err := file.Close(); err != nil {
			return oops.In("snapshot.store").With("operation", "close_attempt_owner").Wrap(err)
		}

		return nil
	}

	removeErr := dir.Remove(name)
	if errors.Is(removeErr, unix.ENOENT) {
		removeErr = nil
	}

	if err := errors.Join(lockErr, removeErr, file.Close()); err != nil {
		return oops.In("snapshot.store").With("operation", "remove_attempt_owner").Wrap(err)
	}

	return nil
}

func flockAttemptOwner(file *os.File) error {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if !errors.Is(err, unix.EINTR) {
			return err //nolint:wrapcheck // callers wrap with the attempt's operation context.
		}
	}
}

func logAbandonedAttempts(ctx context.Context, ids []string) {
	if len(ids) == 0 {
		return
	}

	zerolog.Ctx(ctx).Warn().
		Str("component", "snapshot").
		Str("operation", "settle_attempts").
		Strs("attempts", ids).
		Msg("marked abandoned save attempts as failed")
}
