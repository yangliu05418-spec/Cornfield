//go:build linux

package diskspace

import (
	"errors"

	"golang.org/x/sys/unix"
)

func FreePercent(path string) (float64, error) {
	var stats unix.Statfs_t
	if err := unix.Statfs(path, &stats); err != nil {
		return 0, err
	}
	if stats.Flags&unix.ST_RDONLY != 0 {
		return 0, errors.New("asset filesystem is read-only")
	}
	if stats.Blocks == 0 {
		return 0, nil
	}
	return float64(stats.Bavail) / float64(stats.Blocks) * 100, nil
}
