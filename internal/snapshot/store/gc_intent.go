//go:build linux || darwin

package store

import (
	"encoding/json/v2"
	"errors"
	"os"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/strictjson"
)

type collectionRecord struct {
	intent CollectionIntent
	found  bool
	reason string
	cause  error
}

func absentCollectionRecord() collectionRecord {
	return collectionRecordFor("")
}

func unusableCollectionRecord(err error) collectionRecord {
	record := collectionRecordFor(CollectionReasonUnreadableIntent)
	record.cause = err

	return record
}

func collectionRecordFor(reason string) collectionRecord {
	var intent CollectionIntent

	return collectionRecord{intent: intent, found: false, reason: reason, cause: nil}
}

func readCollectionRecord(records *privatefs.Dir, generation string) collectionRecord {
	if records == nil {
		return absentCollectionRecord()
	}

	data, err := records.Read(generation+collectionRecordSuffix, maxCollectionRecord)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return absentCollectionRecord()
		}

		return unusableCollectionRecord(err)
	}

	intent, err := strictjson.Decode[CollectionIntent](data)
	if err != nil {
		return unusableCollectionRecord(err)
	}

	if err := intent.validate(); err != nil {
		return unusableCollectionRecord(err)
	}

	if intent.Generation != generation {
		return unusableCollectionRecord(ErrCorrupt)
	}

	return collectionRecord{intent: intent, found: true, reason: "", cause: nil}
}

func (r *Repository) writeCollectionIntent(records *privatefs.Dir, intent CollectionIntent) error {
	if records == nil || intent.validate() != nil {
		return ErrCorrupt
	}

	data, err := json.Marshal(intent)
	if err != nil {
		return oops.In("snapshot.store").With("operation", "encode_collection_intent").Wrapf(err, "encode collection intent")
	}

	if len(data) > maxCollectionRecord {
		return ErrCorrupt
	}

	scratch := collectionScratchPrefix + newID()
	if err := writeFile(records, scratch, data); err != nil {
		return err
	}

	if err := records.Rename(scratch, records, intent.Generation+collectionRecordSuffix); err != nil {
		return oops.In("snapshot.store").With("operation", "record_collection_intent").Wrapf(err, "record collection intent")
	}

	if err := records.Sync(); err != nil {
		return errors.Join(ErrUncertain, err)
	}

	return nil
}

func (r *Repository) openCollectionDirectory(create bool) (*privatefs.Dir, error) {
	dir, err := r.root.Child(collectionDirectory, create)
	if err != nil {
		return nil, oops.In("snapshot.store").With("operation", "open_collection_directory").Wrapf(err, "open collection directory")
	}

	return dir, nil
}
