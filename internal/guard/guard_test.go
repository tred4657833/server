package guard

import (
	"testing"
	"time"
)

func newTracker() *tracker {
	return &tracker{fails: map[string]int{}, restarts: map[string][]time.Time{}, renewals: map[string]time.Time{}}
}

func TestCanRestartLimitsToFivePerHour(t *testing.T) {
	tr := newTracker()
	for k := 0; k < 5; k++ {
		if !tr.canRestart("app") {
			t.Fatalf("reinicio %d debería permitirse", k+1)
		}
		tr.noteRestart("app")
	}
	if tr.canRestart("app") {
		t.Error("el sexto reinicio en una hora debe bloquearse (evita bucles)")
	}
	if !tr.canRestart("otra") {
		t.Error("el límite es por servicio, no global")
	}
}

func TestCanRestartForgetsOldRestarts(t *testing.T) {
	tr := newTracker()
	old := time.Now().Add(-2 * time.Hour)
	tr.restarts["app"] = []time.Time{old, old, old, old, old}
	if !tr.canRestart("app") {
		t.Error("los reinicios de hace más de una hora no deben contar")
	}
	if len(tr.restarts["app"]) != 0 {
		t.Error("los reinicios antiguos deben descartarse")
	}
}

func TestCanRenewOncePerSixHours(t *testing.T) {
	tr := newTracker()
	if !tr.canRenew("a.com") {
		t.Fatal("el primer intento debe permitirse")
	}
	if tr.canRenew("a.com") {
		t.Error("un segundo intento inmediato agotaría los límites de Let's Encrypt")
	}
	if !tr.canRenew("b.com") {
		t.Error("el límite es por dominio")
	}
	tr.renewals["a.com"] = time.Now().Add(-7 * time.Hour)
	if !tr.canRenew("a.com") {
		t.Error("pasadas 6 horas debe volver a permitirse")
	}
}

func TestTrackerIsSafeForConcurrentUse(t *testing.T) {
	tr := newTracker()
	done := make(chan struct{})
	for k := 0; k < 20; k++ {
		go func() {
			tr.canRestart("app")
			tr.noteRestart("app")
			tr.canRenew("d")
			done <- struct{}{}
		}()
	}
	for k := 0; k < 20; k++ {
		<-done
	}
}
