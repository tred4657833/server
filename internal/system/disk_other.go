//go:build !linux

package system

type Disk struct {
	Total, Free, InodesTotal, InodesFree uint64
}

// DiskUsage no está soportado fuera de Linux.
func DiskUsage(path string) Disk { return Disk{} }
