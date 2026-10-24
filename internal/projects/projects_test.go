package projects

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tawanzu/internal/state"
	"tawanzu/internal/system"
)

func TestParseClone(t *testing.T) {
	cases := []struct {
		args          []string
		name, url, br string
		wantErr       bool
	}{
		{[]string{"name:miapp", "respo:usuario", "repositorio"}, "miapp", "https://github.com/usuario/repositorio.git", "", false},
		{[]string{"respo:usuario/repo"}, "repo", "https://github.com/usuario/repo.git", "", false},
		{[]string{"https://github.com/u/Repo.git", "branch:dev"}, "repo", "https://github.com/u/Repo.git", "dev", false},
		{[]string{"repositorio"}, "", "", "", true},
		{[]string{"name:Mala Ruta", "respo:u", "r"}, "", "", "", true},
	}
	for _, c := range cases {
		n, u, b, err := ParseClone(c.args)
		if (err != nil) != c.wantErr || n != c.name || u != c.url || b != c.br {
			t.Errorf("ParseClone(%v) = %q %q %q %v", c.args, n, u, b, err)
		}
	}
}

// Regresión: dos proyectos Docker no deben compartir imagen ni nombre de contenedor.
func TestDetectDockerNamesPerProject(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Detect(d)
	if c.Kind != "docker" {
		t.Fatalf("tipo = %q, se esperaba docker", c.Kind)
	}
	got := expand(c.Start, "blog")
	if strings.Contains(got, "tawanzu-app") || !strings.Contains(got, "--name tawanzu-blog") {
		t.Errorf("nombre de contenedor no específico del proyecto: %s", got)
	}
}

func TestProjectUserLengthAndUniqueness(t *testing.T) {
	if got := projectUser("blog"); got != "tawanzu-blog" {
		t.Errorf("projectUser(blog) = %q", got)
	}
	a := projectUser(strings.Repeat("a", 40) + "1")
	b := projectUser(strings.Repeat("a", 40) + "2")
	if len(a) > 32 || len(b) > 32 {
		t.Errorf("los nombres de usuario no pueden pasar de 32 caracteres: %q %q", a, b)
	}
	if a == b {
		t.Error("dos proyectos con prefijo común no deben compartir usuario")
	}
}

func TestOwnerOf(t *testing.T) {
	if got := ownerOf(&state.Project{User: "tawanzu-blog"}, "node"); got != "tawanzu-blog" {
		t.Errorf("owner = %q", got)
	}
	if got := ownerOf(&state.Project{}, "node"); got != "tawanzu" {
		t.Errorf("proyecto antiguo sin usuario debe seguir con 'tawanzu', got %q", got)
	}
	if got := ownerOf(&state.Project{User: "x"}, "docker"); got != "root" {
		t.Errorf("docker = %q", got)
	}
}

func TestDockerExposedPorts(t *testing.T) {
	d := t.TempDir()
	compose := `services:
  db:
    image: postgres
    ports:
      - "5432:5432"
      - "127.0.0.1:5433:5432"
  web:
    image: nginx
    ports:
    - 8080:80/tcp
    - "0.0.0.0:9090:90"
  cache:
    image: redis
    expose:
      - "6379"
`
	if err := os.WriteFile(filepath.Join(d, "docker-compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(DockerExposedPorts(d), " | ")
	for _, want := range []string{"5432:5432", "8080:80", "9090:90"} {
		if !strings.Contains(got, want) {
			t.Errorf("falta %s en %q", want, got)
		}
	}
	if strings.Contains(got, "5433") || strings.Contains(got, "6379") {
		t.Errorf("no deben avisar los puertos ligados a 127.0.0.1 ni 'expose': %q", got)
	}
}

func TestPortConflict(t *testing.T) {
	st := &state.State{
		Projects: map[string]*state.Project{"a": {Name: "a", Port: 3000}, "b": {Name: "b", Port: 3001}},
		Domains:  map[string]*state.Domain{"x.com": {Name: "x.com", Port: 4000, Project: "otro"}},
	}
	i := &system.Info{Listening: map[int]string{5000: "postgres"}}
	if err := portConflict(st, i, "b", 3000); err == nil {
		t.Error("el puerto de otro proyecto debe rechazarse")
	}
	if err := portConflict(st, i, "b", 4000); err == nil {
		t.Error("el puerto de un dominio de otro proyecto debe rechazarse")
	}
	if err := portConflict(st, i, "b", 5000); err == nil {
		t.Error("un puerto que ya escucha otro programa debe rechazarse")
	}
	if err := portConflict(st, i, "b", 3001); err != nil {
		t.Errorf("su propio puerto no es un conflicto: %v", err)
	}
	if err := portConflict(st, i, "b", 3999); err != nil {
		t.Errorf("puerto libre rechazado: %v", err)
	}
}
