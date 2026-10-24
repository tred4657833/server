//go:build !linux && !darwin

package state

// lockFile no hace nada en plataformas sin flock; Tawanzu está pensado para Linux.
func lockFile(path string) (func(), error) { return func() {}, nil }
