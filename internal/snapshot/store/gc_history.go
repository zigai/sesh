//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/privatefs"
)

func (r *Repository) compactHistory(ctx context.Context) error {
	catalog, err := r.catalog()
	if err != nil {
		return err
	}

	names, err := r.generationNames()
	if err != nil {
		return err
	}

	present := make(map[string]bool, len(names))
	for _, name := range names {
		present[name] = true
	}

	records, err := r.openCollectionDirectory(false)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	compactErr := r.compactAttempts(ctx, catalog, present, records)

	return errors.Join(compactErr, closeCollectionDirectory(records))
}

func (r *Repository) compactAttempts(ctx context.Context, catalog Catalog, present map[string]bool, records *privatefs.Dir) error {
	referenced := catalog.historyReferences()
	published := catalog.latestPublishedSaveSequence()
	failedFloor := published

	for _, generation := range catalog.Generations {
		failedFloor = min(failedFloor, generation.Sequence)
	}

	retained := slices.DeleteFunc(slices.Clone(catalog.Attempts), func(attempt Attempt) bool {
		if referenced[attempt.ID] || present[attempt.ID] {
			return false
		}

		switch attempt.State {
		case attemptStateFailed:
			return attempt.Sequence < failedFloor
		case attemptStateCommitted, attemptStateIncomplete:
			return attempt.Sequence < published && collectionSettled(readCollectionRecord(records, attempt.ID))
		default:
			return false
		}
	})

	if len(retained) != len(catalog.Attempts) {
		catalog.Attempts = retained
		if err := r.publish(ctx, catalog, nil); err != nil {
			return err
		}
	}

	return removeSettledCollectionRecords(records, retained, present)
}

func (c Catalog) historyReferences() map[string]bool {
	referenced := make(map[string]bool, len(c.Generations)+len(c.Rewrites)+len(c.Operations))
	for _, generation := range c.Generations {
		referenced[generation.ID] = true
	}

	for _, rewrite := range c.Rewrites {
		referenced[rewrite.Source] = true
		referenced[rewrite.Replacement] = true
	}

	for _, operation := range c.Operations {
		referenced[operation.Source] = true
	}

	return referenced
}

func collectionSettled(record collectionRecord) bool {
	return record.reason == "" && (!record.found || record.intent.State == collectionStateRemoved)
}

func removeSettledCollectionRecords(records *privatefs.Dir, attempts []Attempt, present map[string]bool) error {
	if records == nil {
		return nil
	}

	settled, err := settledCollectionRecords(records, attempts, present)
	if err != nil || len(settled) == 0 {
		return err
	}

	for _, name := range settled {
		if err := records.Remove(name); err != nil {
			return oops.In("snapshot.store").With("operation", "remove_collection_record").Wrapf(err, "remove collection record")
		}
	}

	if err := records.Sync(); err != nil {
		return oops.In("snapshot.store").With("operation", "sync_collection_records").Wrapf(err, "sync collection records")
	}

	return nil
}

func settledCollectionRecords(records *privatefs.Dir, attempts []Attempt, present map[string]bool) ([]string, error) {
	names, err := records.NamesLimit(maxCollectionEntries)
	if err != nil {
		return nil, oops.In("snapshot.store").With("operation", "list_collection_records").Wrapf(err, "list collection records")
	}

	attempted := make(map[string]bool, len(attempts))
	for _, attempt := range attempts {
		attempted[attempt.ID] = true
	}

	settled := make([]string, 0, len(names))

	for _, name := range names {
		id, ok := strings.CutSuffix(name, collectionRecordSuffix)
		if !ok || !validID(id) || attempted[id] || present[id] {
			continue
		}

		if record := readCollectionRecord(records, id); record.found && collectionSettled(record) {
			settled = append(settled, name)
		}
	}

	return settled, nil
}
