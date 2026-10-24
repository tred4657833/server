// Package backup crea copias verificables (tar.gz + sha256), con cifrado opcional, retención y restauración.
package backup

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"tawanzu/internal/runner"
	"tawanzu/internal/state"
	"tawanzu/internal/ui"
)

// Encrypt cifra las copias nuevas con AES-256-GCM (clave en KeyPath(), que se crea si no existe).
// También se activa con TAWANZU_BACKUP_ENCRYPT=1.
var Encrypt bool

func Dir() string {
	if v := os.Getenv("TAWANZU_BACKUPS"); v != "" {
		return v
	}
	if os.Geteuid() == 0 {
		return "/var/backups/tawanzu"
	}
	return filepath.Join(state.Home(), "backups")
}

// sources lista lo que se respalda: configuración, estado, .env y datos de proyectos y unidades.
func sources() []string {
	list := []string{state.Home(), "/etc/nginx", "/etc/letsencrypt", "/etc/fail2ban/jail.d", "/etc/ssh/sshd_config.d"}
	units, _ := filepath.Glob("/etc/systemd/system/tawanzu-*.service")
	list = append(list, units...)
	// shared/ guarda el .env; data/ es el directorio de datos de la app (variable DATA_DIR): uploads, SQLite...
	for _, sub := range []string{"shared", "data"} {
		m, _ := filepath.Glob(filepath.Join(state.ProjectsRoot(), "*", sub))
		list = append(list, m...)
	}
	var ok []string
	for _, s := range list {
		if _, err := os.Stat(s); err == nil {
			ok = append(ok, s)
		}
	}
	return ok
}

// excluded indica si p es el propio directorio de copias (o está dentro): evita que un backup incluya a los demás.
func excluded(p string) bool {
	d := filepath.Clean(Dir())
	p = filepath.Clean(p)
	return p == d || strings.HasPrefix(p, d+"/")
}

func wantEncrypt() bool { return Encrypt || os.Getenv("TAWANZU_BACKUP_ENCRYPT") == "1" }

// Create genera el archivo y su suma de verificación. Con db=true incluye un volcado de PostgreSQL.
// Si algún archivo no se pudo leer o el volcado falló, la copia se escribe igualmente pero Create devuelve
// un error: nunca se presenta como correcta una copia incompleta.
func Create(db bool) (string, error) {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return "", err
	}
	enc := wantEncrypt()
	ext := ".tar.gz"
	if enc {
		ext += ".enc"
	}
	name := filepath.Join(Dir(), "tawanzu-"+time.Now().Format("20060102-150405")+ext)
	if runner.DryRun {
		ui.Plain("  [dry-run] crear %s con %v", name, sources())
		return name, nil
	}
	var key []byte
	if enc {
		var err error
		if key, err = loadKey(true); err != nil {
			return "", err
		}
	}
	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	fail := func(err error) (string, error) {
		f.Close()
		_ = os.Remove(name)
		return "", err
	}
	h := sha256.New()
	sink := io.MultiWriter(f, h)
	var ew *encWriter
	var dst io.Writer = sink
	if enc {
		if ew, err = newEncWriter(sink, key); err != nil {
			return fail(err)
		}
		dst = ew
	}
	gz, _ := gzip.NewWriterLevel(dst, gzip.BestSpeed)
	tw := tar.NewWriter(gz)
	count := 0
	var problems []string
	problem := func(f string, a ...any) { problems = append(problems, fmt.Sprintf(f, a...)) }

	for _, src := range sources() {
		walkErr := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if !os.IsNotExist(err) { // un archivo que desaparece durante la copia no es un fallo
					problem("no se pudo leer %s: %v", p, err)
				}
				return nil
			}
			if excluded(p) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			in, err := d.Info()
			if err != nil {
				if !os.IsNotExist(err) {
					problem("no se pudo consultar %s: %v", p, err)
				}
				return nil
			}
			link := ""
			switch {
			case in.Mode()&fs.ModeSymlink != 0:
				// /etc/letsencrypt/live/<dominio>/*.pem son enlaces simbólicos: sin ellos, Nginx no
				// encuentra los certificados tras restaurar y deja de arrancar.
				if link, err = os.Readlink(p); err != nil {
					problem("no se pudo leer el enlace %s: %v", p, err)
					return nil
				}
			case !(in.Mode().IsRegular() || in.IsDir()):
				return nil
			}
			hd, err := tar.FileInfoHeader(in, link)
			if err != nil {
				problem("cabecera inválida para %s: %v", p, err)
				return nil
			}
			hd.Name = strings.TrimPrefix(p, "/")
			if in.IsDir() {
				hd.Name += "/"
			}
			var r *os.File
			if in.Mode().IsRegular() {
				// Se abre antes de escribir la cabecera: si falla, no queda una entrada vacía en el tar.
				if r, err = os.Open(p); err != nil {
					if !os.IsNotExist(err) {
						problem("no se pudo abrir %s: %v", p, err)
					}
					return nil
				}
				defer r.Close()
			}
			if err := tw.WriteHeader(hd); err != nil {
				return fmt.Errorf("escribiendo la copia: %w", err) // error de escritura (¿disco lleno?): se aborta
			}
			if r != nil {
				n, cerr := io.Copy(tw, io.LimitReader(r, hd.Size))
				if n < hd.Size { // el archivo cambió mientras se copiaba: se rellena para no romper el tar
					if _, err := io.CopyN(tw, zeros{}, hd.Size-n); err != nil {
						return fmt.Errorf("escribiendo la copia: %w", err)
					}
					problem("%s cambió durante la copia (se copió %d de %d bytes)", p, n, hd.Size)
				} else if cerr != nil {
					return fmt.Errorf("escribiendo la copia: %w", cerr)
				}
			}
			count++
			return nil
		})
		if walkErr != nil {
			return fail(walkErr)
		}
	}
	if db {
		if err := dumpPostgres(tw, &count); err == errNoPostgres {
			ui.Warn("la copia NO incluye PostgreSQL: pg_dumpall no está instalado")
		} else if err != nil {
			problem("PostgreSQL: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		return fail(err)
	}
	if err := gz.Close(); err != nil {
		return fail(err)
	}
	if ew != nil {
		if err := ew.Close(); err != nil {
			return fail(err)
		}
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if err := os.WriteFile(name+".sha256", []byte(sum+"  "+filepath.Base(name)+"\n"), 0o600); err != nil {
		return "", err
	}
	if len(problems) > 0 {
		for i, p := range problems {
			if i == 10 {
				ui.Warn("... y %d problemas más", len(problems)-10)
				break
			}
			ui.Warn("%s", p)
		}
		return name, fmt.Errorf("copia INCOMPLETA en %s (%d elementos, %d problemas): no se debe confiar en ella", name, count, len(problems))
	}
	extra := ""
	if enc {
		extra = ", cifrada (guarda la clave " + KeyPath() + " FUERA de este servidor)"
	}
	ui.OK("copia creada: %s (%d elementos%s)", name, count, extra)
	return name, nil
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// dumpPostgres vuelca todas las bases a un archivo temporal (no a memoria) y lo añade al tar.
var errNoPostgres = fmt.Errorf("pg_dumpall no está instalado")

func dumpPostgres(tw *tar.Writer, count *int) error {
	if !runner.Has("pg_dumpall") {
		return errNoPostgres
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("hay que ejecutar como root")
	}
	launcher, args := "runuser", []string{"-u", "postgres", "--", "pg_dumpall"}
	if !runner.Has("runuser") {
		launcher, args = "sudo", []string{"-u", "postgres", "pg_dumpall"}
	}
	tmp, err := os.CreateTemp(Dir(), ".pgdump-*")
	if err != nil {
		return err
	}
	defer func() { tmp.Close(); _ = os.Remove(tmp.Name()) }()
	cmd := exec.Command(launcher, args...)
	cmd.Stdout = tmp // solo stdout: stderr corrompería el SQL
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_dumpall falló (%v): %s", err, strings.TrimSpace(stderr.String()))
	}
	st, err := tmp.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: "dumps/postgres.sql", Mode: 0o600, Size: st.Size(), ModTime: time.Now()}); err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := io.Copy(tw, tmp); err != nil {
		return err
	}
	*count++
	return nil
}

// openArchive abre una copia y devuelve el flujo gzip en claro, descifrándola si hace falta.
func openArchive(path string) (io.ReadCloser, io.Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	br := bufio.NewReader(f)
	var r io.Reader = br
	if head, _ := br.Peek(len(encMagic)); string(head) == encMagic {
		key, err := loadKey(false)
		if err != nil {
			f.Close()
			return nil, nil, err
		}
		if r, err = newDecReader(br, key); err != nil {
			f.Close()
			return nil, nil, err
		}
	}
	return f, r, nil
}

// Verify comprueba la suma sha256 y que el tar se pueda leer completo (descifrándolo si está cifrado).
func Verify(path string) error {
	want, err := os.ReadFile(path + ".sha256")
	if err != nil {
		return fmt.Errorf("falta %s.sha256", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	f.Close()
	if err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.HasPrefix(string(want), got) {
		return fmt.Errorf("la suma de verificación no coincide: archivo dañado")
	}
	c, r, err := openArchive(path)
	if err != nil {
		return err
	}
	defer c.Close()
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		if _, err := tr.Next(); err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("tar corrupto: %w", err)
		}
	}
	ui.OK("copia íntegra: %s", filepath.Base(path))
	return nil
}

// Upload copia el archivo y su .sha256 fuera del servidor ejecutando TAWANZU_BACKUP_UPLOAD con sh -c.
// El comando recibe TAWANZU_BACKUP_FILE y TAWANZU_BACKUP_SUM; ejemplo:
//
//	TAWANZU_BACKUP_UPLOAD='rsync -a "$TAWANZU_BACKUP_FILE" "$TAWANZU_BACKUP_SUM" usuario@otro-host:/backups/'
//
// Sin la variable no hace nada y devuelve false.
func Upload(path string) (bool, error) {
	cmdline := os.Getenv("TAWANZU_BACKUP_UPLOAD")
	if cmdline == "" || runner.DryRun {
		return false, nil
	}
	c := exec.Command("sh", "-c", cmdline)
	c.Env = append(os.Environ(), "TAWANZU_BACKUP_FILE="+path, "TAWANZU_BACKUP_SUM="+path+".sha256")
	if out, err := c.CombinedOutput(); err != nil {
		return true, fmt.Errorf("la copia local existe, pero la subida externa falló: %v\n%s", err, strings.TrimSpace(string(out)))
	}
	ui.OK("copia enviada fuera del servidor")
	return true, nil
}

type Item struct {
	Path string
	Size int64
	Time time.Time
}

func List() []Item {
	m, _ := filepath.Glob(filepath.Join(Dir(), "tawanzu-*.tar.gz"))
	e, _ := filepath.Glob(filepath.Join(Dir(), "tawanzu-*.tar.gz.enc"))
	m = append(m, e...)
	sort.Strings(m)
	var items []Item
	for _, p := range m {
		if st, err := os.Stat(p); err == nil {
			items = append(items, Item{p, st.Size(), st.ModTime()})
		}
	}
	// Orden por nombre (marca de tiempo), no por extensión.
	stamp := func(p string) string {
		return strings.TrimSuffix(strings.TrimSuffix(filepath.Base(p), ".enc"), ".tar.gz")
	}
	sort.Slice(items, func(a, b int) bool { return stamp(items[a].Path) < stamp(items[b].Path) })
	return items
}

// Prune conserva las 'keep' copias más recientes.
func Prune(keep int) int {
	items := List()
	n := 0
	for len(items) > keep {
		if !runner.DryRun {
			_ = os.Remove(items[0].Path)
			_ = os.Remove(items[0].Path + ".sha256")
		}
		items = items[1:]
		n++
	}
	return n
}

// restoreAllowed indica si una ruta de destino es de las que Tawanzu respalda. Restore se niega a
// escribir en cualquier otro sitio, aunque el archivo diga lo contrario (defensa ante copias manipuladas).
func restoreAllowed(dst string) bool {
	for _, r := range []string{state.Home(), state.ProjectsRoot(), "/etc/nginx", "/etc/letsencrypt", "/etc/fail2ban/jail.d", "/etc/ssh/sshd_config.d"} {
		r = filepath.Clean(r)
		if dst == r || strings.HasPrefix(dst, r+"/") {
			return true
		}
	}
	return strings.HasPrefix(dst, "/etc/systemd/system/tawanzu-")
}

// Restore extrae una copia verificada sobre /. Solo escribe en las rutas que Tawanzu respalda.
func Restore(path string) error {
	if err := Verify(path); err != nil {
		return err
	}
	if !ui.Confirm("Esto sobrescribirá archivos de configuración actuales. ¿Continuar?", false) {
		return fmt.Errorf("cancelado")
	}
	c, r, err := openArchive(path)
	if err != nil {
		return err
	}
	defer c.Close()
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	restored, skipped := 0, 0
	for {
		hd, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if strings.HasPrefix(hd.Name, "dumps/") {
			continue
		}
		dst := filepath.Clean("/" + hd.Name)
		if !restoreAllowed(dst) {
			ui.Warn("se omite %s: fuera de las rutas que Tawanzu respalda", dst)
			skipped++
			continue
		}
		perm := fs.FileMode(hd.Mode).Perm()
		switch hd.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, perm); err != nil {
				return err
			}
		case tar.TypeSymlink:
			target := hd.Linkname
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(dst), target)
			}
			if !restoreAllowed(filepath.Clean(target)) {
				ui.Warn("se omite el enlace %s → %s: apunta fuera de las rutas respaldadas", dst, hd.Linkname)
				skipped++
				continue
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			_ = os.Remove(dst)
			if err := os.Symlink(hd.Linkname, dst); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			// Si el destino es un enlace, se reemplaza en vez de escribir a través de él.
			if st, err := os.Lstat(dst); err == nil && st.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(dst)
			}
			out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			out.Close()
			if err != nil {
				return err
			}
			_ = os.Chmod(dst, perm)
		default:
			ui.Warn("se omite %s: tipo de entrada no soportado", dst)
			skipped++
			continue
		}
		restored++
	}
	ui.OK("restaurados %d elementos (%d omitidos)", restored, skipped)
	if runner.Has("nginx") {
		if out, err := runner.Query("nginx", "-t"); err != nil {
			ui.Warn("nginx -t falla tras restaurar, revísalo antes de recargar: %s", out)
		} else {
			ui.Info("nginx -t correcto; aplica los cambios con: systemctl reload nginx")
		}
	}
	return nil
}
