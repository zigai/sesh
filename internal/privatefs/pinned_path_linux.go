//go:build linux

package privatefs

import "strconv"

func (d *Dir) PinnedPath(name string) (string, error) {
	if !validName(name) {
		return "", ErrUnsafe
	}

	return "/proc/self/fd/" + strconv.FormatUint(uint64(d.file.Fd()), 10) + "/" + name, nil
}
