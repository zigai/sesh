//go:build linux || darwin

package store

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/snapshot/model"
)

const manifestFileName = "manifest.json.gz"

func compressManifest(data []byte) ([]byte, error) {
	var compressed bytes.Buffer

	writer := gzip.NewWriter(&compressed)

	_, writeErr := writer.Write(data)
	if err := errors.Join(writeErr, writer.Close()); err != nil {
		return nil, oops.In("snapshot.store").
			With("operation", "compress_manifest").
			Wrapf(err, "compress manifest")
	}

	return compressed.Bytes(), nil
}

func decompressManifest(compressed []byte) ([]byte, error) {
	source := bytes.NewReader(compressed)

	reader, err := gzip.NewReader(source)
	if err != nil {
		return nil, errors.Join(ErrCorrupt, err)
	}

	reader.Multistream(false)

	data, readErr := io.ReadAll(io.LimitReader(reader, model.MaxManifestBytes+1))
	if err := errors.Join(readErr, reader.Close()); err != nil {
		return nil, errors.Join(ErrCorrupt, err)
	}

	if len(data) > model.MaxManifestBytes {
		return nil, ErrCorrupt
	}

	if source.Len() != 0 {
		return nil, errors.Join(ErrCorrupt, errTrailingArtifactBytes)
	}

	return data, nil
}

func writeManifestFile(dir *privatefs.Dir, data []byte) error {
	compressed, err := compressManifest(data)
	if err != nil {
		return err
	}

	return writeFile(dir, manifestFileName, compressed)
}

// readManifestFile returns the manifest JSON, which catalog digests cover, and its size on disk.
func readManifestFile(dir *privatefs.Dir) ([]byte, int64, error) {
	compressed, err := dir.Read(manifestFileName, model.MaxManifestBytes)
	if err != nil {
		return nil, 0, oops.In("snapshot.store").
			With("operation", "read_manifest_file").
			Wrapf(err, "read manifest file")
	}

	data, err := decompressManifest(compressed)
	if err != nil {
		return nil, 0, err
	}

	return data, int64(len(compressed)), nil
}
