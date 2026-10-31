//go:build linux || darwin

package automation

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/samber/oops"
	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/privatefs"
)

const FinalSaveStatusName = "autosave-status.json"

const (
	FinalSaveSaved      = "saved"
	FinalSaveUncertain  = "uncertain"
	FinalSaveFailed     = "failed"
	FinalSaveIncomplete = "incomplete"
)

const (
	maxFinalStatusBytes = 64 * 1024
	statusTempNameBytes = 8
)

var (
	ErrFinalStatusPathEmpty  = errors.New("final save status path is empty")
	ErrFinalStatusNotRegular = errors.New("final save status is not a regular file")
	ErrFinalStatusTooLarge   = errors.New("final save status exceeds size limit")
)

type FinalSaveStatus struct {
	StartedAtUnix   int64  `json:"startedAt"`
	CompletedAtUnix int64  `json:"completedAt"`
	DurationMS      int64  `json:"durationMs"`
	Result          string `json:"result"`
	Reason          string `json:"reason,omitempty"`
	Attempt         string `json:"attempt,omitempty"`
	Generation      string `json:"generation,omitempty"`
	SnapshotStatus  string `json:"snapshotStatus,omitempty"`
	Recorded        bool   `json:"recorded"`
	Bookkeeping     bool   `json:"bookkeepingComplete"`
	Incomplete      bool   `json:"incomplete"`
	ServerPID       int    `json:"serverPid,omitempty"`
	ServerStarted   int64  `json:"serverStartedAt,omitempty"`
	// Announcement is set by the lifecycle coordinator for a shutdown or sleep that logind
	// announced and has not canceled since.
	Announcement bool `json:"announcement,omitempty"`
}

// CoversAnnouncedStop reports whether the coordinator already saved this exact tmux server for a
// shutdown or sleep that is still in progress.
func (s FinalSaveStatus) CoversAnnouncedStop(serverPID int, serverStarted int64) bool {
	return s.Announcement && s.Result == FinalSaveSaved && !s.Incomplete &&
		serverPID > 0 && s.ServerPID == serverPID && s.ServerStarted == serverStarted
}

// EndAnnouncement marks a recorded coordinator save as no longer covering the current state.
func EndAnnouncement(path string) error {
	status, err := ReadFinalSaveStatus(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	if !status.Announcement {
		return nil
	}

	status.Announcement = false

	return WriteFinalSaveStatus(path, status)
}

func WriteFinalSaveStatus(path string, status FinalSaveStatus) (returnErr error) {
	if path == "" {
		return oops.In("automation").Code("final_status_failed").With("operation", "write_final_save_status").Wrap(ErrFinalStatusPathEmpty)
	}

	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return oops.In("automation").Code("final_status_failed").With("operation", "encode_final_save_status").Wrap(err)
	}

	absolute, err := filepath.Abs(path)
	if err != nil {
		return oops.In("automation").Code("final_status_failed").With("operation", "resolve_final_save_status_path").Wrap(err)
	}

	dir, name, err := privatefs.OpenParent(absolute, true)
	if err != nil {
		return oops.In("automation").Code("final_status_failed").With("operation", "open_final_save_status_dir").Wrap(err)
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
			oops.In("automation").Code("final_status_failed").With("operation", "publish_final_save_status").Wrap(err),
			wrapStatusCleanup("remove", removeStatusTemp(dir, temporary)),
		)
	}

	if err := dir.Sync(); err != nil {
		return oops.In("automation").Code("final_status_failed").With("operation", "sync_final_save_status_dir").Wrap(err)
	}

	return nil
}

func ReadFinalSaveStatus(path string) (FinalSaveStatus, error) {
	if path == "" {
		return FinalSaveStatus{}, oops.In("automation").Code("final_status_failed").With("operation", "read_final_save_status").Wrap(ErrFinalStatusPathEmpty)
	}

	absolute, err := filepath.Abs(path)
	if err != nil {
		return FinalSaveStatus{}, oops.In("automation").Code("final_status_failed").With("operation", "resolve_final_save_status_path").Wrap(err)
	}

	dir, name, err := privatefs.OpenParent(absolute, false)
	if err != nil {
		return FinalSaveStatus{}, oops.In("automation").Code("final_status_failed").With("operation", "open_final_save_status_dir").Wrap(err)
	}

	status, readErr := readFinalSaveStatus(dir, name)
	closeErr := dir.Close()

	return status, errors.Join(readErr, wrapStatusCleanup("close directory", closeErr))
}

func readFinalSaveStatus(dir *privatefs.Dir, name string) (FinalSaveStatus, error) {
	file, err := dir.File(name, unix.O_RDONLY)
	if err != nil {
		if errors.Is(err, privatefs.ErrUnsafe) {
			return FinalSaveStatus{}, oops.In("automation").Code("final_status_failed").With("operation", "open_final_save_status").Wrap(ErrFinalStatusNotRegular)
		}

		return FinalSaveStatus{}, oops.In("automation").Code("final_status_failed").With("operation", "open_final_save_status").Wrap(err)
	}

	data, readErr := io.ReadAll(io.LimitReader(file, maxFinalStatusBytes+1))

	closeErr := file.Close()
	if readErr != nil {
		return FinalSaveStatus{}, errors.Join(
			oops.In("automation").Code("final_status_failed").With("operation", "read_final_save_status").Wrap(readErr),
			wrapStatusCleanup("close", closeErr),
		)
	}

	if len(data) > maxFinalStatusBytes {
		return FinalSaveStatus{}, errors.Join(
			oops.In("automation").Code("final_status_failed").With("operation", "read_final_save_status").Wrap(ErrFinalStatusTooLarge),
			wrapStatusCleanup("close", closeErr),
		)
	}

	if closeErr != nil {
		return FinalSaveStatus{}, oops.In("automation").Code("final_status_failed").With("operation", "close_final_save_status").Wrap(closeErr)
	}

	var status FinalSaveStatus

	if err := json.Unmarshal(data, &status); err != nil {
		return FinalSaveStatus{}, oops.In("automation").Code("final_status_failed").With("operation", "decode_final_save_status").Wrap(err)
	}

	return status, nil
}

func statusTemporaryName(name string) (string, error) {
	suffix := make([]byte, statusTempNameBytes)
	if _, err := io.ReadFull(rand.Reader, suffix); err != nil {
		return "", oops.In("automation").Code("final_status_failed").With("operation", "generate_temp_name").Wrap(err)
	}

	return "." + name + ".tmp." + hex.EncodeToString(suffix), nil
}

func writeStatusTemp(dir *privatefs.Dir, name string, data []byte) error {
	file, err := dir.File(name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return oops.In("automation").Code("final_status_failed").With("operation", "create_final_save_status_temp").Wrap(err)
	}

	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()

	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return errors.Join(
			oops.In("automation").Code("final_status_failed").With("operation", "write_final_save_status_temp").Wrap(err),
			wrapStatusCleanup("remove", removeStatusTemp(dir, name)),
		)
	}

	return nil
}

func removeStatusTemp(dir *privatefs.Dir, name string) error {
	if err := dir.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return oops.In("automation").Code("final_status_failed").With("operation", "remove_temp_status").Wrap(err)
	}

	return nil
}

func wrapStatusCleanup(operation string, err error) error {
	if err == nil {
		return nil
	}

	return oops.In("automation").Code("final_status_failed").With("operation", operation).Wrap(err)
}
