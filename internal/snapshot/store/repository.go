//go:build linux || darwin

package store

import (
	"context"
	"encoding/json/v2"
	"errors"
	"slices"

	"github.com/samber/oops"
	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/strictjson"
)

type Repository struct {
	root  *privatefs.Dir
	fault func(string) error
}

func Open(ctx context.Context, path string) (*Repository, error) {
	return openWithFault(ctx, path, nil)
}

func OpenExisting(ctx context.Context, path string) (*Repository, error) {
	if err := ctx.Err(); err != nil {
		return nil, repositoryError("open_existing", err)
	}

	root, err := privatefs.Open(path, false)
	if err != nil {
		return nil, repositoryError("open_existing", err)
	}

	repository := &Repository{root: root, fault: nil}
	if err := repository.inspectExisting(ctx); err != nil {
		return nil, repositoryError("open_existing", errors.Join(err, root.Close()))
	}

	return repository, nil
}

func openWithFault(ctx context.Context, path string, fault func(string) error) (*Repository, error) {
	root, err := privatefs.Ensure(path)
	if err != nil {
		return nil, repositoryError("open", oops.In("snapshot.store").With("operation", "open_repository").Wrapf(err, "open repository"))
	}

	repository := &Repository{root: root, fault: fault}
	if err := repository.initialize(ctx); err != nil {
		return nil, repositoryError("open", errors.Join(err, root.Close()))
	}

	return repository, nil
}

func (r *Repository) Close() error {
	if err := r.root.Close(); err != nil {
		return repositoryError("close", oops.In("snapshot.store").With("operation", "close_repository").Wrapf(err, "close repository"))
	}

	return nil
}

func (r *Repository) Inspect(ctx context.Context) (Catalog, error) {
	lock, err := r.root.Lock(ctx, "repository.lock")
	if err != nil {
		return Catalog{}, repositoryError("inspect", oops.In("snapshot.store").With("operation", "lock_catalog_inspection").Wrapf(err, "lock catalog inspection"))
	}

	catalog, readErr := r.catalog()

	return catalog, repositoryError("inspect", errors.Join(readErr, lock.Close()))
}

func (r *Repository) Begin(ctx context.Context) (Attempt, error) {
	attempt := Attempt{ID: newID(), Sequence: 0, State: "pending", Reason: "capture_pending", ManifestSHA256: ""}
	err := r.update(ctx, func(c *Catalog) error {
		if c.Sequence == ^uint64(0) {
			return ErrConflict
		}

		c.Sequence++
		attempt.Sequence = c.Sequence
		c.Attempts = append(c.Attempts, attempt)

		return nil
	})

	return attempt, repositoryError("begin", err)
}

func (r *Repository) Fail(ctx context.Context, id string) error {
	err := r.update(ctx, func(c *Catalog) error {
		index := c.attemptIndex(id)
		if index < 0 {
			return ErrUnavailable
		}

		if c.Attempts[index].State != "pending" {
			return ErrConflict
		}

		c.Attempts[index].State, c.Attempts[index].Reason = "failed", "save_failed"

		return nil
	})

	return repositoryError("fail", err)
}

func (r *Repository) initialize(ctx context.Context) error {
	lock, err := r.root.Lock(ctx, "repository.lock")
	if err != nil {
		return oops.In("snapshot.store").With("operation", "lock_catalog_update").Wrapf(err, "lock catalog update")
	}

	err = r.initializeLocked(ctx)

	return errors.Join(err, lock.Close())
}

func (r *Repository) inspectExisting(ctx context.Context) error {
	lock, err := r.root.LockExisting(ctx, "repository.lock")
	if err != nil {
		return oops.In("snapshot.store").With("operation", "lock_existing_repository").Wrap(err)
	}

	err = r.validateExisting()

	return errors.Join(err, lock.Close())
}

func (r *Repository) validateExisting() error {
	names, err := r.root.NamesLimit(maxEntries)
	if err != nil {
		return oops.In("snapshot.store").With("operation", "inspect_existing_repository").Wrap(err)
	}

	if slices.Contains(names, initializationMarker) {
		return ErrRecoveryRequired
	}

	if _, err := r.catalog(); err != nil {
		return err
	}

	if err := r.root.Sync(); err != nil {
		return errors.Join(ErrUncertain, err)
	}

	return nil
}

func (r *Repository) update(ctx context.Context, change func(*Catalog) error) error {
	lock, err := r.root.Lock(ctx, "repository.lock")
	if err != nil {
		return oops.In("snapshot.store").With("operation", "lock_catalog_update").Wrapf(err, "lock catalog update")
	}

	err = r.change(ctx, change)

	return errors.Join(err, lock.Close())
}

func (r *Repository) change(ctx context.Context, change func(*Catalog) error) error {
	catalog, err := r.catalog()
	if err != nil {
		return err
	}

	if err := change(&catalog); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return oops.In("snapshot.store").With("operation", "publish_catalog").Wrapf(err, "publish catalog")
	}

	return r.publish(ctx, catalog, nil)
}

func (r *Repository) catalog() (Catalog, error) {
	data, err := r.root.Read("catalog.json", maxCatalogBytes)
	if err != nil {
		return Catalog{}, oops.In("snapshot.store").With("operation", "read_catalog").Wrapf(err, "read catalog")
	}

	catalog, err := strictjson.Decode[Catalog](data)
	if err != nil {
		return Catalog{}, errors.Join(ErrCorrupt, err)
	}

	if err := catalog.validate(); err != nil {
		return Catalog{}, err
	}

	return catalog, nil
}

func (r *Repository) publish(ctx context.Context, catalog Catalog, committed *Listing) error {
	data, err := encodeCatalog(catalog)
	if err != nil {
		return err
	}

	current, err := r.stageCurrentState(ctx, catalog, committed)
	if err != nil {
		return err
	}

	name := ".catalog-" + newID()
	if err := writeFile(r.root, name, data); err != nil {
		return err
	}

	if err := r.boundary("catalog_synced"); err != nil {
		return err
	}

	if err := r.root.Rename(name, r.root, "catalog.json"); err != nil {
		return oops.In("snapshot.store").With("operation", "publish_catalog").Wrapf(err, "publish catalog")
	}

	if err := r.boundary("catalog_replaced"); err != nil {
		return errors.Join(ErrUncertain, err)
	}

	if current != "" {
		if err := r.root.Rename(current, r.root, currentStateName); err != nil {
			return errors.Join(ErrUncertain, oops.In("snapshot.store").With("operation", "publish_current_state").Wrapf(err, "publish current state"))
		}

		if err := r.boundary("current_replaced"); err != nil {
			return errors.Join(ErrUncertain, err)
		}
	}

	if err := r.root.Sync(); err != nil {
		return errors.Join(ErrUncertain, err)
	}

	return nil
}

func (r *Repository) boundary(phase string) error {
	if r.fault != nil {
		return r.fault(phase)
	}

	return nil
}

func writeFile(dir *privatefs.Dir, name string, data []byte) error {
	file, err := dir.File(name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return oops.In("snapshot.store").With("operation", "create_repository_file").Wrapf(err, "create repository file")
	}

	_, writeErr := file.Write(data)

	return errors.Join(writeErr, file.Sync(), file.Close())
}

func encodeCatalog(catalog Catalog) ([]byte, error) {
	if err := catalog.validate(); err != nil {
		return nil, err
	}

	data, err := json.Marshal(catalog)
	if err != nil {
		return nil, oops.In("snapshot.store").With("operation", "encode_catalog").Wrapf(err, "encode catalog")
	}

	if len(data) > maxCatalogBytes {
		return nil, ErrCorrupt
	}

	return data, nil
}
