package service

import "syscall"

// deviceOf is the device a path lives on.
func deviceOf(path string) (uint64, bool) {
	var stat syscall.Stat_t
	if err := syscall.Stat(path, &stat); err != nil {
		return 0, false
	}

	return uint64(stat.Dev), true //nolint:unconvert // the type of Dev differs by architecture
}
