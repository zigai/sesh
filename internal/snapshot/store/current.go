//go:build linux || darwin

package store

import (
	"context"
	"encoding/json/v2"
	"errors"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/strictjson"
)

const (
	currentStateName     = "current.json"
	currentStatePrefix   = ".current-"
	currentStateVersion  = 1
	maxCurrentStateBytes = 16 << 20
)

const (
	currentStatusAvailable        = "available"
	currentStatusUnavailable      = "unavailable"
	currentStatusRecoveryRequired = "recovery_required"
	currentStatusUnreadable       = "unreadable"
)

var noGeneration = Generation{ID: "", Sequence: 0, ManifestSHA256: "", Complete: false}

type Listing struct {
	Generation Generation      `json:"generation"`
	Started    int64           `json:"started"`
	Finished   int64           `json:"finished"`
	Sessions   []ListedSession `json:"sessions"`
}

type ListedSession struct {
	ID        string      `json:"id"`
	Name      model.Bytes `json:"name"`
	Directory model.Bytes `json:"directory"`
	Windows   int         `json:"windows"`
}

type currentState struct {
	Version int     `json:"version"`
	Status  string  `json:"status"`
	Listing Listing `json:"listing"`
}

func listingFromManifest(generation Generation, manifest model.Manifest) Listing {
	windows := make(map[string]int, len(manifest.Sessions))
	for _, link := range manifest.Links {
		windows[link.Session]++
	}

	sessions := make([]ListedSession, 0, len(manifest.Sessions))
	for _, session := range manifest.Sessions {
		sessions = append(sessions, ListedSession{
			ID:        session.ID,
			Name:      session.Name,
			Directory: session.Directory,
			Windows:   windows[session.ID],
		})
	}

	return Listing{Generation: generation, Started: manifest.Started, Finished: manifest.Finished, Sessions: sessions}
}

func emptyCurrentState(status string) currentState {
	return currentState{
		Version: currentStateVersion,
		Status:  status,
		Listing: Listing{Generation: noGeneration, Started: 0, Finished: 0, Sessions: []ListedSession{}},
	}
}

func (s currentState) validate() error {
	if s.Version != currentStateVersion {
		return ErrCorrupt
	}

	switch s.Status {
	case currentStatusAvailable:
		return s.Listing.validate()
	case currentStatusUnavailable, currentStatusRecoveryRequired, currentStatusUnreadable:
		if s.Listing.Generation != noGeneration || s.Listing.Started != 0 || s.Listing.Finished != 0 || len(s.Listing.Sessions) != 0 {
			return ErrCorrupt
		}

		return nil
	default:
		return ErrCorrupt
	}
}

func (l Listing) validate() error {
	if !validID(l.Generation.ID) || l.Generation.Sequence == 0 || !validDigest(l.Generation.ManifestSHA256) {
		return ErrCorrupt
	}

	if len(l.Sessions) > model.MaxEntities {
		return ErrCorrupt
	}

	for _, session := range l.Sessions {
		if !model.ValidID(session.ID) || session.Windows < 0 {
			return ErrCorrupt
		}
	}

	return nil
}

func (s currentState) listing() (Listing, error) {
	switch s.Status {
	case currentStatusAvailable:
		return s.Listing, nil
	case currentStatusUnavailable:
		return Listing{}, ErrUnavailable
	case currentStatusRecoveryRequired:
		return Listing{}, ErrRecoveryRequired
	default:
		return Listing{}, ErrCorrupt
	}
}

func (r *Repository) readCurrentState() (currentState, error) {
	data, err := r.root.Read(currentStateName, maxCurrentStateBytes)
	if err != nil {
		return currentState{}, oops.In("snapshot.store").With("operation", "read_current_state").Wrapf(err, "read current state")
	}

	state, err := strictjson.Decode[currentState](data)
	if err == nil {
		err = state.validate()
	} else {
		err = errors.Join(ErrCorrupt, err)
	}

	if err != nil {
		return currentState{}, oops.In("snapshot.store").With("operation", "decode_current_state", "repository", r.path, "file", currentStateName).Wrapf(err, "decode current state")
	}

	return state, nil
}

func (r *Repository) deriveCurrentState(ctx context.Context, catalog Catalog, previous currentState, committed *Listing) (currentState, error) {
	generation, err := catalog.practicalCurrentGeneration()

	switch {
	case errors.Is(err, ErrUnavailable):
		return emptyCurrentState(currentStatusUnavailable), nil
	case errors.Is(err, ErrRecoveryRequired):
		return emptyCurrentState(currentStatusRecoveryRequired), nil
	case err != nil:
		return currentState{}, err
	}

	if previous.Status == currentStatusAvailable && previous.Listing.Generation == generation {
		return previous, nil
	}

	if committed != nil && committed.Generation == generation {
		return currentState{Version: currentStateVersion, Status: currentStatusAvailable, Listing: *committed}, nil
	}

	manifest, err := r.readGeneration(ctx, generation)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return currentState{}, oops.In("snapshot.store").With("operation", "derive_current_state").Wrap(ctxErr)
		}

		return emptyCurrentState(currentStatusUnreadable), nil
	}

	return currentState{Version: currentStateVersion, Status: currentStatusAvailable, Listing: listingFromManifest(generation, manifest)}, nil
}

func (r *Repository) stageCurrentState(ctx context.Context, catalog Catalog, committed *Listing) (string, error) {
	previous, previousErr := r.readCurrentState()

	state, err := r.deriveCurrentState(ctx, catalog, previous, committed)
	if err != nil {
		return "", err
	}

	data, err := json.Marshal(state)
	if err != nil {
		return "", oops.In("snapshot.store").With("operation", "encode_current_state").Wrapf(err, "encode current state")
	}

	if previousErr == nil {
		published, err := json.Marshal(previous)
		if err == nil && string(published) == string(data) {
			return "", nil
		}
	}

	name := currentStatePrefix + newID()
	if err := writeFile(r.root, name, data); err != nil {
		return "", err
	}

	return name, nil
}

func (r *Repository) repairCurrentState(ctx context.Context, catalog Catalog) error {
	staged, err := r.stageCurrentState(ctx, catalog, nil)
	if err != nil || staged == "" {
		return err
	}

	if err := r.root.Rename(staged, r.root, currentStateName); err != nil {
		return oops.In("snapshot.store").With("operation", "publish_current_state").Wrapf(err, "publish current state")
	}

	if err := r.root.Sync(); err != nil {
		return errors.Join(ErrUncertain, err)
	}

	return nil
}

func (r *Repository) ReadCurrentListing(ctx context.Context) (Listing, error) {
	lock, err := r.root.Lock(ctx, "repository.lock")
	if err != nil {
		return Listing{}, repositoryError("current_listing", oops.In("snapshot.store").With("operation", "lock_snapshot_read").Wrapf(err, "lock snapshot read"))
	}

	state, readErr := r.readCurrentState()
	if err := errors.Join(readErr, lock.Close()); err != nil {
		return Listing{}, repositoryError("current_listing", err)
	}

	listing, err := state.listing()
	if err != nil {
		return Listing{}, repositoryError("current_listing", err)
	}

	return listing, nil
}

func (r *Repository) ReadListing(ctx context.Context, id string) (Listing, error) {
	lock, err := r.root.Lock(ctx, "repository.lock")
	if err != nil {
		return Listing{}, repositoryError("listing", oops.In("snapshot.store").With("operation", "lock_snapshot_read").Wrapf(err, "lock snapshot read"))
	}

	listing, readErr := r.listingLocked(ctx, id)

	return listing, repositoryError("listing", errors.Join(readErr, lock.Close()))
}

func (r *Repository) listingLocked(ctx context.Context, id string) (Listing, error) {
	catalog, err := r.catalog()
	if err != nil {
		return Listing{}, err
	}

	index := catalog.generationIndex(id)
	if index < 0 {
		return Listing{}, ErrUnavailable
	}

	generation := catalog.Generations[index]

	manifest, err := r.readGeneration(ctx, generation)
	if err != nil {
		return Listing{}, err
	}

	return listingFromManifest(generation, manifest), nil
}
