//go:build linux || darwin

package store

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"github.com/samber/oops"

	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/snapshot/model"
)

const (
	compressionOverhead = 1 << 20
	maxCompressedBytes  = model.MaxArtifactBytes + compressionOverhead
)

var errTrailingArtifactBytes = errors.New("unexpected trailing bytes after gzip artifact")

type readerFunc func([]byte) (int, error)

func writeArtifacts(ctx context.Context, dir *privatefs.Dir, descriptors []model.Artifact, sources map[string]io.Reader) error {
	for _, descriptor := range descriptors {
		source := sources[descriptor.ID]
		if source == nil {
			return ErrCorrupt
		}

		if err := writeArtifact(ctx, dir, descriptor, source); err != nil {
			return err
		}
	}

	return nil
}

func writeArtifact(ctx context.Context, dir *privatefs.Dir, descriptor model.Artifact, source io.Reader) error {
	file, err := dir.File(descriptor.ID+".bin.gz", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return oops.In("snapshot.store").With("operation", "create_artifact").Wrapf(err, "create artifact")
	}

	compressed := gzip.NewWriter(file)
	hash := sha256.New()
	reader := cancellableReader(ctx, source)

	written, copyErr := io.Copy(io.MultiWriter(compressed, hash), io.LimitReader(reader, descriptor.Length+1))
	if err := errors.Join(copyErr, compressed.Close(), file.Sync(), file.Close()); err != nil {
		return oops.In("snapshot.store").With("operation", "write_artifact").Wrapf(err, "write artifact")
	}

	if written != descriptor.Length || hex.EncodeToString(hash.Sum(nil)) != descriptor.SHA256 {
		return ErrCorrupt
	}

	return nil
}

func verifyArtifact(ctx context.Context, dir *privatefs.Dir, descriptor model.Artifact, output io.Writer) error {
	file, err := dir.File(descriptor.ID+".bin.gz", unix.O_RDONLY)
	if err != nil {
		return oops.In("snapshot.store").With("operation", "open_artifact").Wrapf(err, "open artifact")
	}

	_, verifyErr := verifyArtifactFile(ctx, file, descriptor, output)

	return verifyErr
}

func countingReader(source io.Reader, count *int64) io.Reader {
	return readerFunc(func(data []byte) (int, error) {
		n, err := source.Read(data)
		*count += int64(n)

		if errors.Is(err, io.EOF) {
			return n, io.EOF
		}

		if err != nil {
			return n, oops.In("snapshot.store").With("operation", "read_artifact").Wrapf(err, "read artifact")
		}

		return n, nil
	})
}

func verifyArtifactFile(ctx context.Context, file *os.File, descriptor model.Artifact, output io.Writer) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, errors.Join(err, file.Close())
	}

	info, statErr := file.Stat()
	if statErr != nil {
		return 0, errors.Join(statErr, file.Close())
	}

	size := info.Size()
	if size <= 0 || size > maxCompressedBytes {
		return 0, errors.Join(ErrCorrupt, file.Close())
	}

	var readCount int64

	limited := io.LimitReader(cancellableReader(ctx, file), maxCompressedBytes+1)
	cr := countingReader(limited, &readCount)
	reader := bufio.NewReader(cr)

	compressed, err := gzip.NewReader(reader)
	if err != nil {
		return 0, errors.Join(ErrCorrupt, err, file.Close())
	}

	compressed.Multistream(false)

	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(hash, output), io.LimitReader(compressed, descriptor.Length+1))
	_, trailingErr := reader.ReadByte()

	trailingOutcome := normalizeTrailingError(trailingErr)
	validationErr := validateArtifactContent(written, readCount, size, descriptor, hash.Sum(nil))

	closeErr := errors.Join(copyErr, trailingOutcome, validationErr, compressed.Close(), file.Close())
	if closeErr != nil {
		return 0, oops.In("snapshot.store").With("operation", "verify_artifact").Wrapf(closeErr, "verify artifact")
	}

	if err := ctx.Err(); err != nil {
		return 0, oops.In("snapshot.store").With("operation", "verify_artifact").Wrap(err)
	}

	return size, nil
}

func normalizeTrailingError(err error) error {
	switch {
	case errors.Is(err, io.EOF):
		return nil
	case err == nil:
		return errors.Join(ErrCorrupt, errTrailingArtifactBytes)
	default:
		return errors.Join(ErrCorrupt, err)
	}
}

func validateArtifactContent(written, readCount, size int64, descriptor model.Artifact, hash []byte) error {
	if written != descriptor.Length || hex.EncodeToString(hash) != descriptor.SHA256 {
		return ErrCorrupt
	}

	if readCount > maxCompressedBytes || readCount != size {
		return ErrCorrupt
	}

	return nil
}

func (r readerFunc) Read(data []byte) (int, error) { return r(data) }

func cancellableReader(ctx context.Context, source io.Reader) io.Reader {
	return readerFunc(func(data []byte) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, oops.In("snapshot.store").With("operation", "artifact_canceled").Wrapf(err, "artifact canceled")
		}

		n, err := source.Read(data)
		if errors.Is(err, io.EOF) {
			return n, io.EOF
		}

		if err != nil {
			return n, oops.In("snapshot.store").With("operation", "read_artifact").Wrapf(err, "read artifact")
		}

		return n, nil
	})
}
