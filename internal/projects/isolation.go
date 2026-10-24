package projects

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"tawanzu/internal/runner"
	"tawanzu/internal/state"
	"tawanzu/internal/system"
)

// projectUser devuelve el usuario de sistema propio de un proyecto (aislamiento entre proyectos).
// Linux limita los nombres a 32 caracteres: los largos se acortan con un sufijo hash para no colisionar.
func projectUser(name string) string {
	u := "tawanzu-" + name
	if len(u) <= 32 {
		return u
	}
	h := fnv.New32a()
	h.Write([]byte(name))
	return fmt.Sprintf("%s-%08x", u[:23], h.Sum32())
}

// ownerOf indica con qué usuario se ejecuta un proyecto. Los proyectos preparados con versiones
// anteriores (sin usuario propio guardado) siguen con el usuario compartido "tawanzu" hasta que se
// vuelva a ejecutar 'project setup', que les crea el suyo.
func ownerOf(p *state.Project, kind string) string {
	switch kind {
	case "php", "static":
		return webUser()
	case "docker":
		return "root"
	}
	if p.User != "" {
		return p.User
	}
	return "tawanzu"
}

// lockDown deja el directorio del proyecto legible solo por su usuario: sin esto, cualquier otro
// usuario del sistema (incluido el de otro proyecto comprometido) podría leer código, .env y datos.
func lockDown(name, owner string) {
	if os.Geteuid() != 0 || runner.DryRun || owner == "" || owner == "root" {
		return
	}
	d := dir(name)
	_, _ = runner.Run("chown", "root:"+owner, d)
	_, _ = runner.Run("chmod", "0750", d)
}

// portConflict comprueba un puerto fijado (p. ej. con 'port:' en tawanzu.yml) contra los proyectos y
// dominios ya registrados y contra lo que escucha ahora mismo en el servidor.
func portConflict(st *state.State, i *system.Info, name string, port int) error {
	for _, o := range st.Projects {
		if o.Name != name && o.Port == port {
			return fmt.Errorf("el puerto %d ya lo usa el proyecto %q: cambia 'port' en tawanzu.yml", port, o.Name)
		}
	}
	for _, d := range st.Domains {
		if d.Port == port && d.Project != name {
			return fmt.Errorf("el puerto %d ya lo usa el dominio %s: cambia 'port' en tawanzu.yml", port, d.Name)
		}
	}
	if who, ok := i.Listening[port]; ok {
		own := st.Projects[name]
		// Si ya es este proyecto el que escucha (re-ejecución de setup) no hay conflicto.
		if own == nil || own.Port != port {
			return fmt.Errorf("el puerto %d ya está en uso en el servidor (%s): cambia 'port' en tawanzu.yml", port, who)
		}
	}
	return nil
}

var (
	portsKey  = regexp.MustCompile(`^\s*ports:\s*$`)
	portEntry = regexp.MustCompile(`^\s*-\s*["']?(?:(\d{1,3}(?:\.\d{1,3}){3}):)?(\d+(?:-\d+)?):(\d+(?:-\d+)?)(?:/\w+)?["']?\s*(?:#.*)?$`)
)

// DockerExposedPorts devuelve los puertos que un docker-compose publica en todas las interfaces.
// Docker inserta sus propias reglas de iptables y salta ufw, así que 'ports: "5432:5432"' deja el
// puerto abierto a Internet aunque el firewall diga lo contrario. La forma segura es "127.0.0.1:5432:5432".
func DockerExposedPorts(repo string) []string {
	var out []string
	for _, f := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
		b, err := os.ReadFile(filepath.Join(repo, f))
		if err != nil {
			continue
		}
		in := false
		for _, line := range strings.Split(string(b), "\n") {
			t := strings.TrimSpace(line)
			switch {
			case portsKey.MatchString(line):
				in = true
			case in && strings.HasPrefix(t, "-"):
				if m := portEntry.FindStringSubmatch(line); m != nil && (m[1] == "" || m[1] == "0.0.0.0") {
					out = append(out, fmt.Sprintf("%s (en %s)", m[2]+":"+m[3], f))
				}
			case in && t != "" && !strings.HasPrefix(t, "#"):
				in = false
			}
		}
	}
	return out
}
