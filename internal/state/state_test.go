package state

import (
	"os"
	"path/filepath"
	"testing"
)

// Regresión: un state.json corrupto no debe perderse al guardar un estado vacío encima.
func TestLoadCorruptStateIsKept(t *testing.T) {
	t.Setenv("TAWANZU_HOME", t.TempDir())
	if err := os.MkdirAll(Home(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file(), []byte("{no es json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Load()
	if len(s.Projects) != 0 || len(s.Domains) != 0 {
		t.Fatal("se esperaba un estado vacío")
	}
	if m, _ := filepath.Glob(file() + ".corrupt-*"); len(m) != 1 {
		t.Fatalf("el archivo corrupto debía conservarse renombrado, hay %d", len(m))
	}
}

// Regresión: dos procesos (CLI y guardián) no deben pisarse los cambios entre Load() y Save().
func TestSaveMergesConcurrentChanges(t *testing.T) {
	t.Setenv("TAWANZU_HOME", t.TempDir())
	a, b := Load(), Load() // dos "procesos" parten del mismo estado
	a.Projects["uno"] = &Project{Name: "uno"}
	b.Domains["d.com"] = &Domain{Name: "d.com", Port: 3000}
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}
	got := Load()
	if got.Projects["uno"] == nil || got.Domains["d.com"] == nil {
		t.Fatalf("se perdió un cambio: proyectos=%v dominios=%v", got.Projects, got.Domains)
	}
	// Borrados también se propagan, sin resucitar lo que otro proceso eliminó.
	c := Load()
	delete(c.Projects, "uno")
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if Load().Projects["uno"] != nil {
		t.Fatal("el proyecto borrado reapareció")
	}
}

func TestSaveRepeatedKeepsWorking(t *testing.T) {
	t.Setenv("TAWANZU_HOME", t.TempDir())
	s := Load()
	s.Projects["x"] = &Project{Name: "x", Port: 1}
	s.Log("uno")
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.Projects["x"].Port = 2
	s.Log("dos")
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	got := Load()
	if got.Projects["x"].Port != 2 || len(got.History) != 2 {
		t.Fatalf("estado inesperado: %+v hist=%v", got.Projects["x"], got.History)
	}
}
