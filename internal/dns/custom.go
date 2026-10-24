package dns

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// customCmd ejecuta TAWANZU_DNS_CMD (con sh -c) para cualquier proveedor sin API integrada
// (nsupdate, scripts propios, herramientas como lexicon...). Recibe el registro por variables:
// TAWANZU_DNS_HOST, TAWANZU_DNS_ZONE, TAWANZU_DNS_NAME, TAWANZU_DNS_IP, TAWANZU_DNS_TTL.
// El comando lo define el administrador del servidor; se ejecuta con sus mismos permisos.
type customCmd struct{}

func (customCmd) Name() string { return "custom" }

func (customCmd) Existing(Target) (Existing, error) { return Existing{}, nil }

func (customCmd) SetA(t Target, ip string, ttl int) error {
	cmdline := strings.TrimSpace(os.Getenv("TAWANZU_DNS_CMD"))
	if cmdline == "" {
		return fmt.Errorf("define TAWANZU_DNS_CMD con el comando que crea el registro A")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdline)
	cmd.Env = append(os.Environ(),
		"TAWANZU_DNS_HOST="+t.Host, "TAWANZU_DNS_ZONE="+t.Zone, "TAWANZU_DNS_NAME="+t.Name,
		"TAWANZU_DNS_IP="+ip, fmt.Sprintf("TAWANZU_DNS_TTL=%d", ttl))
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 400 {
			msg = msg[len(msg)-400:]
		}
		return fmt.Errorf("TAWANZU_DNS_CMD falló: %w\n%s", err, msg)
	}
	return nil
}
