//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"io/fs"
	"slices"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/privatefs"
)

// viewReadAttempts bounds retries when a save replaces the catalog, or collection removes a
// generation, while a view reads it.
const viewReadAttempts = 4

// View reads the published catalog without the repository lock or fsync, so display listings
// never wait on a concurrent save. It sees one consistent publication that a crash may still roll
// back, so mutations and recovery decisions must use Open or OpenExisting.
type View struct {
	repository Repository
}

// OpenView opens an existing repository for lock-free display reads. A repository that
// does not exist yet has no saved sessions; its reads report ErrUnavailable.
func OpenView(ctx context.Context, path string) (*View, error) {
	if err := ctx.Err(); err != nil {
		return nil, repositoryError("open_view", err)
	}

	root, err := privatefs.Open(path, false)
	if errors.Is(err, fs.ErrNotExist) {
		return &View{repository: Repository{root: nil, fault: nil}}, nil
	}

	if err != nil {
		return nil, repositoryError("open_view", oops.In("snapshot.store").With("operation", "open_repository_view").Wrapf(err, "open repository view"))
	}

	return &View{repository: Repository{root: root, fault: nil}}, nil
}

func (v *View) Close() error {
	if v == nil || v.repository.root == nil {
		return nil
	}

	return v.repository.Close()
}

func (v *View) ReadCurrentListing(ctx context.Context) (Listing, error) {
	var listing Listing

	err := v.read(ctx, func() error {
		state, readErr := v.repository.readCurrentState()
		if readErr != nil {
			return readErr
		}

		listing, readErr = state.listing()

		return readErr
	})
	if err != nil {
		return Listing{}, repositoryError("view_current_listing", err)
	}

	return listing, nil
}

func (v *View) ReadListing(ctx context.Context, id string) (Listing, error) {
	var listing Listing

	err := v.read(ctx, func() error {
		var readErr error

		listing, readErr = v.repository.listingLocked(ctx, id)

		return readErr
	})
	if err != nil {
		return Listing{}, repositoryError("view_listing", err)
	}

	return listing, nil
}

func (v *View) read(ctx context.Context, read func() error) error {
	if err := ctx.Err(); err != nil {
		return oops.In("snapshot.store").With("operation", "read_repository_view").Wrap(err)
	}

	if v == nil || v.repository.root == nil {
		return ErrUnavailable
	}

	names, err := v.repository.root.NamesLimit(maxEntries)
	if err != nil {
		return oops.In("snapshot.store").With("operation", "inspect_repository_view").Wrap(err)
	}

	// Only a durable open publishes the first catalog and removes the initialization
	// marker, and no save can commit before that, so either state lists nothing.
	if slices.Contains(names, initializationMarker) || !slices.Contains(names, "catalog.json") {
		return ErrUnavailable
	}

	for attempt := 1; ; attempt++ {
		err = read()
		if err == nil || attempt == viewReadAttempts || !replacedDuringRead(err) {
			return err
		}
	}
}

// replacedDuringRead reports a file that a save or collection replaced or removed after the
// view opened it: private reads reject an opened file whose last link is gone.
func replacedDuringRead(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, privatefs.ErrUnsafe)
}
