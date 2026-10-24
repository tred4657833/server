package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T) (home string) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, "home")
	t.Setenv("TAWANZU_HOME", home)
	t.Setenv("TAWANZU_PROJECTS", filepath.Join(root, "srv"))
	t.Setenv("TAWANZU_BACKUPS", filepath.Join(root, "bk"))
	t.Setenv("TAWANZU_BACKUP_KEY", filepath.Join(root, "clave.key"))
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	return home
}

func names(t *testing.T, path string) []string {
	t.Helper()
	c, r, err := openArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var out []string
	for {
		hd, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, hd.Name)
	}
}

func TestEncryptedBackupRoundTrip(t *testing.T) {
	home := setup(t)
	secret := strings.Repeat("contraseña-secreta ", 20000) // > 1 fragmento cifrado
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TAWANZU_BACKUP_ENCRYPT", "1")
	name, err := Create(false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(name, ".tar.gz.enc") {
		t.Fatalf("extensión inesperada: %s", name)
	}
	raw, _ := os.ReadFile(name)
	if bytes.Contains(raw, []byte("contraseña-secreta")) {
		t.Fatal("el contenido aparece en claro en el archivo cifrado")
	}
	if err := Verify(name); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range names(t, name) {
		if strings.HasSuffix(n, ".env") {
			found = true
		}
	}
	if !found {
		t.Fatal(".env no está en la copia cifrada")
	}
	if got := List(); len(got) != 1 || got[0].Path != name {
		t.Fatalf("List no ve la copia cifrada: %v", got)
	}
}

func TestEncryptedBackupDetectsTamperingAndWrongKey(t *testing.T) {
	home := setup(t)
	_ = os.WriteFile(filepath.Join(home, "a.txt"), []byte("hola"), 0o600)
	t.Setenv("TAWANZU_BACKUP_ENCRYPT", "1")
	name, err := Create(false)
	if err != nil {
		t.Fatal(err)
	}
	// Archivo truncado: el .sha256 fallaría, pero incluso sin él el descifrado debe rechazarlo.
	raw, _ := os.ReadFile(name)
	trunc := filepath.Join(filepath.Dir(name), "tawanzu-00000000-000000.tar.gz.enc")
	_ = os.WriteFile(trunc, raw[:len(raw)-10], 0o600)
	c, r, err := openArchive(trunc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, r); err == nil {
		t.Error("un archivo cifrado truncado se leyó sin error")
	}
	c.Close()
	// Clave incorrecta.
	other := filepath.Join(t.TempDir(), "otra.key")
	_ = os.WriteFile(other, []byte(strings.Repeat("ab", 32)+"\n"), 0o600)
	t.Setenv("TAWANZU_BACKUP_KEY", other)
	if err := Verify(name); err == nil {
		t.Error("Verify aceptó una clave incorrecta")
	}
}

// Regresión: antes se excluía cualquier ruta que contuviera "/backups/", p. ej. datos legítimos de una app.
func TestOnlyBackupDirIsExcluded(t *testing.T) {
	home := setup(t)
	legit := filepath.Join(home, "apps", "backups")
	_ = os.MkdirAll(legit, 0o755)
	_ = os.WriteFile(filepath.Join(legit, "dato.txt"), []byte("importante"), 0o600)
	name, err := Create(false)
	if err != nil {
		t.Fatal(err)
	}
	ok := false
	for _, n := range names(t, name) {
		if strings.HasSuffix(n, "apps/backups/dato.txt") {
			ok = true
		}
	}
	if !ok {
		t.Error("se excluyó un archivo legítimo que está en una carpeta llamada backups")
	}
	if !excluded(Dir()) || !excluded(filepath.Join(Dir(), "x.tar.gz")) || excluded(legit) {
		t.Error("excluded() no distingue el directorio de copias")
	}
}

func TestIncludesProjectDataDir(t *testing.T) {
	setup(t)
	data := filepath.Join(os.Getenv("TAWANZU_PROJECTS"), "miapp", "data")
	_ = os.MkdirAll(data, 0o755)
	_ = os.WriteFile(filepath.Join(data, "app.db"), []byte("sqlite"), 0o600)
	name, err := Create(false)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names(t, name) {
		if strings.HasSuffix(n, "miapp/data/app.db") {
			return
		}
	}
	t.Error("data/ del proyecto no está en la copia")
}

// Regresión: un archivo ilegible dejaba una copia incompleta marcada como correcta.
func TestUnreadableFileMakesBackupFail(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root puede leer cualquier archivo")
	}
	home := setup(t)
	bad := filepath.Join(home, "secreto")
	_ = os.WriteFile(bad, []byte("x"), 0o000)
	name, err := Create(false)
	if err == nil || !strings.Contains(err.Error(), "INCOMPLETA") {
		t.Fatalf("se esperaba error de copia incompleta, got %v", err)
	}
	if name == "" {
		t.Error("la copia incompleta debería conservarse para inspección")
	}
}

func TestUploadHook(t *testing.T) {
	setup(t)
	dst := filepath.Join(t.TempDir(), "subido.txt")
	t.Setenv("TAWANZU_BACKUP_UPLOAD", `printf %s "$TAWANZU_BACKUP_FILE" > `+dst)
	did, err := Upload("/tmp/x.tar.gz")
	if err != nil || !did {
		t.Fatalf("did=%v err=%v", did, err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "/tmp/x.tar.gz" {
		t.Errorf("el hook no recibió la ruta: %q", b)
	}
	t.Setenv("TAWANZU_BACKUP_UPLOAD", "exit 3")
	if _, err := Upload("/tmp/x"); err == nil {
		t.Error("un fallo de subida debe devolver error")
	}
	t.Setenv("TAWANZU_BACKUP_UPLOAD", "")
	if did, _ := Upload("/tmp/x"); did {
		t.Error("sin variable no debe subir nada")
	}
}
