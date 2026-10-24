package update

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func mkzip(t *testing.T, comment string, files map[string]string) string {
	p := filepath.Join(t.TempDir(), "t.zip")
	f, _ := os.Create(p)
	w := zip.NewWriter(f)
	w.SetComment(comment)
	for n, c := range files {
		fw, _ := w.Create(n)
		fw.Write([]byte(c))
	}
	w.Close()
	f.Close()
	return p
}

func TestUnzip(t *testing.T) {
	dest := t.TempDir()
	top, c, err := Unzip(mkzip(t, "abc123", map[string]string{"server-main/Makefile": "x", "server-main/cmd/a.go": "y"}), dest)
	if err != nil || top != "server-main" || c != "abc123" {
		t.Fatalf("%q %q %v", top, c, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "server-main", "cmd", "a.go")); string(b) != "y" {
		t.Error("no se extrajo cmd/a.go")
	}
}

func TestUnzipRejectsTraversalAndMultipleRoots(t *testing.T) {
	if _, _, err := Unzip(mkzip(t, "", map[string]string{"x/../../evil": "1"}), t.TempDir()); err == nil {
		t.Error("debía rechazar una ruta que escapa del destino")
	}
	if _, _, err := Unzip(mkzip(t, "", map[string]string{"a/f": "1", "b/f": "2"}), t.TempDir()); err == nil {
		t.Error("debía rechazar varias carpetas raíz")
	}
}
