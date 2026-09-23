//go:build darwin

package agents

import (
	"bytes"
	"encoding/binary"

	"golang.org/x/sys/unix"
)

func agentProcessArguments(pid int) []string {
	data, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(data) < 4 {
		return nil
	}

	count := binary.LittleEndian.Uint32(data[:4])
	data = data[4:]

	end := bytes.IndexByte(data, 0)
	if end < 0 {
		return nil
	}

	data = bytes.TrimLeft(data[end+1:], "\x00")

	var args []string

	for range count {
		end = bytes.IndexByte(data, 0)
		if end < 0 {
			return nil
		}

		args = append(args, string(data[:end]))
		data = data[end+1:]
	}

	return args
}
