package modules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogBuiltinsAreWellFormed(t *testing.T) {
	t.Setenv("TAWANZU_HOME", t.TempDir())
	seen := map[string]bool{}
	for _, m := range Catalog() {
		if m.Name == "" || m.Description == "" {
			t.Errorf("módulo incompleto: %+v", m)
		}
		if seen[m.Name] {
			t.Errorf("módulo duplicado: %s", m.Name)
		}
		seen[m.Name] = true
	}
	for _, n := range []string{"node", "php", "docker", "database:postgres", "git"} {
		if _, ok := Find(n); !ok {
			t.Errorf("falta el módulo integrado %q", n)
		}
	}
}

func TestCatalogLoadsExternalModules(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TAWANZU_HOME", home)
	_ = os.MkdirAll(filepath.Join(home, "modules"), 0o755)
	_ = os.WriteFile(filepath.Join(home, "modules", "mio.json"), []byte(`{"name":"mio","description":"externo","ram_mb":10}`), 0o644)
	_ = os.WriteFile(filepath.Join(home, "modules", "roto.json"), []byte(`{no es json`), 0o644)
	_ = os.WriteFile(filepath.Join(home, "modules", "sin-nombre.json"), []byte(`{"description":"x"}`), 0o644)
	m, ok := Find("mio")
	if !ok || m.RAMMB != 10 {
		t.Fatalf("no se cargó el módulo externo: %+v ok=%v", m, ok)
	}
	if _, ok := Find("roto"); ok {
		t.Error("un JSON inválido no debe cargarse")
	}
}

func TestCatalogIsSorted(t *testing.T) {
	t.Setenv("TAWANZU_HOME", t.TempDir())
	c := Catalog()
	for k := 1; k < len(c); k++ {
		if c[k-1].Name > c[k].Name {
			t.Fatalf("catálogo sin ordenar: %s > %s", c[k-1].Name, c[k].Name)
		}
	}
}

func TestExampleModuleFileParses(t *testing.T) {
	b, err := os.ReadFile("../../configs/modulo-ejemplo.json")
	if err != nil {
		t.Skip("sin archivo de ejemplo")
	}
	var m Module
	if err := json.Unmarshal(b, &m); err != nil || m.Name == "" {
		t.Fatalf("el módulo de ejemplo del repositorio no es válido: %v %+v", err, m)
	}
}
