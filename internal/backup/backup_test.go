package backup

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// Regresión: los enlaces simbólicos (Let's Encrypt live/*.pem) deben entrar en la copia.
func TestCreateKeepsSymlinks(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("TAWANZU_HOME", home)
	t.Setenv("TAWANZU_PROJECTS", filepath.Join(root, "srv"))
	t.Setenv("TAWANZU_BACKUPS", filepath.Join(root, "bk"))
	if err := os.MkdirAll(filepath.Join(home, "live"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "archive.pem"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../archive.pem", filepath.Join(home, "live", "fullchain.pem")); err != nil {
		t.Fatal(err)
	}
	name, err := Create(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(name); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	found := false
	for {
		hd, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hd.Typeflag == tar.TypeSymlink && hd.Linkname == "../archive.pem" {
			found = true
		}
	}
	if !found {
		t.Error("el enlace simbólico no está en la copia de seguridad")
	}
}

// Regresión: Restore solo puede escribir en rutas que Tawanzu respalda.
func TestRestoreAllowed(t *testing.T) {
	t.Setenv("TAWANZU_HOME", "/var/lib/tawanzu")
	t.Setenv("TAWANZU_PROJECTS", "/srv/tawanzu")
	cases := map[string]bool{
		"/etc/nginx/nginx.conf":                   true,
		"/etc/letsencrypt/live/a.com/privkey.pem": true,
		"/etc/systemd/system/tawanzu-x.service":   true,
		"/var/lib/tawanzu/state.json":             true,
		"/srv/tawanzu/app/shared/.env":            true,
		"/etc/passwd":                             false,
		"/etc/shadow":                             false,
		"/root/.ssh/authorized_keys":              false,
		"/etc/nginxevil/x":                        false,
		"/etc/systemd/system/sshd.service":        false,
	}
	for p, want := range cases {
		if got := restoreAllowed(p); got != want {
			t.Errorf("restoreAllowed(%q) = %v, se esperaba %v", p, got, want)
		}
	}
}
