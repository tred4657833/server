// Package state guarda el estado conocido de Tawanzu (proyectos, dominios, módulos).
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"

	"tawanzu/internal/runner"
)

type Project struct {
	Name     string    `json:"name"`
	Repo     string    `json:"repo"`
	Branch   string    `json:"branch,omitempty"`
	Kind     string    `json:"kind,omitempty"`
	Port     int       `json:"port,omitempty"`
	Domain   string    `json:"domain,omitempty"`
	Service  string    `json:"service,omitempty"`
	User     string    `json:"user,omitempty"` // usuario de sistema propio del proyecto
	Created  time.Time `json:"created"`
	Deployed time.Time `json:"deployed,omitempty"`
}

type Domain struct {
	Name    string `json:"name"`
	Port    int    `json:"port"`
	HTTPS   bool   `json:"https"`
	Project string `json:"project,omitempty"`
	Kind    string `json:"kind"` // proxy | static | php
	Root    string `json:"root,omitempty"`
	Email   string `json:"email,omitempty"`
}

// Published es un puerto publicado con `tawanzu add port`: Nginx escucha en Public y reenvía a
// 127.0.0.1:Backend. La clave del mapa es el puerto público en texto.
type Published struct {
	Public  int       `json:"public"`
	Backend int       `json:"backend"`
	From    string    `json:"from,omitempty"`
	Default bool      `json:"default,omitempty"` // --index: la app ocupa el sitio por defecto de Nginx (sites-available/default)
	Created time.Time `json:"created"`
}

type State struct {
	Profile   string                `json:"profile,omitempty"`
	Installed []string              `json:"installed"`
	Projects  map[string]*Project   `json:"projects"`
	Domains   map[string]*Domain    `json:"domains"`
	LastIPs   map[string]string     `json:"last_ips,omitempty"`  // última IP local/pública vista por `tawanzu view`
	Ports     []string              `json:"ports,omitempty"`     // puertos abiertos con `tawanzu add port --raw`
	Published map[string]*Published `json:"published,omitempty"` // puertos publicados tras Nginx con `tawanzu add port`
	History   []string              `json:"history"`

	base []byte // instantánea JSON del estado al cargarlo: sirve para fusionar cambios al guardar
}

var mu sync.Mutex

// Home devuelve el directorio de datos de Tawanzu.
func Home() string {
	if v := os.Getenv("TAWANZU_HOME"); v != "" {
		return v
	}
	if os.Geteuid() == 0 {
		return "/var/lib/tawanzu"
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".tawanzu")
}

// ProjectsRoot es donde viven los proyectos.
func ProjectsRoot() string {
	if v := os.Getenv("TAWANZU_PROJECTS"); v != "" {
		return v
	}
	if os.Geteuid() == 0 {
		return "/srv/tawanzu"
	}
	return filepath.Join(Home(), "projects")
}

func file() string { return filepath.Join(Home(), "state.json") }

func Load() *State {
	mu.Lock()
	defer mu.Unlock()
	s := &State{Projects: map[string]*Project{}, Domains: map[string]*Domain{}}
	if b, err := os.ReadFile(file()); err == nil {
		if err := json.Unmarshal(b, s); err != nil {
			// Antes se ignoraba el error y el siguiente Save() sobrescribía todo con un estado vacío.
			bad := fmt.Sprintf("%s.corrupt-%s", file(), time.Now().Format("20060102-150405"))
			_ = os.Rename(file(), bad)
			fmt.Fprintf(os.Stderr, "⚠ state.json ilegible (%v): se conservó en %s y se empieza con un estado vacío\n", err, bad)
			s = &State{Projects: map[string]*Project{}, Domains: map[string]*Domain{}}
		}
	}
	if s.Projects == nil {
		s.Projects = map[string]*Project{}
	}
	if s.Domains == nil {
		s.Domains = map[string]*Domain{}
	}
	if s.Published == nil {
		s.Published = map[string]*Published{}
	}
	s.base = snapshot(s)
	return s
}

func snapshot(s *State) []byte {
	b, _ := json.Marshal(s)
	return b
}

// readDisk lee el estado actual del disco sin tocar nada (vacío si no existe o es ilegible).
func readDisk() *State {
	d := &State{Projects: map[string]*Project{}, Domains: map[string]*Domain{}}
	if b, err := os.ReadFile(file()); err == nil {
		_ = json.Unmarshal(b, d)
	}
	if d.Projects == nil {
		d.Projects = map[string]*Project{}
	}
	if d.Domains == nil {
		d.Domains = map[string]*Domain{}
	}
	if d.Published == nil {
		d.Published = map[string]*Published{}
	}
	return d
}

// merge aplica sobre 'disk' solo lo que este proceso cambió respecto a 'base' (fusión de tres vías),
// de modo que los cambios guardados por otro proceso entre Load() y Save() no se pierdan.
func merge(base, mine, disk *State) *State {
	for k, v := range mine.Projects {
		if b, ok := base.Projects[k]; !ok || !reflect.DeepEqual(b, v) {
			disk.Projects[k] = v
		}
	}
	for k := range base.Projects {
		if _, ok := mine.Projects[k]; !ok {
			delete(disk.Projects, k)
		}
	}
	for k, v := range mine.Domains {
		if b, ok := base.Domains[k]; !ok || !reflect.DeepEqual(b, v) {
			disk.Domains[k] = v
		}
	}
	for k := range base.Domains {
		if _, ok := mine.Domains[k]; !ok {
			delete(disk.Domains, k)
		}
	}
	if disk.Published == nil {
		disk.Published = map[string]*Published{}
	}
	for k, v := range mine.Published {
		if b, ok := base.Published[k]; !ok || !reflect.DeepEqual(b, v) {
			disk.Published[k] = v
		}
	}
	for k := range base.Published {
		if _, ok := mine.Published[k]; !ok {
			delete(disk.Published, k)
		}
	}
	if mine.Profile != base.Profile {
		disk.Profile = mine.Profile
	}
	for _, n := range mine.Installed {
		disk.MarkInstalled(n)
	}
	for k, v := range mine.LastIPs {
		if base.LastIPs[k] != v {
			if disk.LastIPs == nil {
				disk.LastIPs = map[string]string{}
			}
			disk.LastIPs[k] = v
		}
	}
	for _, p := range mine.Ports {
		disk.AddPort(p)
	}
	inBase := map[string]bool{}
	for _, h := range base.History {
		inBase[h] = true
	}
	for _, h := range mine.History {
		if !inBase[h] {
			disk.History = append(disk.History, h)
		}
	}
	if len(disk.History) > 200 {
		disk.History = disk.History[len(disk.History)-200:]
	}
	return disk
}

// Save escribe el estado de forma atómica y segura entre procesos: toma un flock, relee el disco,
// fusiona solo los cambios hechos por este proceso desde Load() y escribe el resultado.
func (s *State) Save() error {
	if runner.DryRun {
		return nil // las simulaciones nunca modifican el estado
	}
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(Home(), 0o755); err != nil {
		return err
	}
	unlock, err := lockFile(filepath.Join(Home(), "state.lock"))
	if err != nil {
		return fmt.Errorf("no se pudo bloquear el estado: %w", err)
	}
	defer unlock()

	out := s
	if s.base != nil {
		var base State
		if json.Unmarshal(s.base, &base) == nil {
			if base.Projects == nil {
				base.Projects = map[string]*Project{}
			}
			if base.Domains == nil {
				base.Domains = map[string]*Domain{}
			}
			out = merge(&base, s, readDisk())
		}
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", file(), os.Getpid())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, file()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// El estado de este proceso pasa a reflejar lo que hay en disco y la nueva base.
	*s = State{Profile: out.Profile, Installed: out.Installed, Projects: out.Projects, Domains: out.Domains, LastIPs: out.LastIPs, Ports: out.Ports, Published: out.Published, History: out.History}
	s.base = snapshot(s)
	return nil
}

func (s *State) Log(msg string) {
	s.History = append(s.History, time.Now().Format(time.RFC3339)+" "+msg)
	if len(s.History) > 200 {
		s.History = s.History[len(s.History)-200:]
	}
}

func (s *State) MarkInstalled(name string) {
	for _, n := range s.Installed {
		if n == name {
			return
		}
	}
	s.Installed = append(s.Installed, name)
	sort.Strings(s.Installed)
}

// AddPort registra un puerto abierto ("1555/tcp" o "1555/tcp desde 10.0.0.5"), sin repetir.
func (s *State) AddPort(spec string) {
	for _, p := range s.Ports {
		if p == spec {
			return
		}
	}
	s.Ports = append(s.Ports, spec)
	sort.Strings(s.Ports)
}

func (s *State) IsInstalled(name string) bool {
	for _, n := range s.Installed {
		if n == name {
			return true
		}
	}
	return false
}

func (s *State) UsedPorts() map[int]bool {
	m := map[int]bool{}
	for _, p := range s.Projects {
		if p.Port > 0 {
			m[p.Port] = true
		}
	}
	for _, d := range s.Domains {
		if d.Port > 0 {
			m[d.Port] = true
		}
	}
	for _, p := range s.Published {
		m[p.Backend] = true
	}
	return m
}

// Publish registra un puerto publicado tras Nginx.
func (s *State) Publish(p Published) {
	if s.Published == nil {
		s.Published = map[string]*Published{}
	}
	s.Published[fmt.Sprint(p.Public)] = &p
}

// Unpublish olvida un puerto publicado y lo devuelve (nil si no existía).
func (s *State) Unpublish(public int) *Published {
	k := fmt.Sprint(public)
	p := s.Published[k]
	delete(s.Published, k)
	return p
}

// ResetInfra olvida todo lo que dejó `init`, `add port` y los dominios (módulos instalados, perfil,
// puertos, publicaciones y dominios) para que `tawanzu init` empiece de cero. Los proyectos y el
// historial se conservan. Escribe directamente en disco: la fusión de Save() solo sabe añadir.
func (s *State) ResetInfra() error {
	if runner.DryRun {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(Home(), 0o755); err != nil {
		return err
	}
	unlock, err := lockFile(filepath.Join(Home(), "state.lock"))
	if err != nil {
		return fmt.Errorf("no se pudo bloquear el estado: %w", err)
	}
	defer unlock()
	d := readDisk()
	d.Profile, d.Installed, d.Ports = "", []string{}, nil
	d.Published, d.Domains = nil, map[string]*Domain{}
	for _, p := range d.Projects {
		p.Domain = ""
	}
	d.History = append(d.History, time.Now().Format(time.RFC3339)+" remove all")
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", file(), os.Getpid())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, file()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	*s = State{Profile: d.Profile, Installed: d.Installed, Projects: d.Projects, Domains: d.Domains, LastIPs: d.LastIPs, Ports: d.Ports, Published: d.Published, History: d.History}
	s.base = snapshot(s)
	return nil
}
