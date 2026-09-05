//go:build darwin || linux

package main

import "syscall"

// diskUsage reports the size of the filesystem mounted at path and how much of
// it can still be written to. The available-block count is used rather than the
// free-block count so the number matches what df reports as "Avail": on
// filesystems that hold blocks back for root, the two differ and only the
// former is space the user can actually fill with photos.
func diskUsage(path string) (total, free uint64, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, err
	}
	// Bsize is uint32 on darwin and int64 on linux; widen it either way.
	blockSize := uint64(stat.Bsize)
	return stat.Blocks * blockSize, stat.Bavail * blockSize, nil
}
