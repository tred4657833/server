//go:build linux || darwin

package state

import (
	"os"
	"syscall"
)

// lockFile toma un flock exclusivo sobre el archivo de bloqueo y devuelve la función que lo libera.
// Protege entre procesos (CLI y guardián); el mutex de paquete solo protege dentro de un proceso.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
