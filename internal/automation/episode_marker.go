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

// ServerLossMarkerName records the autosave a server-loss report followed, so each loss is
// reported once.
const ServerLossMarkerName = "server-loss.json"

// RestorePendingMarkerName records the server start a restore-pending report or late restore was
// made for, so each unrestored server is handled once.
const RestorePendingMarkerName = "restore-pending.json"

const maxEpisodeMarkerBytes = 4096

type episodeMarker struct {
	ReportedFor int64 `json:"reportedFor"`
}

func ServerLossMarkerPath(historyPath string) string {
	return filepath.Join(filepath.Dir(historyPath), ServerLossMarkerName)
}

func RestorePendingMarkerPath(historyPath string) string {
	return filepath.Join(filepath.Dir(historyPath), RestorePendingMarkerName)
}

// EpisodeUnreported reports whether the episode identified by stamp has not been reported yet.
// A non-positive stamp identifies no episode.
func EpisodeUnreported(path string, stamp int64) (bool, error) {
	if stamp <= 0 {
		return false, nil
	}

	marker, err := readEpisodeMarker(path)
	if err != nil {
		return false, err
	}

	return marker.ReportedFor != stamp, nil
}

// RecordEpisodeReported marks the episode identified by stamp as reported.
func RecordEpisodeReported(path string, stamp int64) (returnErr error) {
	data, err := json.Marshal(episodeMarker{ReportedFor: stamp})
	if err != nil {
		return oops.In("automation").Code("episode_marker_failed").With("operation", "encode_episode_marker").Wrap(err)
	}

	dir, name, err := privatefs.OpenParent(path, true)
	if err != nil {
		return oops.In("automation").Code("episode_marker_failed").With("operation", "open_episode_marker_dir").Wrap(err)
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
			oops.In("automation").Code("episode_marker_failed").With("operation", "publish_episode_marker").Wrap(err),
			wrapStatusCleanup("remove", removeStatusTemp(dir, temporary)),
		)
	}

	return nil
}

func readEpisodeMarker(path string) (episodeMarker, error) {
	dir, name, err := privatefs.OpenParent(path, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return episodeMarker{ReportedFor: 0}, nil
		}

		return episodeMarker{ReportedFor: 0}, oops.In("automation").Code("episode_marker_failed").With("operation", "open_episode_marker_dir").Wrap(err)
	}

	marker, readErr := readEpisodeMarkerFile(dir, name)
	closeErr := dir.Close()

	return marker, errors.Join(readErr, wrapStatusCleanup("close directory", closeErr))
}

func readEpisodeMarkerFile(dir *privatefs.Dir, name string) (episodeMarker, error) {
	file, err := dir.File(name, unix.O_RDONLY)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return episodeMarker{ReportedFor: 0}, nil
		}

		return episodeMarker{ReportedFor: 0}, oops.In("automation").Code("episode_marker_failed").With("operation", "open_episode_marker").Wrap(err)
	}

	data, readErr := io.ReadAll(io.LimitReader(file, maxEpisodeMarkerBytes+1))
	closeErr := file.Close()

	if err := errors.Join(readErr, closeErr); err != nil {
		return episodeMarker{ReportedFor: 0}, oops.In("automation").Code("episode_marker_failed").With("operation", "read_episode_marker").Wrap(err)
	}

	marker, err := strictjson.Decode[episodeMarker](data)
	if err != nil || len(data) > maxEpisodeMarkerBytes || marker.ReportedFor < 0 {
		return episodeMarker{ReportedFor: 0}, oops.In("automation").Code("episode_marker_failed").With("operation", "decode_episode_marker").Wrap(strictjson.ErrInvalid)
	}

	return marker, nil
}
