package services

import (
	"strings"
	"testing"
)

func TestRenderHardening(t *testing.T) {
	u := Unit{Name: "x", Description: "d", WorkDir: "/", Exec: "/bin/true"}
	if !strings.Contains(u.Render(), "ProtectSystem=full") {
		t.Error("las unidades deben endurecerse por defecto")
	}
	u.NoHarden = true
	if strings.Contains(u.Render(), "ProtectSystem") || strings.Contains(u.Render(), "NoNewPrivileges") {
		t.Error("NoHarden no debe incluir restricciones de escritura")
	}
	if !strings.Contains(u.Render(), "[Install]") {
		t.Error("falta la sección [Install]")
	}
}

// Regresión: "%" y "$" tienen significado para systemd y rompían la unidad (o se expandían mal).
func TestRenderEscapesSystemdSpecials(t *testing.T) {
	u := Unit{Name: "x", Description: "d", WorkDir: "/", Exec: "node app.js --fmt 100% --home $HOME",
		Env: map[string]string{"FMT": "50%d"}}
	out := u.Render()
	if !strings.Contains(out, "100%% --home $$HOME") {
		t.Errorf("Exec sin escapar:\n%s", out)
	}
	if !strings.Contains(out, "Environment=FMT=50%%d") {
		t.Errorf("Environment sin escapar:\n%s", out)
	}
}

func TestRenderQuotesSingleQuotes(t *testing.T) {
	u := Unit{Name: "x", Description: "d", WorkDir: "/", Exec: "echo 'hola mundo'"}
	if !strings.Contains(u.Render(), `exec echo '\''hola mundo'\'''`) {
		t.Errorf("comillas simples mal escapadas:\n%s", u.Render())
	}
}
