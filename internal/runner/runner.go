// Package runner ejecuta comandos del sistema con soporte de dry-run, registro y copias de seguridad.
package runner

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"tawanzu/internal/ui"
)

var (
	DryRun  bool
	Verbose bool
)

// Has indica si un binario existe en PATH.
func Has(bin string) bool { _, err := exec.LookPath(bin); return err == nil }

// Run ejecuta un comando y devuelve su salida combinada.
func Run(name string, args ...string) (string, error) {
	line := name + " " + strings.Join(args, " ")
	if DryRun {
		ui.Plain("  %s %s", ui.Dim("[dry-run]"), line)
		return "", nil
	}
	if Verbose {
		ui.Plain("  %s %s", ui.Dim("$"), line)
	}
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive", "LC_ALL=C")
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	out := strings.TrimSpace(buf.String())
	if err != nil {
		return out, fmt.Errorf("%s: %w\n%s", line, err, tail(out, 12))
	}
	return out, nil
}

// Query ejecuta siempre (incluso en dry-run) comandos de solo lectura.
func Query(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	b, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(b)), err
}

// RunIn ejecuta en un directorio dado con variables extra.
func RunIn(dir string, env []string, name string, args ...string) (string, error) {
	line := name + " " + strings.Join(args, " ")
	if DryRun {
		ui.Plain("  %s (%s) %s", ui.Dim("[dry-run]"), dir, line)
		return "", nil
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	out := strings.TrimSpace(buf.String())
	if err != nil {
		return out, fmt.Errorf("%s: %w\n%s", line, err, tail(out, 15))
	}
	return out, nil
}

// Stream ejecuta conectando stdout/stderr a la terminal (logs, journalctl -f...).
func Stream(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	return cmd.Run()
}

func tail(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// NoChange lo devuelve WriteFile cuando el archivo ya tenía ese contenido.
// Restore lo trata como "no hay nada que revertir": sin esto, un "" ambiguo
// (archivo nuevo vs. sin cambios) hacía que Restore borrara un archivo existente.
const NoChange = "\x00sin-cambios"

// WriteFile escribe un archivo guardando antes una copia del anterior.
// Devuelve la ruta del backup ("" si no existía, NoChange si no hubo cambios).
func WriteFile(path, content string, mode os.FileMode) (string, error) {
	if DryRun {
		ui.Plain("  %s escribir %s (%d bytes)", ui.Dim("[dry-run]"), path, len(content))
		return "", nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	bak := ""
	if old, err := os.ReadFile(path); err == nil {
		if string(old) == content {
			return NoChange, nil
		}
		bak = fmt.Sprintf("%s.tawanzu.bak.%s", path, time.Now().Format("20060102-150405"))
		if err := os.WriteFile(bak, old, mode); err != nil {
			return "", err
		}
	}
	return bak, os.WriteFile(path, []byte(content), mode)
}

// Restore devuelve un archivo a su copia de seguridad (o lo elimina si no había).
func Restore(path, bak string) {
	if DryRun || bak == NoChange {
		return
	}
	if bak == "" {
		_ = os.Remove(path)
		return
	}
	if b, err := os.ReadFile(bak); err == nil {
		_ = os.WriteFile(path, b, 0o644)
	}
}

// ApplyBlock inserta o reemplaza un bloque delimitado por marcadores en un archivo de configuración.
func ApplyBlock(path, tag, body string) (string, error) {
	begin, end := "# >>> tawanzu:"+tag+" >>>", "# <<< tawanzu:"+tag+" <<<"
	old, _ := os.ReadFile(path)
	re := regexp.MustCompile(`(?s)\n?` + regexp.QuoteMeta(begin) + `.*?` + regexp.QuoteMeta(end) + `\n?`)
	clean := re.ReplaceAllString(string(old), "\n")
	return WriteFile(path, strings.TrimRight(clean, "\n")+"\n\n"+begin+"\n"+strings.TrimSpace(body)+"\n"+end+"\n", 0o644)
}
