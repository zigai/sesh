//go:build linux || darwin

package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

type currentSnapshotReader interface {
	Inspect(ctx context.Context) (store.Catalog, error)
	Read(ctx context.Context, generationID string) (model.Manifest, error)
	ReadCurrent(ctx context.Context) (store.Generation, model.Manifest, error)
}

func (s *Service) checkSnapshot(ctx context.Context) ([]Result, error) {
	repo, openErr := store.OpenExisting(ctx, s.opts.SnapshotDir)
	if openErr != nil {
		if isCanceled(openErr) {
			return nil, fmt.Errorf("open the snapshot repository: %w", openErr)
		}

		repoRes := s.classifySnapshotOpenError(openErr)
		s.repoStatus = repoRes.Status

		return []Result{repoRes, skipped(CheckSnapshotCurrent, CheckSnapshotRepository, "repository is unusable")}, nil
	}

	repoRes, curRes, inspectErr := s.inspectSnapshot(ctx, repo)
	if err := errors.Join(inspectErr, repo.Close()); err != nil {
		return nil, err
	}

	s.repoStatus = repoRes.Status

	return []Result{repoRes, curRes}, nil
}

func (s *Service) inspectSnapshot(ctx context.Context, repo currentSnapshotReader) (Result, Result, error) {
	catalog, inspectErr := repo.Inspect(ctx)
	if inspectErr != nil {
		if isCanceled(inspectErr) {
			return Result{}, Result{}, fmt.Errorf("inspect the snapshot catalog: %w", inspectErr)
		}

		repoRes := s.verifyFix(newResult(CheckSnapshotRepository, StatusFail, "Catalog is corrupt").withReason(ReasonCorrupt))
		if !errors.Is(inspectErr, store.ErrCorrupt) {
			repoRes = newResult(CheckSnapshotRepository, StatusFail, "Cannot read the catalog: "+inspectErr.Error()).
				withReason(ReasonInspectionFailed).
				withCommand("ls -ld " + shellPath(s.opts.SnapshotDir))
		}

		return repoRes, skipped(CheckSnapshotCurrent, CheckSnapshotRepository, "repository is unusable"), nil
	}

	repoRes := newResult(CheckSnapshotRepository, StatusOK, plural(len(catalog.Generations), "generation"))
	curRes, curErr := s.resolveCurrentSnapshot(ctx, repo)

	return repoRes, curRes, curErr
}

func (s *Service) resolveCurrentSnapshot(ctx context.Context, repo currentSnapshotReader) (Result, error) {
	const stableViewAttempts = 2

	for range stableViewAttempts {
		gen, manifest, readErr := repo.ReadCurrent(ctx)
		if readErr == nil {
			return s.manifestResult(gen.ID, manifest), nil
		}

		if errors.Is(readErr, store.ErrUnavailable) {
			return s.classifyUnavailableCurrent(ctx, repo, readErr)
		}

		if !errors.Is(readErr, store.ErrRecoveryRequired) {
			return s.classifyReadCurrentError(readErr)
		}

		before, inspectErr := repo.Inspect(ctx)
		if inspectErr != nil {
			return s.classifyReadCurrentError(inspectErr)
		}

		if !before.PracticalCurrentAdmissible() {
			return s.classifyReadCurrentError(readErr)
		}

		candidateManifest, candidateErr := repo.Read(ctx, before.Current)
		if candidateErr != nil {
			return s.classifyReadCurrentError(candidateErr)
		}

		after, inspectErr := repo.Inspect(ctx)
		if inspectErr != nil {
			return s.classifyReadCurrentError(inspectErr)
		}

		if before.Sequence != after.Sequence || before.Current != after.Current || !after.PracticalCurrentAdmissible() {
			continue
		}

		return s.practicalCandidateResult(after, candidateManifest), nil
	}

	return newResult(CheckSnapshotCurrent, StatusWarn, "Repository changed during inspection; the current snapshot was not verified").
		withReason(ReasonUnverifiable).
		withCommand(s.doctorCommand()), nil
}

func (s *Service) classifyUnavailableCurrent(ctx context.Context, repo currentSnapshotReader, readErr error) (Result, error) {
	catalog, inspectErr := repo.Inspect(ctx)
	if inspectErr != nil {
		return s.classifyReadCurrentError(inspectErr)
	}

	if catalog.LatestSaveFailed() {
		return s.saveFix(newResult(CheckSnapshotCurrent, StatusFail, "Latest save failed and no earlier generation can be restored").
			withReason(ReasonRecoveryRequired)), nil
	}

	return s.classifyReadCurrentError(readErr)
}

func (s *Service) practicalCandidateResult(catalog store.Catalog, manifest model.Manifest) Result {
	genID := catalog.Current
	if !catalog.LatestSaveUnfinished() || len(manifest.Sessions) == 0 {
		return s.manifestResult(genID, manifest)
	}

	sessions := plural(len(manifest.Sessions), "session")
	if catalog.LatestSaveFailed() {
		return s.saveFix(newResult(CheckSnapshotCurrent, StatusWarn,
			fmt.Sprintf("Latest save failed; restore uses %s, the newest saved generation (%s)", genID, sessions)).
			withReason(ReasonSaveUnfinished))
	}

	return s.saveFix(newResult(CheckSnapshotCurrent, StatusWarn,
		fmt.Sprintf("Latest save did not finish; restore uses %s, the newest saved generation (%s)", genID, sessions)).
		withReason(ReasonSaveUnfinished))
}

func (s *Service) manifestResult(genID string, manifest model.Manifest) Result {
	sessions := plural(len(manifest.Sessions), "session")

	switch {
	case len(manifest.Sessions) == 0:
		return s.saveFix(newResult(CheckSnapshotCurrent, StatusWarn, fmt.Sprintf("Generation %s holds no sessions", genID)).
			withReason(ReasonEmpty))
	case manifest.Complete() || (isObservationalSnapshot(manifest) && manifest.PracticalRecoveryReady()):
		return newResult(CheckSnapshotCurrent, StatusOK, fmt.Sprintf("Generation %s restores %s", genID, sessions))
	default:
		return s.saveFix(newResult(CheckSnapshotCurrent, StatusWarn, fmt.Sprintf("Generation %s is incomplete (%s)", genID, sessions)).
			withReason(ReasonIncomplete))
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

func (s *Service) classifySnapshotOpenError(err error) Result {
	dir := DisplayPath(s.opts.SnapshotDir)

	switch {
	case isUnsafePathError(s.opts.SnapshotDir, err):
		return newResult(CheckSnapshotRepository, StatusFail, dir+" is a symlink or has unsafe permissions").
			withReason(ReasonUnsafe).
			withManual(fmt.Sprintf("Make %s a real directory owned by you with mode 0700 (chmod 700 %s)", dir, shellPath(s.opts.SnapshotDir)))
	case errors.Is(err, store.ErrRecoveryRequired):
		return s.saveFix(newResult(CheckSnapshotRepository, StatusFail, "Initialization or a save was interrupted").
			withReason(ReasonRecoveryRequired))
	case errors.Is(err, store.ErrCorrupt):
		return s.verifyFix(newResult(CheckSnapshotRepository, StatusFail, "Catalog is corrupt").withReason(ReasonCorrupt))
	case errors.Is(err, os.ErrNotExist):
		return s.classifySnapshotNotExist()
	default:
		return newResult(CheckSnapshotRepository, StatusFail, "Cannot open the repository: "+err.Error()).
			withReason(ReasonInspectionFailed).
			withCommand("ls -ld " + shellPath(s.opts.SnapshotDir))
	}
}

func (s *Service) classifySnapshotNotExist() Result {
	if _, err := os.Lstat(s.opts.SnapshotDir); errors.Is(err, os.ErrNotExist) {
		return newResult(CheckSnapshotRepository, StatusInfo, "No snapshots saved yet; the first save creates the repository").
			withReason(ReasonNotFound)
	}

	if _, err := os.Lstat(filepath.Join(s.opts.SnapshotDir, "init.marker")); err == nil {
		return s.saveFix(newResult(CheckSnapshotRepository, StatusFail, "Initialization was interrupted").
			withReason(ReasonRecoveryRequired))
	}

	return s.saveFix(newResult(CheckSnapshotRepository, StatusWarn, "Directory exists but holds no repository; the next save initializes it").
		withReason(ReasonUninitialized))
}

func (s *Service) classifyReadCurrentError(err error) (Result, error) {
	switch {
	case isCanceled(err):
		return Result{}, err
	case errors.Is(err, store.ErrUnavailable):
		return newResult(CheckSnapshotCurrent, StatusInfo, "No generations saved yet").withReason(ReasonNotFound), nil
	case errors.Is(err, store.ErrRecoveryRequired):
		return s.saveFix(newResult(CheckSnapshotCurrent, StatusFail, "A save was interrupted and must finish before restores").
			withReason(ReasonRecoveryRequired)), nil
	case errors.Is(err, model.ErrVersion):
		return s.saveFix(newResult(CheckSnapshotCurrent, StatusFail, "Current generation has an unsupported schema version").
			withReason(ReasonIncompatible)), nil
	case errors.Is(err, store.ErrCorrupt):
		return s.verifyFix(newResult(CheckSnapshotCurrent, StatusFail, "Current generation is corrupt").withReason(ReasonCorrupt)), nil
	default:
		return s.verifyFix(newResult(CheckSnapshotCurrent, StatusFail, "Cannot read the current generation: "+err.Error()).
			withReason(ReasonInspectionFailed)), nil
	}
}

func (s *Service) verifyFix(result Result) Result {
	return result.withCommand(s.seshCommand("snapshot verify", []string{flagRepository}))
}

func isCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func isUnsafePathError(path string, err error) bool {
	if errors.Is(err, privatefs.ErrUnsafe) || errors.Is(err, syscall.ELOOP) {
		return true
	}

	info, lstatErr := os.Lstat(path)

	return lstatErr == nil && info.Mode()&os.ModeSymlink != 0
}
