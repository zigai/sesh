//go:build linux || darwin

package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

type currentSnapshotReader interface {
	Inspect(ctx context.Context) (store.Catalog, error)
	Read(ctx context.Context, generationID string) (model.Manifest, error)
	ReadCurrent(ctx context.Context) (store.Generation, model.Manifest, error)
}

func (s *Service) checkSnapshot(ctx context.Context) (Result, Result, error) {
	if s.snapshotDir == "" {
		repoRes := Result{
			Check:   CheckSnapshotRepository,
			Status:  StatusWarning,
			Reason:  ReasonNotConfigured,
			Message: "snapshot repository path not configured",
			Fix:     "Configure [paths].snapshot_dir or run 'sesh action snapshot-save'",
		}
		curRes := Result{
			Check:   CheckSnapshotCurrent,
			Status:  StatusWarning,
			Reason:  ReasonNotConfigured,
			Message: "snapshot repository path not configured",
			Fix:     "Configure [paths].snapshot_dir or run 'sesh action snapshot-save'",
		}

		return repoRes, curRes, nil
	}

	repo, openErr := store.OpenExisting(ctx, s.snapshotDir)
	if openErr != nil {
		return classifySnapshotOpenError(s.snapshotDir, openErr)
	}

	repoRes, curRes, inspectErr := inspectSnapshot(ctx, repo)

	return repoRes, curRes, errors.Join(inspectErr, repo.Close())
}

func inspectSnapshot(ctx context.Context, repo currentSnapshotReader) (Result, Result, error) {
	catalog, inspectErr := repo.Inspect(ctx)
	if inspectErr != nil {
		if errors.Is(inspectErr, context.Canceled) || errors.Is(inspectErr, context.DeadlineExceeded) {
			return Result{}, Result{}, oops.In("doctor").Code("operation_canceled").With("operation", "inspect_snapshot").Wrap(inspectErr)
		}

		if errors.Is(inspectErr, store.ErrCorrupt) {
			repoRes := Result{
				Check:   CheckSnapshotRepository,
				Status:  StatusError,
				Reason:  ReasonCorrupt,
				Message: "snapshot repository catalog is corrupt",
				Fix:     "Inspect snapshot repository catalog and artifacts",
			}
			curRes := Result{
				Check:   CheckSnapshotCurrent,
				Status:  StatusWarning,
				Reason:  ReasonUnavailable,
				Message: "snapshot repository is corrupt; current snapshot cannot be read",
				Fix:     "Inspect snapshot repository catalog and artifacts",
			}

			return repoRes, curRes, nil
		}

		repoRes := Result{
			Check:   CheckSnapshotRepository,
			Status:  StatusError,
			Reason:  ReasonInspectionFailed,
			Message: "failed to inspect snapshot repository catalog",
			Fix:     "Verify snapshot directory permissions and filesystem state",
		}
		curRes := Result{
			Check:   CheckSnapshotCurrent,
			Status:  StatusWarning,
			Reason:  ReasonUnavailable,
			Message: "snapshot repository inspection failed; current snapshot cannot be read",
			Fix:     "Verify snapshot directory permissions and filesystem state",
		}

		return repoRes, curRes, nil
	}

	repoRes := repositoryResultFromCatalog(catalog)
	curRes, curErr := resolveCurrentSnapshot(ctx, repo)

	return repoRes, curRes, curErr
}

func resolveCurrentSnapshot(ctx context.Context, repo currentSnapshotReader) (Result, error) {
	const stableViewAttempts = 2

	for range stableViewAttempts {
		gen, manifest, readErr := repo.ReadCurrent(ctx)
		if readErr == nil {
			return manifestResult(gen.ID, manifest), nil
		}

		if !errors.Is(readErr, store.ErrRecoveryRequired) {
			return classifyReadCurrentError(readErr)
		}

		before, inspectErr := repo.Inspect(ctx)
		if inspectErr != nil {
			return classifyReadCurrentError(inspectErr)
		}

		candidate, admissible := practicalCurrentGeneration(before)
		if !admissible {
			if latestSaveAttempt(before).State == "failed" {
				return failedSaveResult(), nil
			}

			return classifyReadCurrentError(readErr)
		}

		candidateManifest, candidateErr := repo.Read(ctx, candidate)
		if candidateErr != nil {
			return classifyReadCurrentError(candidateErr)
		}

		after, inspectErr := repo.Inspect(ctx)
		if inspectErr != nil {
			return classifyReadCurrentError(inspectErr)
		}

		afterCandidate, stillAdmissible := practicalCurrentGeneration(after)
		if before.Sequence != after.Sequence || candidate != afterCandidate || !stillAdmissible {
			continue
		}

		return manifestResult(candidate, candidateManifest), nil
	}

	return Result{
		Check:   CheckSnapshotCurrent,
		Status:  StatusWarning,
		Reason:  ReasonUnverifiable,
		Message: "snapshot repository changed during inspection; current snapshot could not be verified",
		Fix:     "Run 'sesh doctor' again after snapshot activity settles",
	}, nil
}

// failedSaveResult reports a recovery barrier left by a save that failed. Repeating the
// same save cannot clear it until the cause the save reports is resolved.
func failedSaveResult() Result {
	return Result{
		Check:   CheckSnapshotCurrent,
		Status:  StatusError,
		Reason:  ReasonRecoveryRequired,
		Message: "current snapshot requires recovery (latest save failed)",
		Fix:     "Run 'sesh action snapshot-save --json' and resolve the reported error code",
	}
}

func repositoryResultFromCatalog(catalog store.Catalog) Result {
	genCount := len(catalog.Generations)
	if genCount == 0 {
		return Result{
			Check:   CheckSnapshotRepository,
			Status:  StatusOK,
			Reason:  "",
			Message: "snapshot repository initialized (empty, 0 generations)",
			Fix:     "",
		}
	}

	return Result{
		Check:   CheckSnapshotRepository,
		Status:  StatusOK,
		Reason:  "",
		Message: fmt.Sprintf("snapshot repository initialized (%d generations)", genCount),
		Fix:     "",
	}
}

func classifySnapshotOpenError(path string, err error) (Result, Result, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Result{}, Result{}, err
	}

	if isUnsafePathError(path, err) {
		repoRes := Result{
			Check:   CheckSnapshotRepository,
			Status:  StatusError,
			Reason:  ReasonUnsafe,
			Message: "snapshot repository path has unsafe permissions or symlinks",
			Fix:     "Ensure snapshot directory permissions are 0700 and not symlinked",
		}
		curRes := Result{
			Check:   CheckSnapshotCurrent,
			Status:  StatusWarning,
			Reason:  ReasonUnavailable,
			Message: "snapshot repository unavailable; current snapshot cannot be read",
			Fix:     "Fix snapshot repository permissions",
		}

		return repoRes, curRes, nil
	}

	if errors.Is(err, store.ErrRecoveryRequired) {
		repoRes := Result{
			Check:   CheckSnapshotRepository,
			Status:  StatusError,
			Reason:  ReasonRecoveryRequired,
			Message: "snapshot repository has an interrupted initialization or pending save barrier",
			Fix:     "Run 'sesh action snapshot-save' to resume or recover repository",
		}
		curRes := Result{
			Check:   CheckSnapshotCurrent,
			Status:  StatusWarning,
			Reason:  ReasonUnavailable,
			Message: "snapshot repository requires recovery; current snapshot cannot be read",
			Fix:     "Run 'sesh action snapshot-save' to resume or recover repository",
		}

		return repoRes, curRes, nil
	}

	if errors.Is(err, store.ErrCorrupt) {
		repoRes := Result{
			Check:   CheckSnapshotRepository,
			Status:  StatusError,
			Reason:  ReasonCorrupt,
			Message: "snapshot repository catalog is corrupt",
			Fix:     "Inspect snapshot repository catalog and artifacts",
		}
		curRes := Result{
			Check:   CheckSnapshotCurrent,
			Status:  StatusWarning,
			Reason:  ReasonUnavailable,
			Message: "snapshot repository is corrupt; current snapshot cannot be read",
			Fix:     "Inspect snapshot repository catalog and artifacts",
		}

		return repoRes, curRes, nil
	}

	if errors.Is(err, os.ErrNotExist) {
		return classifySnapshotNotExist(path)
	}

	repoRes := Result{
		Check:   CheckSnapshotRepository,
		Status:  StatusError,
		Reason:  ReasonInspectionFailed,
		Message: "failed to open snapshot repository",
		Fix:     "Verify snapshot directory permissions and filesystem state",
	}
	curRes := Result{
		Check:   CheckSnapshotCurrent,
		Status:  StatusWarning,
		Reason:  ReasonUnavailable,
		Message: "snapshot repository unavailable; current snapshot cannot be read",
		Fix:     "Verify snapshot directory permissions and filesystem state",
	}

	return repoRes, curRes, nil
}

func classifySnapshotNotExist(path string) (Result, Result, error) {
	_, lstatErr := os.Lstat(path)
	if lstatErr != nil && errors.Is(lstatErr, os.ErrNotExist) {
		repoRes := Result{
			Check:   CheckSnapshotRepository,
			Status:  StatusWarning,
			Reason:  ReasonNotFound,
			Message: "snapshot repository directory does not exist",
			Fix:     "Run 'sesh action snapshot-save' to initialize the repository",
		}
		curRes := Result{
			Check:   CheckSnapshotCurrent,
			Status:  StatusWarning,
			Reason:  ReasonNotFound,
			Message: "no snapshot generations found",
			Fix:     "Run 'sesh action snapshot-save' to capture a snapshot",
		}

		return repoRes, curRes, nil
	}

	markerPath := filepath.Join(path, "init.marker")
	if _, markerErr := os.Lstat(markerPath); markerErr == nil {
		repoRes := Result{
			Check:   CheckSnapshotRepository,
			Status:  StatusError,
			Reason:  ReasonRecoveryRequired,
			Message: "snapshot repository has an interrupted initialization or pending save barrier",
			Fix:     "Run 'sesh action snapshot-save' to resume or recover repository",
		}
		curRes := Result{
			Check:   CheckSnapshotCurrent,
			Status:  StatusWarning,
			Reason:  ReasonUnavailable,
			Message: "snapshot repository requires recovery; current snapshot cannot be read",
			Fix:     "Run 'sesh action snapshot-save' to resume or recover repository",
		}

		return repoRes, curRes, nil
	}

	repoRes := Result{
		Check:   CheckSnapshotRepository,
		Status:  StatusWarning,
		Reason:  ReasonUninitialized,
		Message: "snapshot repository is uninitialized",
		Fix:     "Run 'sesh action snapshot-save' to initialize the repository",
	}
	curRes := Result{
		Check:   CheckSnapshotCurrent,
		Status:  StatusWarning,
		Reason:  ReasonNotFound,
		Message: "no snapshot generations found",
		Fix:     "Run 'sesh action snapshot-save' to capture a snapshot",
	}

	return repoRes, curRes, nil
}

func classifyReadCurrentError(err error) (Result, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Result{}, err
	}

	if errors.Is(err, store.ErrUnavailable) {
		return Result{
			Check:   CheckSnapshotCurrent,
			Status:  StatusWarning,
			Reason:  ReasonNotFound,
			Message: "no snapshot generations found",
			Fix:     "Run 'sesh action snapshot-save' to capture a snapshot",
		}, nil
	}

	if errors.Is(err, store.ErrRecoveryRequired) {
		return Result{
			Check:   CheckSnapshotCurrent,
			Status:  StatusError,
			Reason:  ReasonRecoveryRequired,
			Message: "current snapshot requires recovery (interrupted save or pending attempt barrier)",
			Fix:     "Run 'sesh action snapshot-save' to resolve pending attempt",
		}, nil
	}

	if errors.Is(err, store.ErrCorrupt) {
		return Result{
			Check:   CheckSnapshotCurrent,
			Status:  StatusError,
			Reason:  ReasonCorrupt,
			Message: "current snapshot manifest or artifacts are corrupt",
			Fix:     "Inspect snapshot generation files and artifacts",
		}, nil
	}

	return Result{
		Check:   CheckSnapshotCurrent,
		Status:  StatusError,
		Reason:  ReasonInspectionFailed,
		Message: "failed to read current snapshot",
		Fix:     "Verify snapshot generation files",
	}, nil
}

func manifestResult(genID string, manifest model.Manifest) Result {
	sessionCount := len(manifest.Sessions)

	if manifest.Complete() {
		if sessionCount == 0 {
			return Result{
				Check:   CheckSnapshotCurrent,
				Status:  StatusWarning,
				Reason:  ReasonEmpty,
				Message: fmt.Sprintf("current snapshot %s contains no saved sessions", genID),
				Fix:     "Run 'sesh action snapshot-save' with active sessions",
			}
		}

		return Result{
			Check:   CheckSnapshotCurrent,
			Status:  StatusOK,
			Reason:  "",
			Message: fmt.Sprintf("current snapshot %s verified (%d sessions)", genID, sessionCount),
			Fix:     "",
		}
	}

	if sessionCount == 0 {
		return Result{
			Check:   CheckSnapshotCurrent,
			Status:  StatusWarning,
			Reason:  ReasonEmpty,
			Message: fmt.Sprintf("current snapshot %s contains no saved sessions", genID),
			Fix:     "Run 'sesh action snapshot-save' with active sessions",
		}
	}

	if isObservationalSnapshot(manifest) && manifest.PracticalRecoveryReady() {
		return Result{
			Check:   CheckSnapshotCurrent,
			Status:  StatusWarning,
			Reason:  ReasonObservational,
			Message: fmt.Sprintf("snapshot generation %s was verified for practical recovery (%d sessions); this observational result does not establish current automatic selection or full runtime parity", genID, sessionCount),
			Fix:     "",
		}
	}

	return Result{
		Check:   CheckSnapshotCurrent,
		Status:  StatusWarning,
		Reason:  ReasonIncomplete,
		Message: fmt.Sprintf("current snapshot %s is incomplete/partial (%d sessions)", genID, sessionCount),
		Fix:     "Run 'sesh action snapshot-save' to capture a complete snapshot",
	}
}

func isObservationalSnapshot(m model.Manifest) bool {
	if m.Source.Consistency != "observed" {
		return false
	}

	for _, e := range m.Evidence {
		if e.Status == "observed" {
			return true
		}
	}

	return false
}

func practicalCurrentGeneration(catalog store.Catalog) (string, bool) {
	if catalog.Current == "" {
		return "", false
	}

	for _, rewrite := range catalog.Rewrites {
		if rewrite.State == "pending" && rewrite.Source == catalog.Current {
			return "", false
		}
	}

	latestSave := latestSaveAttempt(catalog)
	if latestSave.State != "incomplete" {
		return "", false
	}

	if currentRewriteDescendant(catalog, latestSave.ID) != catalog.Current {
		return "", false
	}

	for _, gen := range catalog.Generations {
		if gen.ID == catalog.Current {
			return catalog.Current, !gen.Complete
		}
	}

	return "", false
}

func latestSaveAttempt(catalog store.Catalog) store.Attempt {
	replacements := make(map[string]bool, len(catalog.Rewrites))
	for _, rewrite := range catalog.Rewrites {
		if rewrite.Replacement != "" {
			replacements[rewrite.Replacement] = true
		}
	}

	for _, attempt := range slices.Backward(catalog.Attempts) {
		if !replacements[attempt.ID] {
			return attempt
		}
	}

	var absent store.Attempt

	return absent
}

func currentRewriteDescendant(catalog store.Catalog, source string) string {
	for range len(catalog.Rewrites) {
		replacement, ok := completedRewriteReplacement(catalog.Rewrites, source)
		if !ok {
			return source
		}

		source = replacement
	}

	return source
}

func completedRewriteReplacement(rewrites []store.Rewrite, source string) (string, bool) {
	for _, rewrite := range rewrites {
		if rewrite.State == "completed" && rewrite.Source == source && rewrite.Replacement != "" {
			return rewrite.Replacement, true
		}
	}

	return "", false
}

func isUnsafePathError(path string, err error) bool {
	if errors.Is(err, privatefs.ErrUnsafe) || errors.Is(err, syscall.ELOOP) {
		return true
	}

	info, lstatErr := os.Lstat(path)
	if lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return true
	}

	return false
}
