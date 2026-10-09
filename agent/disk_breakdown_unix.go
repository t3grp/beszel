//go:build linux || darwin || freebsd

package agent

import (
	"io/fs"
	"syscall"
)

// fileDiskUsage returns the space allocated on disk, not the apparent size, so
// sparse files and block rounding match what df and du report.
func fileDiskUsage(info fs.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Blocks) * 512
	}
	return uint64(max(info.Size(), 0))
}
