// Package update descarga la última versión de Tawanzu desde GitHub, la compila e instala.
package update

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"tawanzu/internal/runner"
	"tawanzu/internal/state"
	"tawanzu/internal/ui"
)

// DefaultURL es el zip de la rama main. Se puede cambiar con TAWANZU_UPDATE_URL.
const DefaultURL = "https://github.com/tred4657833/server/archive/refs/heads/main.zip"

const maxZip = 200 << 20 // 200 MB: de sobra para el código fuente

// Run ejecuta: descargar → descomprimir → renombrar a "server" → make build → make install.
func Run(keep bool) error {
	if os.Geteuid() != 0 && !runner.DryRun {
		return fmt.Errorf("este comando necesita root: usa sudo, o --dry-run para simular")
	}
	url := DefaultURL
	if v := os.Getenv("TAWANZU_UPDATE_URL"); v != "" {
		url = v
	}
	work := filepath.Join(state.Home(), "update")
	ui.Title("Actualizar Tawanzu")
	ui.Plain("Origen:  %s", url)
	if runner.DryRun {
		for _, s := range []string{"descargar el zip", "descomprimir", "renombrar la carpeta a server", "cd server && make build", "cd server && make install"} {
			ui.Plain("  %s %s", ui.Dim("[dry-run]"), s)
		}
		return nil
	}
	for _, bin := range []string{"make", "go"} {
		if !runner.Has(bin) {
			hint := "sudo apt install make"
			if bin == "go" {
				hint = "sudo tawanzu add go"
			}
			return fmt.Errorf("falta %q para compilar: instálalo con `%s`", bin, hint)
		}
	}
	if err := os.RemoveAll(work); err != nil {
		return err
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	if !keep {
		defer os.RemoveAll(work)
	}

	ui.Step("descargando")
	zipPath := filepath.Join(work, "main.zip")
	if err := download(url, zipPath); err != nil {
		return err
	}

	ui.Step("descomprimiendo")
	top, commit, err := Unzip(zipPath, work)
	if err != nil {
		return err
	}
	dir := filepath.Join(work, "server")
	if err := os.Rename(filepath.Join(work, top), dir); err != nil {
		return err
	}
	if commit != "" {
		ui.Info("versión del repositorio: %s", short(commit))
	}
	if _, err := os.Stat(filepath.Join(dir, "Makefile")); err != nil {
		return fmt.Errorf("el repositorio no tiene Makefile en su raíz")
	}

	env := append(os.Environ(), "GOCACHE="+filepath.Join(work, "gocache"), "GOTOOLCHAIN=local")
	for _, target := range []string{"build", "install"} {
		ui.Step("make %s", target)
		cmd := exec.Command("make", target)
		cmd.Dir, cmd.Env = dir, env
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("make %s falló (la versión instalada no se tocó si fue en build): %w", target, err)
		}
	}
	ui.OK("Tawanzu actualizado")
	if out, err := runner.Query("/usr/local/bin/tawanzu", "version"); err == nil {
		ui.Plain("   %s", out)
	}
	return nil
}

func short(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

func download(url, dst string) error {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://127.0.0.1") && !strings.HasPrefix(url, "http://localhost") {
		return fmt.Errorf("la URL de actualización debe ser https://")
	}
	c := &http.Client{Timeout: 3 * time.Minute}
	resp, err := c.Get(url)
	if err != nil {
		return fmt.Errorf("no se pudo descargar: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("la descarga devolvió %s", resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxZip+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > maxZip {
		return fmt.Errorf("el zip supera %d MB", maxZip>>20)
	}
	return nil
}

// Unzip extrae zipPath en dest (rechazando rutas que escapen de dest) y devuelve el nombre de la
// única carpeta raíz del zip y el comentario del zip (GitHub pone ahí el hash del commit).
func Unzip(zipPath, dest string) (top, comment string, err error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", "", fmt.Errorf("zip inválido: %w", err)
	}
	defer r.Close()
	root, err := filepath.Abs(dest)
	if err != nil {
		return "", "", err
	}
	tops := map[string]bool{}
	var total uint64
	for _, f := range r.File {
		name := filepath.ToSlash(f.Name)
		first, _, _ := strings.Cut(name, "/")
		if first == "" {
			continue
		}
		tops[first] = true
		target := filepath.Join(root, filepath.FromSlash(name))
		if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return "", "", fmt.Errorf("ruta peligrosa en el zip: %q", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", "", err
			}
			continue
		}
		if !f.Mode().IsRegular() {
			continue // enlaces simbólicos y archivos especiales se ignoran
		}
		total += f.UncompressedSize64
		if total > 4*maxZip {
			return "", "", fmt.Errorf("el zip descomprimido es demasiado grande")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", "", err
		}
		if err := extract(f, target); err != nil {
			return "", "", err
		}
	}
	if len(tops) != 1 {
		return "", "", fmt.Errorf("se esperaba una sola carpeta raíz en el zip y hay %d", len(tops))
	}
	for t := range tops {
		top = t
	}
	return top, strings.TrimSpace(r.Comment), nil
}

func extract(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	mode := f.Mode().Perm() | 0o600
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, io.LimitReader(rc, int64(f.UncompressedSize64)+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}
