//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"

	"github.com/samber/oops"
	"golang.org/x/sys/unix"
)

const initializationMarker = ".initializing-v1"

func (r *Repository) initializeLocked(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return oops.In("snapshot.store").With("operation", "initialize_repository").Wrap(err)
	}

	catalog, err := r.catalog()
	if err == nil {
		return r.finalizeInitialization(ctx, catalog)
	}

	if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := r.prepareInitialization(); err != nil {
		return err
	}

	if err := r.initBoundary(ctx, "init_recorded"); err != nil {
		return err
	}

	for _, step := range []struct{ name, phase string }{
		{"generations", "init_generations_created"},
		{"staging", "init_staging_created"},
	} {
		dir, err := r.root.Child(step.name, true)
		if err != nil {
			return oops.In("snapshot.store").With("operation", "create_repository_directory").Wrap(err)
		}

		if err := errors.Join(dir.Sync(), dir.Close()); err != nil {
			return err
		}

		if err := r.initBoundary(ctx, step.phase); err != nil {
			return err
		}
	}

	catalog = Catalog{Version: catalogVersion, Sequence: 0, Current: "", Attempts: nil, Generations: nil, Operations: nil, Rewrites: nil}
	if err := r.publish(catalog); err != nil {
		return err
	}

	return r.finalizeInitialization(ctx, catalog)
}

func (r *Repository) prepareInitialization() error {
	names, err := r.root.Names()
	if err != nil {
		return oops.In("snapshot.store").With("operation", "inspect_initialization").Wrap(err)
	}

	if len(names) == 1 && names[0] == "repository.lock" {
		dir, err := r.root.CreateDir(initializationMarker)
		if err != nil {
			return oops.In("snapshot.store").With("operation", "record_initialization").Wrap(err)
		}

		if err := errors.Join(dir.Sync(), dir.Close()); err != nil {
			return err
		}

		names = append(names, initializationMarker)
	}

	if !slices.Contains(names, initializationMarker) {
		return ErrCorrupt
	}

	return r.validateInitialization(names)
}

func (r *Repository) validateInitialization(names []string) error {
	for _, name := range names {
		switch {
		case name == "repository.lock", name == "catalog.json":
		case name == initializationMarker, name == "generations", name == "staging":
			if err := r.verifyEmptyDirectory(name); err != nil {
				return err
			}
		case strings.HasPrefix(name, ".catalog-") && validID(strings.TrimPrefix(name, ".catalog-")):
			file, err := r.root.File(name, unix.O_RDONLY)
			if err != nil {
				return errors.Join(ErrCorrupt, err)
			}

			if err := file.Close(); err != nil {
				return errors.Join(ErrCorrupt, err)
			}
		default:
			return ErrCorrupt
		}
	}

	return nil
}

func (r *Repository) finalizeInitialization(ctx context.Context, catalog Catalog) error {
	names, err := r.root.Names()
	if err != nil {
		return oops.In("snapshot.store").With("operation", "inspect_initialization").Wrap(err)
	}

	hasMarker := slices.Contains(names, initializationMarker)
	if hasMarker {
		if err := r.validatePublishedInitialization(catalog, names); err != nil {
			return err
		}
	}

	if err := r.initBoundary(ctx, "init_catalog_sync"); err != nil {
		return errors.Join(ErrUncertain, err)
	}

	if err := r.root.Sync(); err != nil {
		return errors.Join(ErrUncertain, err)
	}

	if err := r.initBoundary(ctx, "init_catalog_confirmed"); err != nil {
		return errors.Join(ErrUncertain, err)
	}

	if !hasMarker {
		return nil
	}

	return r.removeInitializationMarker(ctx)
}

func (r *Repository) removeInitializationMarker(ctx context.Context) error {
	if err := r.root.RemoveDir(initializationMarker); err != nil {
		return errors.Join(ErrUncertain, err)
	}

	if err := r.initBoundary(ctx, "init_marker_removed"); err != nil {
		return errors.Join(ErrUncertain, err)
	}

	if err := r.root.Sync(); err != nil {
		return errors.Join(ErrUncertain, err)
	}

	if err := r.initBoundary(ctx, "init_finalized"); err != nil {
		return errors.Join(ErrUncertain, err)
	}

	return nil
}

func (r *Repository) validatePublishedInitialization(catalog Catalog, names []string) error {
	if catalog.Sequence != 0 || catalog.Current != "" || len(catalog.Attempts)+len(catalog.Generations)+len(catalog.Operations) != 0 {
		return ErrCorrupt
	}

	if !slices.Contains(names, "generations") || !slices.Contains(names, "staging") {
		return ErrCorrupt
	}

	return r.validateInitialization(names)
}

func (r *Repository) initBoundary(ctx context.Context, phase string) error {
	if err := r.boundary(phase); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return oops.In("snapshot.store").With("operation", "initialize_repository").Wrap(err)
	}

	return nil
}

func (r *Repository) verifyEmptyDirectory(name string) error {
	dir, err := r.root.Child(name, false)
	if err != nil {
		return errors.Join(ErrCorrupt, err)
	}

	names, readErr := dir.Names()

	var stateErr error
	if len(names) != 0 {
		stateErr = ErrCorrupt
	}

	return errors.Join(readErr, stateErr, dir.Close())
}
