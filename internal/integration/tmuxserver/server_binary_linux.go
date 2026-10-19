//go:build linux

package tmuxserver

import (
	"bufio"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// unixListening is __SO_ACCEPTCON in /proc/net/unix flags: the socket accepts connections.
	unixListening        = 0x10000
	maxServerDescriptors = 65536
)

// serverBinary returns the executable of the tmux server listening on socket when $TMUX
// names that server, and "" otherwise. Commands then run the exact binary the server runs,
// skipping any PATH wrapper around tmux. The candidate pid is trusted only while it holds
// the listening socket, so a stale $TMUX or a reused pid never selects another program.
func serverBinary(socket string) string {
	return resolveServerBinary(os.Getenv("TMUX"), socket, os.DirFS("/proc"))
}

func resolveServerBinary(tmuxEnvironment, socket string, proc fs.FS) string {
	pid, ok := tmuxServerPID(tmuxEnvironment, socket)
	if !ok {
		return ""
	}

	inodes := listeningSocketInodes(proc, socket)
	if len(inodes) == 0 || !holdsSocket(proc, path.Join(pid, "fd"), inodes) {
		return ""
	}

	executable, err := fs.ReadLink(proc, path.Join(pid, "exe"))
	if err != nil || !filepath.IsAbs(executable) || strings.HasSuffix(executable, " (deleted)") {
		return ""
	}

	return executable
}

// tmuxServerPID parses $TMUX ("socket,pid,session") and returns the pid when the socket matches.
func tmuxServerPID(tmuxEnvironment, socket string) (string, bool) {
	fields := strings.Split(tmuxEnvironment, ",")
	if len(fields) != 3 || filepath.Clean(fields[0]) != socket {
		return "", false
	}

	pid, err := strconv.Atoi(fields[1])
	if err != nil || pid <= 0 {
		return "", false
	}

	return strconv.Itoa(pid), true
}

func listeningSocketInodes(proc fs.FS, socket string) map[string]bool {
	file, err := proc.Open("net/unix")
	if err != nil {
		return nil
	}

	defer func() { _ = file.Close() }()

	inodes := make(map[string]bool)
	scanner := bufio.NewScanner(file)

	// Rows are "Num RefCount Protocol Flags Type St Inode Path".
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 8 || fields[7] != socket {
			continue
		}

		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err == nil && flags&unixListening != 0 {
			inodes[fields[6]] = true
		}
	}

	return inodes
}

func holdsSocket(proc fs.FS, descriptors string, inodes map[string]bool) bool {
	entries, err := fs.ReadDir(proc, descriptors)
	if err != nil || len(entries) > maxServerDescriptors {
		return false
	}

	for _, entry := range entries {
		target, err := fs.ReadLink(proc, path.Join(descriptors, entry.Name()))
		if err != nil {
			continue
		}

		inode, ok := strings.CutPrefix(target, "socket:[")
		if ok && inodes[strings.TrimSuffix(inode, "]")] {
			return true
		}
	}

	return false
}
