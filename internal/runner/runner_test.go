package runner

import (
	"os"
	"path/filepath"
	"testing"
)

// Regresión: Restore no debe borrar un archivo cuyo contenido no cambió.
func TestRestoreNoChangeKeepsFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "conf")
	if _, err := WriteFile(p, "x", 0o644); err != nil {
		t.Fatal(err)
	}
	bak, err := WriteFile(p, "x", 0o644)
	if err != nil || bak != NoChange {
		t.Fatalf("WriteFile con el mismo contenido = %q, %v; se esperaba NoChange", bak, err)
	}
	Restore(p, bak)
	if _, err := os.Stat(p); err != nil {
		t.Fatal("Restore borró un archivo que no había cambiado")
	}
}

func TestRestoreNewFileIsRemoved(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nuevo")
	bak, err := WriteFile(p, "x", 0o644)
	if err != nil || bak != "" {
		t.Fatalf("archivo nuevo: bak=%q err=%v", bak, err)
	}
	Restore(p, bak)
	if _, err := os.Stat(p); err == nil {
		t.Fatal("un archivo creado por Tawanzu debía eliminarse al revertir")
	}
}

func TestRestoreFromBackup(t *testing.T) {
	p := filepath.Join(t.TempDir(), "conf")
	_, _ = WriteFile(p, "a", 0o644)
	bak, err := WriteFile(p, "b", 0o644)
	if err != nil || bak == "" || bak == NoChange {
		t.Fatalf("se esperaba ruta de backup, hubo %q (%v)", bak, err)
	}
	Restore(p, bak)
	if b, _ := os.ReadFile(p); string(b) != "a" {
		t.Fatalf("contenido restaurado = %q, se esperaba %q", b, "a")
	}
}
