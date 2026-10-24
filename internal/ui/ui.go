// Package ui contiene utilidades de terminal: colores, mensajes y preguntas.
package ui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
	"text/tabwriter"
)

var (
	// AssumeYes responde "sí" automáticamente a todas las confirmaciones.
	AssumeYes bool
	mu        sync.Mutex
	in        = bufio.NewReader(os.Stdin)
	useColor  = isTTY(os.Stdout) && os.Getenv("NO_COLOR") == ""
)

func isTTY(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func c(code, s string) string {
	if !useColor {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

func Bold(s string) string   { return c("1", s) }
func Dim(s string) string    { return c("2", s) }
func Red(s string) string    { return c("31", s) }
func Green(s string) string  { return c("32", s) }
func Yellow(s string) string { return c("33", s) }
func Blue(s string) string   { return c("34", s) }
func Cyan(s string) string   { return c("36", s) }

func out(s string) { mu.Lock(); fmt.Println(s); mu.Unlock() }

func Info(f string, a ...any)  { out(Blue("ℹ ") + fmt.Sprintf(f, a...)) }
func OK(f string, a ...any)    { out(Green("✔ ") + fmt.Sprintf(f, a...)) }
func Warn(f string, a ...any)  { out(Yellow("⚠ ") + fmt.Sprintf(f, a...)) }
func Err(f string, a ...any)   { out(Red("✘ ") + fmt.Sprintf(f, a...)) }
func Step(f string, a ...any)  { out(Cyan("→ ") + fmt.Sprintf(f, a...)) }
func Plain(f string, a ...any) { out(fmt.Sprintf(f, a...)) }

// Title imprime un encabezado.
func Title(s string) {
	out("")
	out(Bold(s))
	out(Dim(strings.Repeat("─", len([]rune(s)))))
}

// Ask pregunta un valor con valor por defecto.
func Ask(prompt, def string) string {
	if AssumeYes {
		return def
	}
	if def != "" {
		fmt.Printf("%s [%s]: ", prompt, def)
	} else {
		fmt.Printf("%s: ", prompt)
	}
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// Confirm devuelve true si el usuario responde S/s/Y/y/sí.
func Confirm(prompt string, defYes bool) bool {
	if AssumeYes {
		return true
	}
	hint := "S/n"
	if !defYes {
		hint = "s/N"
	}
	fmt.Printf("%s [%s]: ", prompt, hint)
	line, _ := in.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	switch line {
	case "":
		return defYes
	case "s", "si", "sí", "y", "yes":
		return true
	}
	return false
}

// ReadLine lee una línea cruda (para el shell interactivo y editores).
func ReadLine(prompt string) (string, bool) {
	fmt.Print(prompt)
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	return strings.TrimRight(line, "\r\n"), true
}

// Table imprime una tabla alineada.
func Table(rows [][]string) {
	mu.Lock()
	defer mu.Unlock()
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	w.Flush()
}

// Bar dibuja una barra de porcentaje.
func Bar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(pct / 100 * float64(width))
	b := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	switch {
	case pct >= 90:
		return Red(b)
	case pct >= 70:
		return Yellow(b)
	}
	return Green(b)
}

// Bytes formatea bytes en unidades legibles.
func Bytes(n uint64) string {
	const u = 1024.0
	f := float64(n)
	for _, s := range []string{"B", "KiB", "MiB", "GiB", "TiB"} {
		if f < u || s == "TiB" {
			if s == "B" {
				return fmt.Sprintf("%d B", n)
			}
			return fmt.Sprintf("%.1f %s", f, s)
		}
		f /= u
	}
	return ""
}
