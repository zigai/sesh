//go:build linux

package autosave

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"strconv"
)

var errBootTimeMissing = errors.New("boot time missing from /proc/stat")

func bootTimeUnix() (int64, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, err
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		value, found := bytes.CutPrefix(scanner.Bytes(), []byte("btime "))
		if found {
			return strconv.ParseInt(string(bytes.TrimSpace(value)), 10, 64)
		}
	}

	return 0, errBootTimeMissing
}
