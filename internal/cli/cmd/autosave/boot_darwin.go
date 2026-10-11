//go:build darwin

package autosave

import "golang.org/x/sys/unix"

func bootTimeUnix() (int64, error) {
	boot, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return 0, err
	}

	return boot.Sec, nil
}
