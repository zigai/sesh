//go:build linux || darwin

package automation

import (
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/samber/oops"
	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/strictjson"
)

// ServerLossMarkerName is the state-directory file that remembers which autosave a missing-server
// report was made for, so one loss is reported once and not on every autosave interval.
const ServerLossMarkerName = "server-loss.json"

const maxServerLossMarkerBytes = 4096

type serverLossMarker struct {
	ReportedFor int64 `json:"reportedFor"`
}

func ServerLossMarkerPath(historyPath string) string {
	return filepath.Join(filepath.Dir(historyPath), ServerLossMarkerName)
}

// ServerLossUnreported reports whether the loss that follows the autosave at lastAutosave has not
// been reported yet. Without a recorded autosave there is nothing that could be lost.
func ServerLossUnreported(path string, lastAutosave int64) (bool, error) {
	if lastAutosave <= 0 {
		return false, nil
	}

	marker, err := readServerLossMarker(path)
	if err != nil {
		return false, err
	}

	return marker.ReportedFor != lastAutosave, nil
}

// RecordServerLossReported remembers that the loss that follows the autosave at lastAutosave was
// reported. A later recorded autosave changes the stamp and so starts a new episode.
func RecordServerLossReported(path string, lastAutosave int64) (returnErr error) {
	data, err := json.Marshal(serverLossMarker{ReportedFor: lastAutosave})
	if err != nil {
		return oops.In("automation").Code("server_loss_marker_failed").With("operation", "encode_server_loss_marker").Wrap(err)
	}

	dir, name, err := privatefs.OpenParent(path, true)
	if err != nil {
		return oops.In("automation").Code("server_loss_marker_failed").With("operation", "open_server_loss_marker_dir").Wrap(err)
	}

	defer func() {
		returnErr = errors.Join(returnErr, wrapStatusCleanup("close directory", dir.Close()))
	}()

	temporary, err := statusTemporaryName(name)
	if err != nil {
		return err
	}

	if err := writeStatusTemp(dir, temporary, data); err != nil {
		return err
	}

	if err := dir.Rename(temporary, dir, name); err != nil {
		return errors.Join(
			oops.In("automation").Code("server_loss_marker_failed").With("operation", "publish_server_loss_marker").Wrap(err),
			wrapStatusCleanup("remove", removeStatusTemp(dir, temporary)),
		)
	}

	return nil
}

func readServerLossMarker(path string) (serverLossMarker, error) {
	dir, name, err := privatefs.OpenParent(path, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return serverLossMarker{ReportedFor: 0}, nil
		}

		return serverLossMarker{ReportedFor: 0}, oops.In("automation").Code("server_loss_marker_failed").With("operation", "open_server_loss_marker_dir").Wrap(err)
	}

	marker, readErr := readServerLossMarkerFile(dir, name)
	closeErr := dir.Close()

	return marker, errors.Join(readErr, wrapStatusCleanup("close directory", closeErr))
}

func readServerLossMarkerFile(dir *privatefs.Dir, name string) (serverLossMarker, error) {
	file, err := dir.File(name, unix.O_RDONLY)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return serverLossMarker{ReportedFor: 0}, nil
		}

		return serverLossMarker{ReportedFor: 0}, oops.In("automation").Code("server_loss_marker_failed").With("operation", "open_server_loss_marker").Wrap(err)
	}

	data, readErr := io.ReadAll(io.LimitReader(file, maxServerLossMarkerBytes+1))
	closeErr := file.Close()

	if err := errors.Join(readErr, closeErr); err != nil {
		return serverLossMarker{ReportedFor: 0}, oops.In("automation").Code("server_loss_marker_failed").With("operation", "read_server_loss_marker").Wrap(err)
	}

	marker, err := strictjson.Decode[serverLossMarker](data)
	if err != nil || len(data) > maxServerLossMarkerBytes || marker.ReportedFor < 0 {
		return serverLossMarker{ReportedFor: 0}, oops.In("automation").Code("server_loss_marker_failed").With("operation", "decode_server_loss_marker").Wrap(strictjson.ErrInvalid)
	}

	return marker, nil
}
