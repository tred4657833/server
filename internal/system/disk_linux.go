//go:build linux

package system

import "syscall"

type Disk struct {
	Total, Free, InodesTotal, InodesFree uint64
}

// DiskUsage devuelve espacio e inodos de un punto de montaje.
func DiskUsage(path string) Disk {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return Disk{}
	}
	bs := uint64(s.Bsize)
	return Disk{
		Total: s.Blocks * bs, Free: s.Bavail * bs,
		InodesTotal: s.Files, InodesFree: s.Ffree,
	}
}
