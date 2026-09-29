//go:build linux || darwin

package tmux

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"

	"golang.org/x/sys/unix"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/privatefs"
)

func TestLeaseExplicitRootOnly(t *testing.T) {
	tempDir := t.TempDir()
	syntheticSocket := filepath.Join(tempDir, "synthetic.sock")

	for _, invalidRoot := range []struct {
		name string
		root string
	}{
		{name: "empty", root: ""},
		{name: "relative", root: "some/relative/path"},
		{name: "root_directory", root: "/"},
		{name: "unclean", root: filepath.Join(tempDir, "sub", "..") + "/"},
		{name: "nul_byte", root: tempDir + "/nul\x00root"},
	} {
		t.Run(invalidRoot.name, func(t *testing.T) {
			lease, err := AcquireLease(t.Context(), invalidRoot.root, syntheticSocket)
			if lease != nil {
				_ = lease.Close()

				t.Fatal("expected nil lease for invalid root")
			}

			if !errors.Is(err, ErrLeaseRoot) {
				t.Fatalf("expected ErrLeaseRoot, got %v", err)
			}

			oopsErr, ok := oops.AsOops(err)
			if !ok {
				t.Fatal("expected oops error")
			}

			if oopsErr.Code() != "endpoint_required" {
				t.Fatalf("expected code endpoint_required, got %q", oopsErr.Code())
			}
		})
	}
}

func TestLeaseEndpointRejection_InvalidPaths(t *testing.T) {
	leaseRoot := filepath.Join(t.TempDir(), "leases")
	tempDir := t.TempDir()

	for _, invalid := range []struct {
		name   string
		socket string
	}{
		{name: "empty", socket: ""},
		{name: "relative", socket: "relative/sock"},
		{name: "root", socket: "/"},
		{name: "unclean_dot_segments", socket: tempDir + "/a/../sock"},
		{name: "trailing_slash", socket: filepath.Join(tempDir, "sock") + "/"},
		{name: "nul_byte", socket: tempDir + "/sock\x00path"},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			lease, err := AcquireLease(t.Context(), leaseRoot, invalid.socket)
			if lease != nil {
				_ = lease.Close()

				t.Fatal("expected nil lease for invalid endpoint")
			}

			if !errors.Is(err, ErrEndpoint) {
				t.Fatalf("expected ErrEndpoint, got %v", err)
			}
		})
	}
}

func TestLeaseReportsNoServerWhenSocketDirectoryIsMissing(t *testing.T) {
	leaseRoot := t.TempDir()

	lease, err := AcquireLease(t.Context(), leaseRoot, filepath.Join(t.TempDir(), "tmux-1000", "default"))
	if lease != nil {
		_ = lease.Close()

		t.Fatal("expected nil lease without a socket directory")
	}

	if !errors.Is(err, gotmux.ErrNoServer) {
		t.Fatalf("expected ErrNoServer, got %v", err)
	}
}

func TestLeaseEndpointRejection_Symlinks(t *testing.T) {
	leaseRoot := filepath.Join(t.TempDir(), "leases")
	tempDir := t.TempDir()

	realParent := filepath.Join(tempDir, "real_parent")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}

	symParent := filepath.Join(tempDir, "sym_parent")
	if err := os.Symlink(realParent, symParent); err != nil {
		t.Fatal(err)
	}

	t.Run("symlinked_parent_dir", func(t *testing.T) {
		socket := filepath.Join(symParent, "test.sock")

		lease, err := AcquireLease(t.Context(), leaseRoot, socket)
		if lease != nil {
			_ = lease.Close()

			t.Fatal("expected nil lease for symlinked parent")
		}

		if !errors.Is(err, ErrEndpoint) || !errors.Is(err, ErrEndpointAlias) {
			t.Fatalf("expected ErrEndpoint and ErrEndpointAlias, got %v", err)
		}
	})

	dummyTarget := filepath.Join(realParent, "target.txt")
	if err := os.WriteFile(dummyTarget, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}

	symSocket := filepath.Join(realParent, "sym.sock")
	if err := os.Symlink(dummyTarget, symSocket); err != nil {
		t.Fatal(err)
	}

	t.Run("symlinked_socket_file", func(t *testing.T) {
		lease, err := AcquireLease(t.Context(), leaseRoot, symSocket)
		if lease != nil {
			_ = lease.Close()

			t.Fatal("expected nil lease for symlinked socket file")
		}

		if !errors.Is(err, ErrEndpoint) || !errors.Is(err, ErrEndpointAlias) {
			t.Fatalf("expected ErrEndpoint and ErrEndpointAlias, got %v", err)
		}
	})
}

func TestLeaseEndpointRejection_NonSockets(t *testing.T) {
	leaseRoot := filepath.Join(t.TempDir(), "leases")
	tempDir := t.TempDir()

	regularFile := filepath.Join(tempDir, "regular.txt")
	if err := os.WriteFile(regularFile, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("regular_file_rejected", func(t *testing.T) {
		lease, err := AcquireLease(t.Context(), leaseRoot, regularFile)
		if lease != nil {
			_ = lease.Close()

			t.Fatal("expected nil lease for regular file")
		}

		if !errors.Is(err, ErrEndpoint) {
			t.Fatalf("expected ErrEndpoint, got %v", err)
		}
	})

	dirPath := filepath.Join(tempDir, "directory")
	if err := os.Mkdir(dirPath, 0o700); err != nil {
		t.Fatal(err)
	}

	t.Run("directory_rejected", func(t *testing.T) {
		lease, err := AcquireLease(t.Context(), leaseRoot, dirPath)
		if lease != nil {
			_ = lease.Close()

			t.Fatal("expected nil lease for directory")
		}

		if !errors.Is(err, ErrEndpoint) {
			t.Fatalf("expected ErrEndpoint, got %v", err)
		}
	})

	fifoPath := filepath.Join(tempDir, "test.fifo")
	if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("fifo_rejected", func(t *testing.T) {
		lease, err := AcquireLease(t.Context(), leaseRoot, fifoPath)
		if lease != nil {
			_ = lease.Close()

			t.Fatal("expected nil lease for fifo")
		}

		if !errors.Is(err, ErrEndpoint) {
			t.Fatalf("expected ErrEndpoint, got %v", err)
		}
	})
}

func TestLeaseAdmittedEndpoints_NotYetExisting(t *testing.T) {
	leaseRoot := filepath.Join(t.TempDir(), "leases")
	tempDir := t.TempDir()

	notYetSocket := filepath.Join(tempDir, "admitted_not_yet.sock")

	lease, err := AcquireLease(t.Context(), leaseRoot, notYetSocket)
	if err != nil {
		t.Fatalf("acquire lease on not-yet-existing socket: %v", err)
	}

	if err := lease.Close(); err != nil {
		t.Fatalf("close lease: %v", err)
	}
}

func TestLeaseUnsafeLockRejection_RootSecurity(t *testing.T) {
	tempDir := t.TempDir()
	syntheticSocket := filepath.Join(tempDir, "synthetic.sock")

	unsafeRoot := filepath.Join(tempDir, "unsafe_root")
	if err := os.Mkdir(unsafeRoot, 0o777); err != nil {
		t.Fatal(err)
	}

	t.Run("unsafe_root_mode_0777", func(t *testing.T) {
		lease, err := AcquireLease(t.Context(), unsafeRoot, syntheticSocket)
		if lease != nil {
			_ = lease.Close()

			t.Fatal("expected nil lease")
		}

		if !errors.Is(err, privatefs.ErrUnsafe) {
			t.Fatalf("expected ErrUnsafe, got %v", err)
		}
	})

	validRoot := filepath.Join(tempDir, "valid_root")
	if err := os.Mkdir(validRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	symRoot := filepath.Join(tempDir, "sym_root")
	if err := os.Symlink(validRoot, symRoot); err != nil {
		t.Fatal(err)
	}

	t.Run("symlink_lease_root", func(t *testing.T) {
		lease, err := AcquireLease(t.Context(), symRoot, syntheticSocket)
		if lease != nil {
			_ = lease.Close()

			t.Fatal("expected nil lease")
		}

		if err == nil {
			t.Fatal("expected error for symlinked lease root")
		}

		if !errors.Is(err, privatefs.ErrUnsafe) && !errors.Is(err, unix.ENOTDIR) {
			t.Fatalf("expected ErrUnsafe or ENOTDIR, got %v", err)
		}
	})
}

func TestLeaseUnsafeLockRejection_SymlinkAndHardlink(t *testing.T) {
	tempDir := t.TempDir()
	syntheticSocket := filepath.Join(tempDir, "synthetic.sock")

	hash := sha256.Sum256([]byte(syntheticSocket))
	lockFilename := hex.EncodeToString(hash[:]) + ".lock"

	root := filepath.Join(tempDir, "valid_root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}

	dummyTarget := filepath.Join(tempDir, "target.txt")
	if err := os.WriteFile(dummyTarget, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	lockPath := filepath.Join(root, lockFilename)

	if err := os.Symlink(dummyTarget, lockPath); err != nil {
		t.Fatal(err)
	}

	t.Run("symlink_lock_file", func(t *testing.T) {
		lease, err := AcquireLease(t.Context(), root, syntheticSocket)
		if lease != nil {
			_ = lease.Close()

			t.Fatal("expected nil lease")
		}

		if err == nil {
			t.Fatal("expected error on symlink lock file")
		}
	})

	_ = os.Remove(lockPath)

	if err := os.Link(dummyTarget, lockPath); err != nil {
		t.Fatal(err)
	}

	t.Run("hardlink_lock_file", func(t *testing.T) {
		lease, err := AcquireLease(t.Context(), root, syntheticSocket)
		if lease != nil {
			_ = lease.Close()

			t.Fatal("expected nil lease")
		}

		if !errors.Is(err, privatefs.ErrUnsafe) {
			t.Fatalf("expected ErrUnsafe for hardlink, got %v", err)
		}
	})
}

func TestLeaseUnsafeLockRejection_FIFOAndPermissions(t *testing.T) {
	tempDir := t.TempDir()
	syntheticSocket := filepath.Join(tempDir, "synthetic.sock")

	hash := sha256.Sum256([]byte(syntheticSocket))
	lockFilename := hex.EncodeToString(hash[:]) + ".lock"

	root := filepath.Join(tempDir, "valid_root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}

	lockPath := filepath.Join(root, lockFilename)

	if err := unix.Mkfifo(lockPath, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("fifo_lock_file", func(t *testing.T) {
		lease, err := AcquireLease(t.Context(), root, syntheticSocket)
		if lease != nil {
			_ = lease.Close()

			t.Fatal("expected nil lease")
		}

		if !errors.Is(err, privatefs.ErrUnsafe) {
			t.Fatalf("expected ErrUnsafe for fifo, got %v", err)
		}
	})

	_ = os.Remove(lockPath)

	if err := os.WriteFile(lockPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(lockPath, 0o666); err != nil {
		t.Fatal(err)
	}

	t.Run("unsafe_lock_file_permissions_0666", func(t *testing.T) {
		lease, err := AcquireLease(t.Context(), root, syntheticSocket)
		if lease != nil {
			_ = lease.Close()

			t.Fatal("expected nil lease")
		}

		if !errors.Is(err, privatefs.ErrUnsafe) {
			t.Fatalf("expected ErrUnsafe for 0666 permissions, got %v", err)
		}
	})
}

func TestLeaseDifferentEndpointsIndependent(t *testing.T) {
	leaseRoot := filepath.Join(t.TempDir(), "leases")
	tempDir := t.TempDir()

	sock1 := filepath.Join(tempDir, "sock1.sock")
	sock2 := filepath.Join(tempDir, "sock2.sock")

	lease1, err := AcquireLease(t.Context(), leaseRoot, sock1)
	if err != nil {
		t.Fatalf("acquire lease1: %v", err)
	}

	defer func() {
		_ = lease1.Close()
	}()

	lease2, err := AcquireLease(t.Context(), leaseRoot, sock2)
	if err != nil {
		t.Fatalf("acquire lease2 concurrently: %v", err)
	}

	defer func() {
		_ = lease2.Close()
	}()

	if err := lease1.Close(); err != nil {
		t.Fatalf("close lease1: %v", err)
	}

	if err := lease2.Close(); err != nil {
		t.Fatalf("close lease2: %v", err)
	}
}

func TestLeaseSimultaneousHandlesSameEndpoint(t *testing.T) {
	leaseRoot := filepath.Join(t.TempDir(), "leases")
	tempDir := t.TempDir()
	sock := filepath.Join(tempDir, "test.sock")

	lease1, err := AcquireLease(t.Context(), leaseRoot, sock)
	if err != nil {
		t.Fatalf("acquire initial lease: %v", err)
	}

	timeoutCtx, cancelTimeout := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancelTimeout()

	lease2, err := AcquireLease(timeoutCtx, leaseRoot, sock)
	if lease2 != nil {
		_ = lease2.Close()

		t.Fatal("expected lease2 to fail while lease1 is held")
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}

	oopsErr, ok := oops.AsOops(err)
	if !ok || oopsErr.Code() != "operation_timed_out" {
		t.Fatalf("expected oops code operation_timed_out, got %v", err)
	}

	if err := lease1.Close(); err != nil {
		t.Fatalf("close lease1: %v", err)
	}

	if err := lease1.Close(); err != nil {
		t.Fatalf("repeated close failed: %v", err)
	}

	lease3, err := AcquireLease(t.Context(), leaseRoot, sock)
	if err != nil {
		t.Fatalf("acquire lease3 after release: %v", err)
	}

	if err := lease3.Close(); err != nil {
		t.Fatalf("close lease3: %v", err)
	}
}

func TestLeaseCanceledWaiterCannotReleaseOthersLock(t *testing.T) {
	leaseRoot := filepath.Join(t.TempDir(), "leases")
	tempDir := t.TempDir()
	sock := filepath.Join(tempDir, "test.sock")

	leaseA, err := AcquireLease(t.Context(), leaseRoot, sock)
	if err != nil {
		t.Fatalf("acquire leaseA: %v", err)
	}

	defer func() {
		_ = leaseA.Close()
	}()

	cancelCtx, cancelWaiter := context.WithCancel(t.Context())

	waiterAcquired := make(chan struct{})

	go func() {
		defer close(waiterAcquired)

		leaseB, waitErr := AcquireLease(cancelCtx, leaseRoot, sock)
		if leaseB != nil {
			_ = leaseB.Close()

			t.Errorf("waiter unexpectedly acquired lease")
		}

		if !errors.Is(waitErr, context.Canceled) {
			t.Errorf("expected Canceled, got %v", waitErr)
		}
	}()

	cancelWaiter()
	<-waiterAcquired

	probeCtx, probeCancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer probeCancel()

	leaseC, err := AcquireLease(probeCtx, leaseRoot, sock)
	if leaseC != nil {
		_ = leaseC.Close()

		t.Fatal("leaseA was improperly released by canceled waiter")
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected probe to time out, got %v", err)
	}

	if err := leaseA.Close(); err != nil {
		t.Fatalf("close leaseA: %v", err)
	}

	leaseC, err = AcquireLease(t.Context(), leaseRoot, sock)
	if err != nil {
		t.Fatalf("acquire leaseC after leaseA closed: %v", err)
	}

	if err := leaseC.Close(); err != nil {
		t.Fatalf("close leaseC: %v", err)
	}
}

func getLockInode(t *testing.T, path string) uint64 {
	t.Helper()

	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat lock file %s: %v", path, err)
	}

	sysStat, ok := stat.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("failed to get syscall.Stat_t")
	}

	return sysStat.Ino
}

func TestLeasePermanentLockInodeStableAcrossReleases(t *testing.T) {
	leaseRoot := filepath.Join(t.TempDir(), "leases")
	tempDir := t.TempDir()
	sock := filepath.Join(tempDir, "stable.sock")

	hash := sha256.Sum256([]byte(sock))
	lockPath := filepath.Join(leaseRoot, hex.EncodeToString(hash[:])+".lock")

	lease1, err := AcquireLease(t.Context(), leaseRoot, sock)
	if err != nil {
		t.Fatalf("acquire lease1: %v", err)
	}

	ino1 := getLockInode(t, lockPath)

	if err := lease1.Close(); err != nil {
		t.Fatalf("close lease1: %v", err)
	}

	inoReleased := getLockInode(t, lockPath)
	if inoReleased != ino1 {
		t.Fatalf("inode changed after close: %v vs %v", inoReleased, ino1)
	}

	lease2, err := AcquireLease(t.Context(), leaseRoot, sock)
	if err != nil {
		t.Fatalf("acquire lease2: %v", err)
	}

	ino2 := getLockInode(t, lockPath)
	if ino2 != ino1 {
		t.Fatalf("inode changed across acquisitions: %d vs %d", ino2, ino1)
	}

	if err := lease2.Close(); err != nil {
		t.Fatalf("close lease2: %v", err)
	}
}

func TestLeaseNoLogsLeak(t *testing.T) {
	leaseRoot := filepath.Join(t.TempDir(), "leases")
	tempDir := t.TempDir()
	secretSocketName := "secret-private-endpoint.sock"
	sock := filepath.Join(tempDir, secretSocketName)

	oldStdout := os.Stdout
	oldStderr := os.Stderr

	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	os.Stdout = wOut
	os.Stderr = wErr

	lease, acquireErr := AcquireLease(t.Context(), leaseRoot, sock)

	var closeErr error
	if lease != nil {
		closeErr = lease.Close()
	}

	_, invalidErr := AcquireLease(t.Context(), leaseRoot, filepath.Join(tempDir, "nonexistent", "sock"))

	_ = wOut.Close()
	_ = wErr.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr

	outBytes, _ := io.ReadAll(rOut)
	errBytes, _ := io.ReadAll(rErr)

	_ = rOut.Close()
	_ = rErr.Close()

	if len(outBytes) != 0 {
		t.Fatalf("stdout leaked: %q", string(outBytes))
	}

	if len(errBytes) != 0 {
		t.Fatalf("stderr leaked: %q", string(errBytes))
	}

	if acquireErr != nil {
		t.Fatalf("unexpected acquireErr: %v", acquireErr)
	}

	if closeErr != nil {
		t.Fatalf("unexpected closeErr: %v", closeErr)
	}

	if invalidErr == nil {
		t.Fatal("expected invalidErr")
	}

	if strings.Contains(invalidErr.Error(), secretSocketName) {
		t.Fatalf("error leaked secret socket name: %s", invalidErr.Error())
	}

	if strings.Contains(invalidErr.Error(), tempDir) {
		t.Fatalf("error leaked tempDir: %s", invalidErr.Error())
	}
}

func TestLeasePostLockRevalidation(t *testing.T) {
	leaseRoot := filepath.Join(t.TempDir(), "leases")
	tempDir := t.TempDir()
	sock := filepath.Join(tempDir, "revalidate.sock")

	afterLock := func() error {
		if err := os.WriteFile(sock, []byte("not_a_socket"), 0o600); err != nil {
			return fmt.Errorf("change endpoint fixture: %w", err)
		}

		return nil
	}

	lease, err := acquireLease(t.Context(), leaseRoot, sock, afterLock)
	if lease != nil {
		_ = lease.Close()

		t.Fatal("expected acquisition to fail on post-lock revalidation")
	}

	if !errors.Is(err, ErrEndpoint) {
		t.Fatalf("expected ErrEndpoint on revalidation, got %v", err)
	}
}

func TestLeaseCloseStateRetainsError(t *testing.T) {
	root := t.TempDir()

	lease, err := AcquireLease(t.Context(), filepath.Join(root, "leases"), filepath.Join(root, "socket"))
	if err != nil {
		t.Fatal(err)
	}

	if err := lease.file.Close(); err != nil {
		t.Fatal(err)
	}

	err1 := lease.Close()
	if !errors.Is(err1, os.ErrClosed) {
		t.Fatalf("expected retained close error, got %v", err1)
	}

	err2 := lease.Close()
	if !errors.Is(err2, os.ErrClosed) {
		t.Fatalf("expected identical retained error on subsequent close, got %v", err2)
	}
}
