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

func TestAddPortNoDuplicatesAndMerge(t *testing.T) {
	t.Setenv("TAWANZU_HOME", t.TempDir())
	a := Load()
	a.AddPort("1555/tcp")
	a.AddPort("1555/tcp")
	if len(a.Ports) != 1 {
		t.Fatalf("no debe repetir puertos: %v", a.Ports)
	}
	b := Load() // otro proceso carga antes de que A guarde
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	b.AddPort("8080/udp")
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}
	got := Load().Ports
	if len(got) != 2 || got[0] != "1555/tcp" || got[1] != "8080/udp" {
		t.Errorf("la fusión debe conservar ambos puertos: %v", got)
	}
}

func TestLastIPsPersistAndMerge(t *testing.T) {
	t.Setenv("TAWANZU_HOME", t.TempDir())
	a := Load()
	a.LastIPs = map[string]string{"public": "1.1.1.1"}
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	b, c := Load(), Load()
	b.LastIPs["public"] = "2.2.2.2"
	c.LastIPs["local"] = "10.0.0.5"
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got := Load().LastIPs
	if got["public"] != "2.2.2.2" || got["local"] != "10.0.0.5" {
		t.Errorf("la fusión debe conservar ambos cambios: %v", got)
	}
}

func TestPublishedMergeAndUsedPorts(t *testing.T) {
	t.Setenv("TAWANZU_HOME", t.TempDir())
	a, b := Load(), Load()
	a.Publish(Published{Public: 1555, Backend: 1555})
	b.Publish(Published{Public: 3001, Backend: 3000})
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}
	c := Load()
	if len(c.Published) != 2 {
		t.Fatalf("la fusión perdió puertos publicados: %v", c.Published)
	}
	if u := c.UsedPorts(); !u[1555] || !u[3000] {
		t.Errorf("UsedPorts debe incluir los puertos de las apps publicadas: %v", u)
	}
	if p := c.Unpublish(1555); p == nil || p.Backend != 1555 {
		t.Fatalf("Unpublish: %v", p)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if d := Load(); len(d.Published) != 1 || d.Published["3001"] == nil {
		t.Errorf("el borrado no se guardó: %v", d.Published)
	}
}

// `remove all` debe vaciar lo de infraestructura pero conservar los proyectos.
func TestResetInfraKeepsProjects(t *testing.T) {
	t.Setenv("TAWANZU_HOME", t.TempDir())
	s := Load()
	s.Profile = "web"
	s.MarkInstalled("base")
	s.AddPort("1555/tcp")
	s.Publish(Published{Public: 80, Backend: 1555})
	s.Domains["d.com"] = &Domain{Name: "d.com", Port: 3000}
	s.Projects["uno"] = &Project{Name: "uno", Domain: "d.com"}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetInfra(); err != nil {
		t.Fatal(err)
	}
	got := Load()
	if got.Profile != "" || len(got.Installed) != 0 || len(got.Ports) != 0 || len(got.Published) != 0 || len(got.Domains) != 0 {
		t.Fatalf("la infraestructura debía quedar vacía: %+v", got)
	}
	if got.Projects["uno"] == nil || got.Projects["uno"].Domain != "" {
		t.Fatalf("el proyecto debe conservarse sin dominio: %+v", got.Projects["uno"])
	}
	if got.IsInstalled("base") {
		t.Fatal("init debe poder ejecutarse de nuevo")
	}
}
