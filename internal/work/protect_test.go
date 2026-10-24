package work

import (
	"os"
	"path/filepath"
	"testing"
)

// Regresión: un enlace simbólico no debe permitir esquivar las rutas protegidas.
func TestProtectedThroughSymlink(t *testing.T) {
	link := filepath.Join(t.TempDir(), "raiz")
	if err := os.Symlink("/", link); err != nil {
		t.Skip("no se pueden crear enlaces simbólicos aquí")
	}
	if !isProtected(filepath.Join(link, "etc")) {
		t.Error("/enlace/etc debía resolverse a /etc y estar protegido")
	}
	if !isProtected("/etc") || isProtected("/tmp/algo") {
		t.Error("isProtected falla en casos básicos")
	}
	if !isGuardedChild("/etc/ssh") || isGuardedChild("/etc/ssh/sshd_config.d") {
		t.Error("isGuardedChild solo debe cubrir los hijos directos de directorios del sistema")
	}
}
