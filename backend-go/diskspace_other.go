//go:build !darwin && !linux

package main

import (
	"errors"
	"runtime"
)

// diskUsage has no implementation outside macOS and Linux, which are also the
// only platforms findUSBMountPoint knows how to scan for a card.
func diskUsage(string) (total, free uint64, err error) {
	return 0, 0, errors.New("reading free space is not supported on " + runtime.GOOS)
}
