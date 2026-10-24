// Package tune calcula valores de configuración adecuados según los recursos reales del servidor.
package tune

import (
	"fmt"

	"tawanzu/internal/system"
)

const (
	MiB = uint64(1) << 20
	GiB = uint64(1) << 30
)

type Tuning struct {
	Profile        string
	NginxConns     int
	NginxRateLimit string
	PGSharedBuf    string
	PGEffCache     string
	PGWorkMem      string
	PGMaxConn      int
	RedisMaxMem    string
	SwapMB         int // swap recomendado a crear (0 = no hace falta)
	FileLimit      int
	Notes          []string
}

func mb(b uint64) uint64 { return b / MiB }

// Analyze decide perfil y parámetros.
func Analyze(i *system.Info) Tuning {
	t := Tuning{}
	ram := i.MemTotal
	switch {
	case ram < 1*GiB:
		t.Profile = "VPS mínimo"
		t.PGMaxConn = 30
		t.Notes = append(t.Notes, "RAM < 1 GiB: se evitarán servicios pesados y se recomienda swap")
	case ram < 4*GiB:
		t.Profile = "VPS estándar"
		t.PGMaxConn = 60
	default:
		t.Profile = "VPS profesional"
		t.PGMaxConn = 150
	}
	t.NginxConns = 1024 * i.CPUs
	if t.NginxConns > 8192 {
		t.NginxConns = 8192
	}
	t.FileLimit = t.NginxConns * 4
	t.NginxRateLimit = "20r/s"

	t.PGSharedBuf = fmt.Sprintf("%dMB", max(64, mb(ram)/4))
	t.PGEffCache = fmt.Sprintf("%dMB", max(128, mb(ram)*6/10))
	t.PGWorkMem = fmt.Sprintf("%dMB", max(2, mb(ram)/uint64(t.PGMaxConn*4)))
	t.RedisMaxMem = fmt.Sprintf("%dmb", max(32, mb(ram)/8))

	if i.SwapTotal == 0 && ram < 4*GiB {
		t.SwapMB = int(min(2048, max(512, mb(ram))))
		t.Notes = append(t.Notes, fmt.Sprintf("Sin swap detectado: se sugiere crear %d MiB para evitar OOM", t.SwapMB))
	}
	return t
}

// FreePort busca un puerto libre a partir de 3000 que no esté ni en uso ni reservado.
func FreePort(reserved map[int]bool, listening map[int]string) int {
	for p := 3000; p < 9000; p++ {
		if _, ok := listening[p]; ok || reserved[p] {
			continue
		}
		return p
	}
	return 0
}

func max[T ~int | ~uint64](a, b T) T {
	if a > b {
		return a
	}
	return b
}

func min[T ~int | ~uint64](a, b T) T {
	if a < b {
		return a
	}
	return b
}
