//go:build !linux

package work

import "io/fs"

func owner(in fs.FileInfo) string { return "-" }
