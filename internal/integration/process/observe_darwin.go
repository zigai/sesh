//go:build darwin

package process

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/snapshot/model"
)

// Layout constants from XNU bsd/sys/proc_info.h and bsd/sys/proc_info_private.h.
// struct vinfo_stat is 136 bytes, struct vnode_info adds vi_type, vi_pad and fsid_t (152 bytes),
// and struct vnode_info_path appends vip_path[MAXPATHLEN]. struct proc_vnodepathinfo holds the
// current directory entry followed by the root directory entry.
const (
	maxProcargsBytes       = 4 << 20
	procargsCountBytes     = 4
	procInfoCallPIDInfo    = 2
	procPIDVnodePathInfo   = 9
	procPIDPathInfo        = 11
	darwinMaxPathLen       = 1024
	procPIDPathInfoMaxSize = 4 * darwinMaxPathLen
	vnodeInfoSize          = 152
	vnodeInfoPathSize      = vnodeInfoSize + darwinMaxPathLen
	vnodePathInfoSize      = 2 * vnodeInfoPathSize
	vinfoStatDevOffset     = 0
	vinfoStatNlinkOffset   = 6
	vinfoStatInoOffset     = 8
	darwinZombieState      = 5
)

var errMalformedProcInfo = errors.New("process info is malformed")

type darwinProcargs struct {
	executable         []byte
	arguments          [][]byte
	environ            []byte
	environUnavailable bool
}

func observePlatform(ctx context.Context, req Request) (*Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("observe canceled: %w", err)
	}

	if err := checkDarwinBirth(ctx, req); err != nil {
		return nil, err
	}

	procargs, err := readDarwinProcargs(ctx, req)
	if err != nil {
		return nil, err
	}

	executable, executableDeleted, err := readDarwinExecutable(ctx, req, procargs.executable)
	if err != nil {
		return nil, err
	}

	directory, directoryDeleted, err := readDarwinDirectory(ctx, req)
	if err != nil {
		return nil, err
	}

	var environment []model.Variable
	if !procargs.environUnavailable {
		environment, err = selectEnvironment(procargs.environ, req.EnvironmentKeys)
		if err != nil {
			return nil, err
		}
	}

	if err := checkDarwinBirth(ctx, req); err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("observe canceled: %w", err)
	}

	return &Observation{
		PID:               req.PID,
		Birth:             req.ExpectedBirth,
		Executable:        executable,
		ExecutableDeleted: executableDeleted,
		Arguments:         procargs.arguments,
		Directory:         directory,
		DirectoryDeleted:  directoryDeleted,
		Environment:       environment,
	}, nil
}

func checkDarwinBirth(ctx context.Context, req Request) error {
	birth, err := readDarwinIdentity(ctx, req.PID)
	if err != nil {
		return err
	}

	if birth != req.ExpectedBirth {
		return ErrIdentityMismatch
	}

	return nil
}

func readDarwinKinfo(ctx context.Context, pid int) (unix.KinfoProc, error) {
	if err := ctx.Err(); err != nil {
		return unix.KinfoProc{}, fmt.Errorf("observe canceled: %w", err)
	}

	// The slice form reports a missing process as an empty result instead of a short read.
	infos, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return unix.KinfoProc{}, classifyDarwinProcessError(err)
	}

	if len(infos) != 1 || int(infos[0].Proc.P_pid) != pid {
		return unix.KinfoProc{}, ErrProcessVanished
	}

	if infos[0].Proc.P_stat == darwinZombieState {
		return unix.KinfoProc{}, ErrProcessZombie
	}

	return infos[0], nil
}

func explainDarwinReadError(ctx context.Context, req Request, err error) error {
	// The kernel reports a vanished or exited process as EINVAL or ESRCH from the failed read.
	if birthErr := checkDarwinBirth(ctx, req); birthErr != nil {
		return birthErr
	}

	return err
}

func readDarwinProcargs(ctx context.Context, req Request) (darwinProcargs, error) {
	if err := ctx.Err(); err != nil {
		return darwinProcargs{}, fmt.Errorf("observe canceled: %w", err)
	}

	data, err := unix.SysctlRaw("kern.procargs2", req.PID)
	if err != nil {
		return darwinProcargs{}, explainDarwinReadError(ctx, req, fmt.Errorf("read process arguments: %w", err))
	}

	return parseDarwinProcargs(data)
}

func parseDarwinProcargs(data []byte) (darwinProcargs, error) {
	// kern.procargs2 holds a native int32 argc, the executable path, NUL padding, argc arguments,
	// then environment entries up to an empty entry.
	if len(data) > maxProcargsBytes {
		return darwinProcargs{}, ErrInputTooLarge
	}

	if len(data) < procargsCountBytes {
		return darwinProcargs{}, ErrMalformedCmdline
	}

	// A negative argc reads as a count larger than any valid buffer.
	argc := binary.NativeEndian.Uint32(data[:procargsCountBytes])
	rest := data[procargsCountBytes:]

	executable, rest, found := bytes.Cut(rest, []byte{0})
	if !found {
		return darwinProcargs{}, ErrMalformedCmdline
	}

	rest = bytes.TrimLeft(rest, "\x00")

	if argc == 0 {
		return darwinProcargs{}, ErrEmptyCmdline
	}

	if uint64(argc) > uint64(len(rest)) {
		return darwinProcargs{}, ErrMalformedCmdline
	}

	arguments := make([][]byte, 0, argc)

	for range argc {
		argument, after, found := bytes.Cut(rest, []byte{0})
		if !found {
			return darwinProcargs{}, ErrMalformedCmdline
		}

		arguments = append(arguments, bytes.Clone(argument))
		rest = after
	}

	// The kernel cuts the environment of restricted processes, leaving nothing after the arguments.
	if len(rest) == 0 {
		return darwinProcargs{
			executable: bytes.Clone(executable), arguments: arguments, environ: nil, environUnavailable: true,
		}, nil
	}

	environ, err := darwinEnvironBlock(rest)
	if err != nil {
		return darwinProcargs{}, err
	}

	return darwinProcargs{
		executable: bytes.Clone(executable), arguments: arguments, environ: environ, environUnavailable: false,
	}, nil
}

func darwinEnvironBlock(data []byte) ([]byte, error) {
	end := 0

	for end < len(data) && data[end] != 0 {
		length := bytes.IndexByte(data[end:], 0)
		if length < 0 {
			return nil, ErrMalformedEnviron
		}

		end += length + 1
	}

	return data[:end], nil
}

func readDarwinExecutable(ctx context.Context, req Request, launchedPath []byte) ([]byte, bool, error) {
	var buffer [procPIDPathInfoMaxSize]byte

	if _, err := procPIDInfo(req.PID, procPIDPathInfo, buffer[:]); err != nil {
		readErr := explainDarwinReadError(ctx, req, fmt.Errorf("read process executable: %w", err))
		if !errors.Is(readErr, unix.ESRCH) && !errors.Is(readErr, unix.ENOENT) {
			return nil, false, readErr
		}

		// The kernel reports ESRCH or ENOENT when the executable no longer resolves at its path.
		if len(launchedPath) == 0 {
			return nil, false, errMalformedProcInfo
		}

		return launchedPath, true, nil
	}

	path, _, found := bytes.Cut(buffer[:], []byte{0})
	if !found || len(path) == 0 {
		return nil, false, errMalformedProcInfo
	}

	return bytes.Clone(path), false, nil
}

func readDarwinDirectory(ctx context.Context, req Request) ([]byte, bool, error) {
	var info [vnodePathInfoSize]byte

	size, err := procPIDInfo(req.PID, procPIDVnodePathInfo, info[:])
	if err != nil {
		return nil, false, explainDarwinReadError(ctx, req, fmt.Errorf("read process directory: %w", err))
	}

	if size != vnodePathInfoSize {
		return nil, false, errMalformedProcInfo
	}

	current := info[:vnodeInfoPathSize]

	path, _, found := bytes.Cut(current[vnodeInfoSize:], []byte{0})
	if !found {
		return nil, false, errMalformedProcInfo
	}

	if len(path) == 0 {
		return nil, true, nil
	}

	var device int32
	if _, err := binary.Decode(current[vinfoStatDevOffset:], binary.NativeEndian, &device); err != nil {
		return nil, false, errMalformedProcInfo
	}

	links := binary.NativeEndian.Uint16(current[vinfoStatNlinkOffset:])
	inode := binary.NativeEndian.Uint64(current[vinfoStatInoOffset:])

	return bytes.Clone(path), links == 0 || !darwinPathMatches(path, device, inode), nil
}

func darwinPathMatches(path []byte, device int32, inode uint64) bool {
	var stat unix.Stat_t

	err := unix.Stat(string(path), &stat)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
		return false
	}

	if err != nil {
		return true
	}

	return stat.Dev == device && stat.Ino == inode
}

func procPIDInfo(pid, flavor int, buffer []byte) (int, error) {
	result, _, errno := unix.Syscall6(
		unix.SYS_PROC_INFO, //nolint:staticcheck // libSystem's proc_pidinfo needs cgo; the proc_info trap is stable.
		procInfoCallPIDInfo,
		uintptr(pid),
		uintptr(flavor),
		0,
		uintptr(unsafe.Pointer(&buffer[0])), //nolint:gosec // The kernel writes into buffer, which the call keeps alive.
		uintptr(len(buffer)),
	)
	if errno != 0 {
		return 0, errno
	}

	return int(result), nil
}
