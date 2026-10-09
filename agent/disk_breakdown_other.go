//go:build !(linux || darwin || freebsd)

package agent

import "io/fs"

func fileDiskUsage(info fs.FileInfo) uint64 {
	return uint64(max(info.Size(), 0))
}
