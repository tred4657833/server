//go:build linux

package work

import (
	"io/fs"
	"os/user"
	"strconv"
	"sync"
	"syscall"
)

var (
	ownerMu    sync.Mutex
	ownerCache = map[uint32]string{}
)

// owner devuelve el nombre del usuario dueño del archivo (con caché).
func owner(in fs.FileInfo) string {
	st, ok := in.Sys().(*syscall.Stat_t)
	if !ok {
		return "?"
	}
	ownerMu.Lock()
	defer ownerMu.Unlock()
	if n, ok := ownerCache[st.Uid]; ok {
		return n
	}
	name := strconv.Itoa(int(st.Uid))
	if u, err := user.LookupId(name); err == nil {
		name = u.Username
	}
	ownerCache[st.Uid] = name
	return name
}
