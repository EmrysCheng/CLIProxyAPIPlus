//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strconv"
)

func testProcessImage(pid int) (string, error) {
	return os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
}
