package tune

import (
	"testing"

	"tawanzu/internal/system"
)

func TestAnalyze(t *testing.T) {
	small := Analyze(&system.Info{MemTotal: 512 * MiB, CPUs: 1})
	if small.Profile != "VPS mínimo" || small.SwapMB == 0 {
		t.Errorf("perfil pequeño incorrecto: %+v", small)
	}
	big := Analyze(&system.Info{MemTotal: 16 * GiB, CPUs: 8, SwapTotal: GiB})
	if big.Profile != "VPS profesional" || big.SwapMB != 0 || big.NginxConns != 8192 || big.PGSharedBuf != "4096MB" {
		t.Errorf("perfil grande incorrecto: %+v", big)
	}
}

func TestFreePort(t *testing.T) {
	if p := FreePort(map[int]bool{3000: true}, map[int]string{3001: "tcp"}); p != 3002 {
		t.Errorf("got %d", p)
	}
}
