//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"io/fs"
	"sync"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/privatefs"
)

const viewReadAttempts = 4

// View reads without the repository lock or fsync and may see a publication a crash rolls back;
// mutations and recovery decisions must use Open or OpenExisting.
type View struct {
	repository Repository
}

func OpenView(ctx context.Context, path string) (*View, error) {
	if err := ctx.Err(); err != nil {
		return nil, repositoryError("open_view", err)
	}

	root, err := privatefs.Open(path, false)
	if errors.Is(err, fs.ErrNotExist) {
		return &View{repository: Repository{root: nil, path: path, fault: nil, owners: attemptOwners{mu: sync.Mutex{}, held: nil}}}, nil
	}

	if err != nil {
		return nil, repositoryError("open_view", oops.In("snapshot.store").With("operation", "open_repository_view").Wrapf(err, "open repository view"))
	}

	return &View{repository: Repository{root: root, path: path, fault: nil, owners: attemptOwners{mu: sync.Mutex{}, held: nil}}}, nil
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

	if !initializationFinished(names) {
		return ErrUnavailable
	}

	for attempt := 1; ; attempt++ {
		err = read()
		if err == nil || attempt == viewReadAttempts || !replacedDuringRead(err) {
			return err
		}
	}
}

func replacedDuringRead(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, privatefs.ErrUnlinked)
}
