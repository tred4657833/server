// Package work implementa `tawanzu work`: un gestor de archivos para terminal.
// Permite listar (/L), navegar, ver, editar, copiar, mover y eliminar con protecciones,
// y usa goroutines para buscar, medir y limpiar directorios grandes rápidamente.
package work

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tawanzu/internal/ui"
)

// Shell mantiene el directorio actual.
type Shell struct{ cwd string }

// protegidos: nunca se eliminan (ni su contenido directo) sin salir de Tawanzu.
var protected = map[string]bool{
	"/": true, "/bin": true, "/sbin": true, "/usr": true, "/etc": true, "/boot": true, "/lib": true,
	"/lib64": true, "/var": true, "/home": true, "/root": true, "/dev": true, "/proc": true, "/sys": true,
	"/opt": true, "/srv": true, "/run": true, "/mnt": true, "/media": true,
}

// Main es el punto de entrada de `tawanzu work`.
//
//	work /L [ruta]       lista y sale
//	work <comando> ...   ejecuta un comando y sale
//	work [ruta]          shell interactivo
func Main(args []string) error {
	s := &Shell{}
	s.cwd, _ = os.Getwd()
	if len(args) > 0 && isListFlag(args[0]) {
		return s.cmdLs(append([]string{"-l"}, args[1:]...))
	}
	if len(args) > 0 && commands[args[0]] != nil {
		return s.exec(args)
	}
	if len(args) > 0 {
		p := s.resolve(args[0])
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			return fmt.Errorf("%s no es un directorio", args[0])
		}
		s.cwd = p
	}
	return s.repl()
}

func isListFlag(a string) bool { return a == "/L" || a == "/l" || a == "-L" || a == "--list" }

type handler func(*Shell, []string) error

var commands map[string]handler

func init() {
	commands = map[string]handler{
		"/L": (*Shell).cmdLs, "/l": (*Shell).cmdLs, "ls": (*Shell).cmdLs, "ll": (*Shell).cmdLs, "dir": (*Shell).cmdLs,
		"cd": (*Shell).cmdCd, "pwd": (*Shell).cmdPwd, "cat": (*Shell).cmdCat, "view": (*Shell).cmdView,
		"head": (*Shell).cmdHead, "tail": (*Shell).cmdTail, "edit": (*Shell).cmdEdit,
		"rm": (*Shell).cmdRm, "del": (*Shell).cmdRm, "rmdir": (*Shell).cmdRm,
		"mkdir": (*Shell).cmdMkdir, "touch": (*Shell).cmdTouch, "cp": (*Shell).cmdCp, "mv": (*Shell).cmdMv,
		"chmod": (*Shell).cmdChmod, "find": (*Shell).cmdFind, "grep": (*Shell).cmdGrep, "du": (*Shell).cmdDu,
		"tree": (*Shell).cmdTree, "stat": (*Shell).cmdStat, "clean": (*Shell).cmdClean, "junk": (*Shell).cmdClean,
		"help": (*Shell).cmdHelp, "?": (*Shell).cmdHelp, "clear": func(*Shell, []string) error { fmt.Print("\033[2J\033[H"); return nil },
	}
}

func (s *Shell) repl() error {
	ui.Plain("%s  (escribe %s para ver comandos, %s para salir)", ui.Bold("Tawanzu work"), ui.Cyan("help"), ui.Cyan("exit"))
	_ = s.cmdLs([]string{"-l"})
	for {
		line, ok := ui.ReadLine(fmt.Sprintf("%s %s ", ui.Cyan("tawanzu:"+s.short()), ui.Bold("›")))
		if !ok {
			fmt.Println()
			return nil
		}
		args := splitArgs(line)
		if len(args) == 0 {
			continue
		}
		if args[0] == "exit" || args[0] == "quit" || args[0] == "q" {
			return nil
		}
		if err := s.exec(args); err != nil {
			ui.Err("%v", err)
		}
	}
}

func (s *Shell) exec(args []string) error {
	h := commands[args[0]]
	if h == nil {
		return fmt.Errorf("comando desconocido %q (help)", args[0])
	}
	return h(s, args[1:])
}

func (s *Shell) short() string {
	if h, err := os.UserHomeDir(); err == nil && strings.HasPrefix(s.cwd, h) {
		return "~" + strings.TrimPrefix(s.cwd, h)
	}
	return s.cwd
}

// resolve convierte ~ y rutas relativas en absolutas.
func (s *Shell) resolve(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		h, _ := os.UserHomeDir()
		p = filepath.Join(h, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.cwd, p)
	}
	return filepath.Clean(p)
}

// splitArgs separa por espacios respetando comillas.
func splitArgs(line string) []string {
	var out []string
	var cur strings.Builder
	var q rune
	in := false
	for _, r := range line {
		switch {
		case q != 0:
			if r == q {
				q = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			q, in = r, true
		case r == ' ' || r == '\t':
			if cur.Len() > 0 || in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 || in {
		out = append(out, cur.String())
	}
	return out
}

func flags(args []string) (fl map[string]bool, rest []string) {
	fl = map[string]bool{}
	for _, a := range args {
		if len(a) > 1 && a[0] == '-' && !isNumber(a[1:]) {
			for _, c := range a[1:] {
				fl[string(c)] = true
			}
		} else {
			rest = append(rest, a)
		}
	}
	return
}

func isNumber(s string) bool { _, err := strconv.Atoi(s); return err == nil }

// ---------- Listado ----------

func (s *Shell) cmdLs(args []string) error {
	fl, rest := flags(args)
	target := s.cwd
	if len(rest) > 0 {
		target = s.resolve(rest[0])
	}
	st, err := os.Stat(target)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return printLong([]fs.FileInfo{st}, true)
	}
	ents, err := os.ReadDir(target)
	if err != nil {
		return err
	}
	// lstat en paralelo: acelera directorios con miles de entradas.
	infos := make([]fs.FileInfo, len(ents))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU()*4)
	for i, e := range ents {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, e os.DirEntry) {
			defer wg.Done()
			defer func() { <-sem }()
			infos[i], _ = e.Info()
		}(i, e)
	}
	wg.Wait()
	var list []fs.FileInfo
	for _, in := range infos {
		if in == nil || (!fl["a"] && strings.HasPrefix(in.Name(), ".")) {
			continue
		}
		list = append(list, in)
	}
	sort.Slice(list, func(a, b int) bool {
		da, db := list[a].IsDir(), list[b].IsDir()
		if da != db {
			return da
		}
		return strings.ToLower(list[a].Name()) < strings.ToLower(list[b].Name())
	})
	ui.Plain("%s", ui.Dim(target))
	_ = fl
	return printLong(list, false)
}

func printLong(list []fs.FileInfo, full bool) error {
	rows := [][]string{{ui.Bold("PERMISOS"), ui.Bold("DUEÑO"), ui.Bold("TAMAÑO"), ui.Bold("MODIFICADO"), ui.Bold("NOMBRE")}}
	var dirs, files int
	var total int64
	for _, in := range list {
		name := in.Name()
		switch {
		case in.IsDir():
			name = ui.Blue(name + "/")
			dirs++
		case in.Mode()&fs.ModeSymlink != 0:
			name = ui.Cyan(name + " @")
		case in.Mode()&0o111 != 0:
			name = ui.Green(name + "*")
			files++
		default:
			files++
		}
		size := ui.Bytes(uint64(in.Size()))
		if in.IsDir() {
			size = "-"
		}
		total += in.Size()
		rows = append(rows, []string{in.Mode().String(), owner(in), size, in.ModTime().Format("2006-01-02 15:04"), name})
	}
	ui.Table(rows)
	ui.Plain("%s", ui.Dim(fmt.Sprintf("%d directorios, %d archivos, %s", dirs, files, ui.Bytes(uint64(total)))))
	return nil
}

func (s *Shell) cmdCd(args []string) error {
	t := "~"
	if len(args) > 0 {
		t = args[0]
	}
	p := s.resolve(t)
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s no es un directorio", p)
	}
	s.cwd = p
	return nil
}

func (s *Shell) cmdPwd(args []string) error { ui.Plain("%s", s.cwd); return nil }

// ---------- Contenido ----------

func (s *Shell) file(args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("falta el archivo")
	}
	p := s.resolve(args[0])
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("%s es un directorio", p)
	}
	return p, nil
}

func isBinary(b []byte) bool {
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	for _, c := range b[:n] {
		if c == 0 {
			return true
		}
	}
	return false
}

func (s *Shell) cmdCat(args []string) error {
	p, err := s.file(args)
	if err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := f.Read(head)
	if isBinary(head[:n]) {
		return fmt.Errorf("%s parece binario; usa `stat` o `head`", p)
	}
	_, _ = f.Seek(0, 0)
	_, err = io.Copy(os.Stdout, f)
	fmt.Println()
	return err
}

// cmdView pagina el archivo con números de línea.
func (s *Shell) cmdView(args []string) error {
	p, err := s.file(args)
	if err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for n := 1; sc.Scan(); n++ {
		fmt.Printf("%s %s\n", ui.Dim(fmt.Sprintf("%5d │", n)), sc.Text())
		if n%40 == 0 {
			if l, _ := ui.ReadLine(ui.Dim("-- Enter: más, q: salir -- ")); strings.HasPrefix(l, "q") {
				return nil
			}
		}
	}
	return sc.Err()
}

func lineCount(args []string) (int, []string) {
	n := 10
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-n" && i+1 < len(args) {
			n, _ = strconv.Atoi(args[i+1])
			i++
		} else {
			rest = append(rest, args[i])
		}
	}
	return n, rest
}

func (s *Shell) cmdHead(args []string) error {
	n, rest := lineCount(args)
	p, err := s.file(rest)
	if err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for i := 0; i < n && sc.Scan(); i++ {
		fmt.Println(sc.Text())
	}
	return nil
}

func (s *Shell) cmdTail(args []string) error {
	n, rest := lineCount(args)
	p, err := s.file(rest)
	if err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	ring := make([]string, 0, n)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		if len(ring) == n {
			ring = ring[1:]
		}
		ring = append(ring, sc.Text())
	}
	fmt.Println(strings.Join(ring, "\n"))
	return nil
}

func (s *Shell) cmdStat(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("falta la ruta")
	}
	p := s.resolve(args[0])
	st, err := os.Lstat(p)
	if err != nil {
		return err
	}
	ui.Table([][]string{
		{"Ruta", p}, {"Tipo", map[bool]string{true: "directorio", false: "archivo"}[st.IsDir()]},
		{"Tamaño", ui.Bytes(uint64(st.Size()))}, {"Permisos", fmt.Sprintf("%s (%04o)", st.Mode(), st.Mode().Perm())},
		{"Dueño", owner(st)}, {"Modificado", st.ModTime().Format(time.RFC3339)},
	})
	return nil
}

// ---------- Edición ----------

func (s *Shell) cmdEdit(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("falta el archivo")
	}
	p := s.resolve(args[0])
	for _, ed := range []string{os.Getenv("VISUAL"), os.Getenv("EDITOR"), "nano", "vim", "vi"} {
		if ed == "" {
			continue
		}
		if path, err := exec.LookPath(ed); err == nil {
			cmd := exec.Command(path, p)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			return cmd.Run()
		}
	}
	ui.Warn("no hay editor instalado: se usa el editor integrado")
	return builtinEditor(p)
}

// builtinEditor es un editor de líneas mínimo para servidores sin nano/vim.
func builtinEditor(path string) error {
	var lines []string
	if b, err := os.ReadFile(path); err == nil {
		if isBinary(b) {
			return fmt.Errorf("archivo binario")
		}
		lines = strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		if len(b) == 0 {
			lines = nil
		}
	}
	dirty := false
	show := func() {
		for i, l := range lines {
			fmt.Printf("%s %s\n", ui.Dim(fmt.Sprintf("%4d │", i+1)), l)
		}
	}
	ui.Plain("%s", ui.Dim("editor integrado: l=ver  a TEXTO=añadir  i N TEXTO=insertar antes de N  r N TEXTO=reemplazar  d N=borrar  w=guardar  q=salir  wq=guardar y salir"))
	show()
	save := func() error {
		err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
		if err == nil {
			dirty = false
			ui.OK("guardado")
		}
		return err
	}
	for {
		l, ok := ui.ReadLine("edit› ")
		if !ok {
			return nil
		}
		cmd, rest, _ := strings.Cut(strings.TrimSpace(l), " ")
		idx := func() (int, string, bool) {
			ns, txt, _ := strings.Cut(rest, " ")
			n, err := strconv.Atoi(ns)
			if err != nil || n < 1 || n > len(lines)+1 {
				ui.Err("número de línea inválido")
				return 0, "", false
			}
			return n - 1, txt, true
		}
		switch cmd {
		case "l":
			show()
		case "a":
			lines, dirty = append(lines, rest), true
		case "i":
			if n, t, ok := idx(); ok {
				lines = append(lines[:n], append([]string{t}, lines[n:]...)...)
				dirty = true
			}
		case "r":
			if n, t, ok := idx(); ok && n < len(lines) {
				lines[n], dirty = t, true
			}
		case "d":
			if n, _, ok := idx(); ok && n < len(lines) {
				lines, dirty = append(lines[:n], lines[n+1:]...), true
			}
		case "w":
			if err := save(); err != nil {
				return err
			}
		case "wq":
			return save()
		case "q":
			if dirty && !ui.Confirm("hay cambios sin guardar, ¿salir igualmente?", false) {
				continue
			}
			return nil
		default:
			ui.Err("comando de editor desconocido")
		}
	}
}

// ---------- Escritura / eliminación ----------

func (s *Shell) cmdMkdir(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("falta el nombre")
	}
	for _, a := range args {
		if err := os.MkdirAll(s.resolve(a), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (s *Shell) cmdTouch(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("falta el nombre")
	}
	for _, a := range args {
		p := s.resolve(a)
		if _, err := os.Stat(p); err == nil {
			now := time.Now()
			_ = os.Chtimes(p, now, now)
			continue
		}
		f, err := os.Create(p)
		if err != nil {
			return err
		}
		f.Close()
	}
	return nil
}

func (s *Shell) cmdChmod(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("uso: chmod 755 archivo")
	}
	m, err := strconv.ParseUint(args[0], 8, 32)
	if err != nil {
		return fmt.Errorf("modo inválido (usa octal, p. ej. 644)")
	}
	for _, a := range args[1:] {
		if err := os.Chmod(s.resolve(a), fs.FileMode(m)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Shell) cmdMv(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("uso: mv origen destino")
	}
	src, dst := s.resolve(args[0]), s.resolve(args[1])
	if st, err := os.Stat(dst); err == nil && st.IsDir() {
		dst = filepath.Join(dst, filepath.Base(src))
	}
	if isProtected(src) {
		return fmt.Errorf("%s está protegido", src)
	}
	return os.Rename(src, dst)
}

func (s *Shell) cmdCp(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("uso: cp origen destino")
	}
	src, dst := s.resolve(args[0]), s.resolve(args[1])
	if st, err := os.Stat(dst); err == nil && st.IsDir() {
		dst = filepath.Join(dst, filepath.Base(src))
	}
	return copyTree(src, dst)
}

// copyTree copia archivos y directorios usando un pool de workers.
func copyTree(src, dst string) error {
	type job struct {
		from, to string
		mode     fs.FileMode
	}
	jobs := make(chan job, 64)
	var wg sync.WaitGroup
	var firstErr atomic.Value
	for w := 0; w < runtime.NumCPU()*2; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if err := copyFile(j.from, j.to, j.mode); err != nil {
					firstErr.CompareAndSwap(nil, err)
				}
			}
		}()
	}
	walkErr := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(to, info.Mode().Perm()|0o700)
		}
		if info.Mode().IsRegular() {
			jobs <- job{p, to, info.Mode().Perm()}
		}
		return nil
	})
	close(jobs)
	wg.Wait()
	if walkErr != nil {
		return walkErr
	}
	if e := firstErr.Load(); e != nil {
		return e.(error)
	}
	return nil
}

func copyFile(from, to string, mode fs.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// cmdRm elimina archivos y directorios, midiendo antes lo que se va a borrar.
func (s *Shell) cmdRm(args []string) error {
	fl, rest := flags(args)
	if len(rest) == 0 {
		return fmt.Errorf("falta la ruta")
	}
	for _, a := range rest {
		p := s.resolve(a)
		if isProtected(p) || p == s.homeDir() {
			return fmt.Errorf("%s es una ruta protegida: no se elimina desde Tawanzu", p)
		}
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if st.IsDir() {
			if isGuardedChild(p) {
				return fmt.Errorf("%s es un directorio del sistema: no se elimina de forma recursiva desde Tawanzu", p)
			}
			files, dirs, size := measure(p)
			if !fl["r"] && !fl["f"] && files+dirs > 0 {
				ui.Warn("%s no está vacío (%d archivos, %d subdirectorios, %s); usa `rm -r`", p, files, dirs, ui.Bytes(size))
				continue
			}
			if !fl["f"] && !ui.Confirm(fmt.Sprintf("¿Eliminar el directorio %s con %d archivos y %d subdirectorios (%s)?", p, files, dirs, ui.Bytes(size)), false) {
				continue
			}
			if err := os.RemoveAll(p); err != nil {
				return err
			}
		} else {
			if !fl["f"] && !ui.Confirm(fmt.Sprintf("¿Eliminar el archivo %s (%s)?", p, ui.Bytes(uint64(st.Size()))), false) {
				continue
			}
			if err := os.Remove(p); err != nil {
				return err
			}
		}
		ui.OK("eliminado %s", p)
	}
	return nil
}

// realPath resuelve los enlaces simbólicos de los directorios padre (no del último elemento), para que
// `rm -rf /enlace/etc` no esquive la lista de rutas protegidas.
func realPath(p string) string {
	dir, base := filepath.Split(p)
	if d, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Join(d, base)
	}
	return p
}

func isProtected(p string) bool { return protected[p] || protected[realPath(p)] }

// guardedParents: de estos directorios tampoco se borran recursivamente sus subdirectorios directos
// (p. ej. /etc/ssh, /var/lib, /usr/bin, /home/usuario).
var guardedParents = map[string]bool{
	"/": true, "/bin": true, "/sbin": true, "/usr": true, "/etc": true, "/boot": true, "/lib": true,
	"/lib64": true, "/var": true, "/home": true, "/dev": true, "/proc": true, "/sys": true, "/run": true,
}

func isGuardedChild(p string) bool { return guardedParents[filepath.Dir(realPath(p))] }

func (s *Shell) homeDir() string { h, _ := os.UserHomeDir(); return h }

// measure cuenta archivos, directorios y tamaño total en paralelo.
func measure(root string) (files, dirs int, size uint64) {
	var f, d int64
	var sz uint64
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU()*4)
	var walk func(string)
	walk = func(dir string) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			if e.IsDir() && e.Type()&fs.ModeSymlink == 0 {
				atomic.AddInt64(&d, 1)
				p := filepath.Join(dir, e.Name())
				select {
				case sem <- struct{}{}:
					wg.Add(1)
					go func() { defer wg.Done(); defer func() { <-sem }(); walk(p) }()
				default:
					walk(p)
				}
				continue
			}
			atomic.AddInt64(&f, 1)
			if in, err := e.Info(); err == nil {
				atomic.AddUint64(&sz, uint64(in.Size()))
			}
		}
	}
	walk(root)
	wg.Wait()
	return int(f), int(d), sz
}

// ---------- Búsqueda y análisis ----------

func (s *Shell) cmdDu(args []string) error {
	target := s.cwd
	if len(args) > 0 {
		target = s.resolve(args[0])
	}
	ents, err := os.ReadDir(target)
	if err != nil {
		return err
	}
	type row struct {
		name string
		size uint64
		dir  bool
	}
	rows := make([]row, len(ents))
	var wg sync.WaitGroup
	for i, e := range ents {
		wg.Add(1)
		go func(i int, e os.DirEntry) {
			defer wg.Done()
			if e.IsDir() {
				_, _, sz := measure(filepath.Join(target, e.Name()))
				rows[i] = row{e.Name() + "/", sz, true}
			} else if in, err := e.Info(); err == nil {
				rows[i] = row{e.Name(), uint64(in.Size()), false}
			}
		}(i, e)
	}
	wg.Wait()
	sort.Slice(rows, func(a, b int) bool { return rows[a].size > rows[b].size })
	var total uint64
	for _, r := range rows {
		total += r.size
	}
	out := [][]string{}
	for i, r := range rows {
		if i >= 25 {
			break
		}
		pct := 0.0
		if total > 0 {
			pct = float64(r.size) / float64(total) * 100
		}
		out = append(out, []string{ui.Bytes(r.size), ui.Bar(pct, 20), fmt.Sprintf("%4.1f%%", pct), r.name})
	}
	ui.Table(out)
	ui.Plain("%s", ui.Dim("total "+ui.Bytes(total)))
	return nil
}

// walkParallel recorre un árbol con workers y llama a fn por cada entrada.
func walkParallel(root string, fn func(path string, d fs.DirEntry)) {
	dirs := make(chan string, 1024)
	var pending sync.WaitGroup
	var wg sync.WaitGroup
	push := func(d string) {
		pending.Add(1)
		select {
		case dirs <- d:
		default:
			go func() { dirs <- d }()
		}
	}
	for w := 0; w < runtime.NumCPU()*2; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for d := range dirs {
				ents, _ := os.ReadDir(d)
				for _, e := range ents {
					p := filepath.Join(d, e.Name())
					fn(p, e)
					if e.IsDir() && e.Type()&fs.ModeSymlink == 0 {
						push(p)
					}
				}
				pending.Done()
			}
		}()
	}
	push(root)
	pending.Wait()
	close(dirs)
	wg.Wait()
}

func (s *Shell) cmdFind(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("uso: find patrón [ruta]   (ej: find *.log /var/log)")
	}
	pat := args[0]
	root := s.cwd
	if len(args) > 1 {
		root = s.resolve(args[1])
	}
	var mu sync.Mutex
	var found []string
	walkParallel(root, func(p string, d fs.DirEntry) {
		if ok, _ := filepath.Match(strings.ToLower(pat), strings.ToLower(d.Name())); ok || strings.Contains(strings.ToLower(d.Name()), strings.ToLower(pat)) {
			mu.Lock()
			found = append(found, p)
			mu.Unlock()
		}
	})
	sort.Strings(found)
	for i, p := range found {
		if i >= 200 {
			ui.Warn("... y %d más", len(found)-200)
			break
		}
		fmt.Println(p)
	}
	ui.Plain("%s", ui.Dim(fmt.Sprintf("%d resultados", len(found))))
	return nil
}

func (s *Shell) cmdGrep(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("uso: grep texto [ruta]")
	}
	needle := args[0]
	root := s.cwd
	if len(args) > 1 {
		root = s.resolve(args[1])
	}
	var mu sync.Mutex
	var hits []string
	var count int64
	check := func(p string) {
		f, err := os.Open(p)
		if err != nil {
			return
		}
		defer f.Close()
		head := make([]byte, 512)
		n, _ := f.Read(head)
		if isBinary(head[:n]) {
			return
		}
		_, _ = f.Seek(0, 0)
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<22)
		for ln := 1; sc.Scan(); ln++ {
			if strings.Contains(sc.Text(), needle) {
				atomic.AddInt64(&count, 1)
				mu.Lock()
				if len(hits) < 100 {
					t := strings.TrimSpace(sc.Text())
					if len(t) > 140 {
						t = t[:140] + "…"
					}
					hits = append(hits, fmt.Sprintf("%s:%d: %s", p, ln, t))
				}
				mu.Unlock()
			}
		}
	}
	if st, err := os.Stat(root); err == nil && !st.IsDir() {
		check(root)
	} else {
		sem := make(chan struct{}, runtime.NumCPU()*2)
		var wg sync.WaitGroup
		walkParallel(root, func(p string, d fs.DirEntry) {
			if d.Type().IsRegular() {
				wg.Add(1)
				sem <- struct{}{}
				go func() { defer wg.Done(); defer func() { <-sem }(); check(p) }()
			}
		})
		wg.Wait()
	}
	sort.Strings(hits)
	for _, h := range hits {
		fmt.Println(h)
	}
	ui.Plain("%s", ui.Dim(fmt.Sprintf("%d coincidencias", count)))
	return nil
}

func (s *Shell) cmdTree(args []string) error {
	depth := 2
	root := s.cwd
	for _, a := range args {
		if n, err := strconv.Atoi(a); err == nil {
			depth = n
		} else {
			root = s.resolve(a)
		}
	}
	ui.Plain("%s", ui.Bold(root))
	var rec func(dir, prefix string, lvl int)
	rec = func(dir, prefix string, lvl int) {
		ents, _ := os.ReadDir(dir)
		var vis []os.DirEntry
		for _, e := range ents {
			if !strings.HasPrefix(e.Name(), ".") {
				vis = append(vis, e)
			}
		}
		for i, e := range vis {
			br, next := "├── ", "│   "
			if i == len(vis)-1 {
				br, next = "└── ", "    "
			}
			name := e.Name()
			if e.IsDir() {
				name = ui.Blue(name + "/")
			}
			fmt.Println(prefix + br + name)
			if e.IsDir() && lvl < depth && e.Type()&fs.ModeSymlink == 0 {
				rec(filepath.Join(dir, e.Name()), prefix+next, lvl+1)
			}
		}
	}
	rec(root, "", 1)
	return nil
}

// ---------- Limpieza de cachivos ----------

var junkNames = map[string]bool{
	"__pycache__": true, ".DS_Store": true, "Thumbs.db": true, ".pytest_cache": true, ".cache": true, ".npm": true,
}

func isJunk(name string, d fs.DirEntry, info fs.FileInfo) bool {
	if junkNames[name] {
		return true
	}
	for _, suf := range []string{".tmp", ".bak", "~", ".swp", ".orig", ".rej", ".core"} {
		if strings.HasSuffix(name, suf) {
			return true
		}
	}
	if strings.Contains(name, ".tawanzu.bak.") && info != nil && time.Since(info.ModTime()) > 30*24*time.Hour {
		return true
	}
	if strings.HasSuffix(name, ".log") && info != nil && time.Since(info.ModTime()) > 30*24*time.Hour {
		return true
	}
	return false
}

// cmdClean busca "cachivos" (temporales, backups viejos, cachés, logs antiguos) y pide confirmación para borrarlos.
func (s *Shell) cmdClean(args []string) error {
	root := s.cwd
	if len(args) > 0 {
		root = s.resolve(args[0])
	}
	if isProtected(root) {
		return fmt.Errorf("por seguridad no se escanea %s completo; entra en un subdirectorio", root)
	}
	type item struct {
		path string
		size uint64
		dir  bool
	}
	var mu sync.Mutex
	var items []item
	walkParallel(root, func(p string, d fs.DirEntry) {
		in, _ := d.Info()
		if !isJunk(d.Name(), d, in) {
			return
		}
		var sz uint64
		if d.IsDir() {
			_, _, sz = measure(p)
		} else if in != nil {
			sz = uint64(in.Size())
		}
		mu.Lock()
		items = append(items, item{p, sz, d.IsDir()})
		mu.Unlock()
	})
	// Evita contar dos veces un archivo dentro de un directorio basura.
	sort.Slice(items, func(a, b int) bool { return items[a].path < items[b].path })
	var clean []item
	for _, it := range items {
		if len(clean) > 0 && clean[len(clean)-1].dir && strings.HasPrefix(it.path, clean[len(clean)-1].path+"/") {
			continue
		}
		clean = append(clean, it)
	}
	if len(clean) == 0 {
		ui.OK("no se encontraron cachivos en %s", root)
		return nil
	}
	var total uint64
	for _, it := range clean {
		total += it.size
		t := "archivo"
		if it.dir {
			t = "directorio"
		}
		fmt.Printf("  %-10s %10s  %s\n", t, ui.Bytes(it.size), it.path)
	}
	ui.Plain("%d elementos, %s recuperables", len(clean), ui.Bytes(total))
	if !ui.Confirm("¿Eliminarlos todos?", false) {
		return nil
	}
	var wg sync.WaitGroup
	var removed int64
	for _, it := range clean {
		wg.Add(1)
		go func(it item) {
			defer wg.Done()
			if os.RemoveAll(it.path) == nil {
				atomic.AddInt64(&removed, 1)
			}
		}(it)
	}
	wg.Wait()
	ui.OK("eliminados %d elementos (%s)", removed, ui.Bytes(total))
	return nil
}

func (s *Shell) cmdHelp(args []string) error {
	ui.Table([][]string{
		{ui.Bold("COMANDO"), ui.Bold("DESCRIPCIÓN")},
		{"/L, ls [-a] [ruta]", "listar directorio (permisos, dueño, tamaño, fecha)"},
		{"cd, pwd", "moverse / mostrar directorio actual"},
		{"cat, view, head, tail -n N", "ver contenido (view pagina con números de línea)"},
		{"edit archivo", "editar con $EDITOR/nano/vim o editor integrado"},
		{"mkdir, touch, cp, mv, chmod", "administrar archivos y directorios"},
		{"rm [-r] [-f], rmdir", "eliminar con confirmación y rutas protegidas"},
		{"clean [ruta]", "detectar y borrar cachivos (tmp, bak, cachés, logs viejos)"},
		{"find patrón [ruta]", "buscar por nombre (concurrente)"},
		{"grep texto [ruta]", "buscar dentro de archivos (concurrente)"},
		{"du [ruta]", "qué ocupa más espacio (concurrente)"},
		{"tree [N]", "árbol de directorios"},
		{"stat ruta", "detalle de un archivo"},
		{"exit", "salir"},
	})
	return nil
}
