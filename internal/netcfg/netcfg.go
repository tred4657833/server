// Package netcfg gestiona Nginx, dominios, verificación DNS y certificados TLS.
package netcfg

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"tawanzu/internal/pkgs"
	"tawanzu/internal/runner"
	"tawanzu/internal/services"
	"tawanzu/internal/state"
	"tawanzu/internal/system"
	"tawanzu/internal/tune"
	"tawanzu/internal/ui"
)

var domainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)

// ValidDomain valida un nombre de dominio simple.
func ValidDomain(d string) bool { return domainRe.MatchString(strings.ToLower(d)) }

// InstallNginx instala Nginx (si falta), aplica ajustes seguros y lo habilita.
func InstallNginx(i *system.Info, t tune.Tuning) error {
	if !i.Installed["nginx"] {
		if err := pkgs.InstallComponent(i.PkgMgr, "nginx"); err != nil {
			return err
		}
	} else {
		ui.Info("Nginx ya está instalado: se reutiliza")
	}
	const confPath = "/etc/nginx/conf.d/00-tawanzu.conf"
	snippet := baseSnippet(t.NginxRateLimit, httpDirectivesOf("/etc/nginx/nginx.conf"))
	bak, err := runner.WriteFile(confPath, snippet, 0o644)
	if err != nil {
		return err
	}
	tuneWorkerConnections(t.NginxConns)
	if err := Test(); err != nil {
		runner.Restore(confPath, bak)
		return fmt.Errorf("la configuración de Nginx no es válida, se revirtió: %w", err)
	}
	return services.Enable("nginx")
}

// baseSnippet genera el contenido de conf.d/00-tawanzu.conf. Las directivas que
// admiten un solo valor (server_tokens, gzip...) se omiten si nginx.conf ya las
// define a nivel http: repetirlas hace fallar `nginx -t` con "directive is duplicate".
func baseSnippet(rate string, have map[string]bool) string {
	var b strings.Builder
	b.WriteString("# Gestionado por Tawanzu\n")
	for _, d := range []struct{ name, line string }{
		{"server_tokens", "server_tokens off;"},
		{"gzip", "gzip on;"},
		{"gzip_comp_level", "gzip_comp_level 5;"},
		{"gzip_min_length", "gzip_min_length 512;"},
		{"gzip_types", "gzip_types text/plain text/css application/json application/javascript text/xml application/xml image/svg+xml;"},
		{"client_max_body_size", "client_max_body_size 32m;"},
	} {
		if have[d.name] {
			fmt.Fprintf(&b, "# %s: ya definido en nginx.conf\n", d.name)
			continue
		}
		b.WriteString(d.line + "\n")
	}
	fmt.Fprintf(&b, "limit_req_zone $binary_remote_addr zone=tawanzu_req:10m rate=%s;\n", rate)
	return b.String()
}

// httpDirectivesOf devuelve las directivas activas (sin comentar) que nginx.conf
// define directamente dentro de http { }.
func httpDirectivesOf(path string) map[string]bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return map[string]bool{}
	}
	return httpDirectives(string(b))
}

func httpDirectives(conf string) map[string]bool {
	have := map[string]bool{}
	var stack []string
	for _, line := range strings.Split(conf, "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		opens, closes := strings.Contains(line, "{"), strings.Contains(line, "}")
		switch {
		case opens && closes:
			// bloque en una sola línea: no afecta al nivel http
		case opens:
			stack = append(stack, f[0])
		case line == "}":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case strings.HasSuffix(line, ";") && len(stack) > 0 && stack[len(stack)-1] == "http":
			have[f[0]] = true
		}
	}
	return have
}

var wcRe = regexp.MustCompile(`worker_connections\s+\d+;`)

func tuneWorkerConnections(n int) {
	const p = "/etc/nginx/nginx.conf"
	b, err := os.ReadFile(p)
	if err != nil || !wcRe.Match(b) {
		return
	}
	repl := fmt.Sprintf("worker_connections %d;", n)
	bak, err := runner.WriteFile(p, wcRe.ReplaceAllString(string(b), repl), 0o644)
	if err != nil {
		return
	}
	if Test() != nil {
		runner.Restore(p, bak)
	}
}

// Test ejecuta nginx -t.
func Test() error {
	if runner.DryRun {
		return nil
	}
	_, err := runner.Run("nginx", "-t")
	return err
}

// ---------- DNS ----------

type DNSResult struct {
	Host    string
	IPs     []string
	Matches bool
	Err     error
}

// CheckDNS resuelve en paralelo los hosts y compara contra la IP pública del servidor.
func CheckDNS(hosts []string, publicIP string) []DNSResult {
	res := make([]DNSResult, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func(i int, h string) {
			defer wg.Done()
			r := DNSResult{Host: h}
			ips, err := net.LookupHost(h)
			r.Err = err
			for _, ip := range ips {
				if p := net.ParseIP(ip); p != nil && p.To4() != nil {
					r.IPs = append(r.IPs, ip)
					if ip == publicIP {
						r.Matches = true
					}
				}
			}
			res[i] = r
		}(i, h)
	}
	wg.Wait()
	return res
}

// ---------- Sitios ----------

type Site struct {
	Domain   string
	Kind     string // proxy | static | php
	Port     int
	Root     string
	WWW      bool
	PHPSock  string
	HasHTTPS bool
}

func (s Site) names() string {
	if s.WWW {
		return s.Domain + " www." + s.Domain
	}
	return s.Domain
}

// Render genera el server block de Nginx.
func (s Site) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Gestionado por Tawanzu\nserver {\n    listen 80;\n    listen [::]:80;\n    server_name %s;\n\n", s.names())
	b.WriteString("    add_header X-Content-Type-Options nosniff always;\n    add_header X-Frame-Options SAMEORIGIN always;\n    add_header Referrer-Policy strict-origin-when-cross-origin always;\n\n")
	b.WriteString("    location /.well-known/acme-challenge/ { root /var/www/html; }\n\n")
	switch s.Kind {
	case "static":
		fmt.Fprintf(&b, "    root %s;\n    index index.html index.htm;\n    location ~ /\\.(?!well-known) { deny all; }\n    location / { try_files $uri $uri/ =404; }\n", s.Root)
	case "php":
		fmt.Fprintf(&b, "    root %s;\n    index index.php index.html;\n    location / { try_files $uri $uri/ /index.php?$query_string; }\n", s.Root)
		fmt.Fprintf(&b, "    location ~ \\.php$ {\n        %s\n        fastcgi_pass unix:%s;\n    }\n    location ~ /\\.(?!well-known) { deny all; }\n", fastcgiInclude(), s.PHPSock)
	default:
		fmt.Fprintf(&b, "    location / {\n        limit_req zone=tawanzu_req burst=60 nodelay;\n        proxy_pass http://127.0.0.1:%d;\n        proxy_http_version 1.1;\n        proxy_set_header Host $host;\n        proxy_set_header X-Real-IP $remote_addr;\n        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n        proxy_set_header X-Forwarded-Proto $scheme;\n        proxy_set_header Upgrade $http_upgrade;\n        proxy_set_header Connection $connection_upgrade;\n        proxy_read_timeout 90s;\n    }\n", s.Port)
	}
	b.WriteString("}\n")
	return b.String()
}

// fastcgiInclude usa el snippet de Debian/Ubuntu si existe; en Fedora/RHEL (que no lo trae)
// escribe los parámetros FastCGI mínimos directamente.
func fastcgiInclude() string {
	if _, err := os.Stat("/etc/nginx/snippets/fastcgi-php.conf"); err == nil {
		return "include snippets/fastcgi-php.conf;"
	}
	return "include fastcgi_params;\n        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;"
}

// ensureRateZone garantiza que exista la zona limit_req que usan los sitios proxy,
// incluso si `tawanzu init` no se ejecutó (sin ella, nginx -t falla).
func ensureRateZone() error {
	const p = "/etc/nginx/conf.d/00-tawanzu.conf"
	// Un archivo vacío o sin la zona (p. ej. quedó en blanco tras un fallo) no sirve: se regenera.
	if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), "zone=tawanzu_req") {
		return nil
	}
	_, err := runner.WriteFile(p, baseSnippet("20r/s", httpDirectivesOf("/etc/nginx/nginx.conf")), 0o644)
	return err
}

// configPath devuelve dónde se guarda el sitio según la estructura de la distro.
func configPath(domain string) (path, link string) {
	if st, err := os.Stat("/etc/nginx/sites-available"); err == nil && st.IsDir() {
		return "/etc/nginx/sites-available/" + domain, "/etc/nginx/sites-enabled/" + domain
	}
	return "/etc/nginx/conf.d/" + domain + ".conf", ""
}

// EnsureUpgradeMap añade el mapa necesario para WebSockets.
func EnsureUpgradeMap() error {
	_, err := runner.WriteFile("/etc/nginx/conf.d/01-tawanzu-ws.conf",
		"map $http_upgrade $connection_upgrade {\n    default upgrade;\n    ''      close;\n}\n", 0o644)
	return err
}

// PHPSocket busca el socket de php-fpm instalado.
func PHPSocket() string {
	m, _ := filepath.Glob("/run/php/php*-fpm.sock")
	if len(m) == 0 {
		m, _ = filepath.Glob("/run/php-fpm/*.sock") // Fedora/RHEL
	}
	sort.Strings(m)
	if len(m) > 0 {
		return m[len(m)-1]
	}
	return "/run/php/php-fpm.sock"
}

// Apply escribe, valida y recarga un sitio. Si nginx -t falla, revierte.
func Apply(s Site) error {
	if err := EnsureUpgradeMap(); err != nil {
		return err
	}
	if s.Kind == "proxy" {
		if err := ensureRateZone(); err != nil {
			return err
		}
	}
	path, link := configPath(s.Domain)
	bak, err := runner.WriteFile(path, s.Render(), 0o644)
	if err != nil {
		return err
	}
	if link != "" && !runner.DryRun {
		_ = os.Remove(link)
		if err := os.Symlink(path, link); err != nil {
			return err
		}
	}
	if err := Test(); err != nil {
		runner.Restore(path, bak)
		if link != "" && bak == "" {
			_ = os.Remove(link)
		}
		return fmt.Errorf("nginx -t falló, se restauró la configuración anterior: %w", err)
	}
	return services.Reload("nginx")
}

// Remove elimina un sitio.
func Remove(domain string) error {
	path, link := configPath(domain)
	if !runner.DryRun {
		if link != "" {
			_ = os.Remove(link)
		}
		_ = os.Remove(path)
	}
	if err := Test(); err != nil {
		return err
	}
	return services.Reload("nginx")
}

// IssueCert pide un certificado con certbot y activa la redirección HTTPS.
func IssueCert(i *system.Info, hosts []string, email string) error {
	if !i.Installed["certbot"] {
		if err := pkgs.InstallComponent(i.PkgMgr, "certbot"); err != nil {
			return err
		}
	}
	args := []string{"--nginx", "--non-interactive", "--agree-tos", "--redirect"}
	if email == "" {
		args = append(args, "--register-unsafely-without-email")
	} else {
		args = append(args, "-m", email)
	}
	for _, h := range hosts {
		args = append(args, "-d", h)
	}
	_, err := runner.Run("certbot", args...)
	return err
}

// ReinstallCert vuelve a instalar en Nginx un certificado que ya existe, sin pedir otro a Let's Encrypt
// (no requiere que el DNS esté propagado). Hace falta tras reescribir el bloque del sitio, porque
// Apply reemplaza lo que certbot había añadido (listen 443, certificados, redirección).
func ReinstallCert(domain string) error {
	_, err := runner.Run("certbot", "install", "--nginx", "--non-interactive", "--redirect", "--cert-name", domain)
	return err
}

// CertExpiry consulta la caducidad del certificado que sirve un dominio.
func CertExpiry(domain string) (time.Time, error) {
	d := net.Dialer{Timeout: 4 * time.Second}
	c, err := tls.DialWithDialer(&d, "tcp", domain+":443", &tls.Config{ServerName: domain})
	if err != nil {
		return time.Time{}, err
	}
	defer c.Close()
	return c.ConnectionState().PeerCertificates[0].NotAfter, nil
}

// ---------- Comando domain add ----------

type AddOptions struct {
	Domain  string
	Port    int
	HTTPS   *bool
	Email   string
	Project string
}

// DomainAdd es el flujo interactivo completo: pregunta puerto, comprueba DNS, configura Nginx y HTTPS.
func DomainAdd(i *system.Info, st *state.State, o AddOptions) error {
	o.Domain = strings.ToLower(strings.TrimSpace(o.Domain))
	if !ValidDomain(o.Domain) {
		return fmt.Errorf("dominio inválido: %q", o.Domain)
	}
	site := Site{Domain: o.Domain, Kind: "proxy"}
	if p := st.Projects[o.Project]; p != nil {
		site.Port = p.Port
		switch p.Kind {
		case "static":
			site.Kind, site.Root = "static", filepath.Join(state.ProjectsRoot(), p.Name, "current")
		case "php":
			site.Kind, site.Root, site.PHPSock = "php", filepath.Join(state.ProjectsRoot(), p.Name, "current"), PHPSocket()
			if _, err := os.Stat(filepath.Join(site.Root, "public")); err == nil {
				site.Root = filepath.Join(site.Root, "public")
			}
		}
	}
	ui.Title("Configurar dominio")
	ui.Plain("Dominio: %s", ui.Bold(o.Domain))
	if site.Kind == "proxy" {
		def := ""
		if o.Port > 0 {
			def = fmt.Sprint(o.Port)
		} else if site.Port > 0 {
			def = fmt.Sprint(site.Port)
		}
		var port int
		if _, err := fmt.Sscan(ui.Ask("Puerto del proyecto", def), &port); err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("puerto inválido")
		}
		site.Port = port
		if !system.PortOpen(port) {
			ui.Warn("ahora mismo nada escucha en 127.0.0.1:%d (Nginx devolverá 502 hasta que arranque la app)", port)
		}
	}
	wantHTTPS := true
	if o.HTTPS != nil {
		wantHTTPS = *o.HTTPS
	} else {
		wantHTTPS = ui.Confirm("¿Configurar HTTPS automáticamente?", true)
	}

	// DNS: dominio y www se comprueban a la vez.
	ui.Step("comprobando DNS (registro A) frente a la IP pública %s", orUnknown(i.PublicIP))
	res := CheckDNS([]string{o.Domain, "www." + o.Domain}, i.PublicIP)
	dnsOK := res[0].Matches
	site.WWW = res[1].Matches
	switch {
	case i.PublicIP == "":
		ui.Warn("no se pudo determinar la IP pública; no se puede verificar el DNS")
		dnsOK = false
	case dnsOK:
		ui.OK("%s → %s", o.Domain, strings.Join(res[0].IPs, ", "))
		if site.WWW {
			ui.OK("www.%s también apunta aquí y se incluirá", o.Domain)
		}
	default:
		ui.Warn("%s resuelve a %v pero tu servidor es %s", o.Domain, res[0].IPs, orUnknown(i.PublicIP))
		ui.Plain("   Crea un registro DNS tipo A en tu proveedor:  %s  →  %s", o.Domain, orUnknown(i.PublicIP))
	}

	if ips, err := net.LookupIP(o.Domain); err == nil {
		for _, ip := range ips {
			if ip.To4() == nil {
				ui.Warn("%s tiene un registro AAAA (IPv6): Let's Encrypt lo usará para validar. Si no apunta a este servidor, el certificado fallará; elimínalo o corrígelo", o.Domain)
				break
			}
		}
	}

	if !i.Installed["nginx"] && !runner.DryRun {
		return fmt.Errorf("Nginx no está instalado: ejecuta primero `tawanzu init`")
	}
	if err := Apply(site); err != nil {
		return err
	}
	ui.OK("Nginx configurado para %s", site.names())

	httpsDone := false
	if prev := st.Domains[o.Domain]; prev != nil && prev.HTTPS {
		if err := ReinstallCert(o.Domain); err != nil {
			ui.Err("no se pudo reactivar el certificado existente: %v", err)
		} else {
			httpsDone, wantHTTPS = true, false
			if o.Email == "" {
				o.Email = prev.Email
			}
			ui.OK("HTTPS conservado con el certificado existente")
		}
	}
	if wantHTTPS {
		if !dnsOK {
			ui.Warn("HTTPS pendiente: el DNS aún no apunta al servidor. Cuando propague, repite: tawanzu domain add %s --https", o.Domain)
		} else {
			hosts := []string{o.Domain}
			if site.WWW {
				hosts = append(hosts, "www."+o.Domain)
			}
			email := o.Email
			if email == "" {
				email = ui.Ask("Email para avisos de Let's Encrypt (opcional)", "")
			}
			if err := IssueCert(i, hosts, email); err != nil {
				ui.Err("no se pudo emitir el certificado: %v", err)
			} else {
				httpsDone = true
				o.Email = email
				ui.OK("HTTPS activo con renovación automática")
			}
		}
	}
	st.Domains[o.Domain] = &state.Domain{Name: o.Domain, Port: site.Port, HTTPS: httpsDone, Project: o.Project, Kind: site.Kind, Root: site.Root, Email: o.Email}
	if p := st.Projects[o.Project]; p != nil {
		p.Domain = o.Domain
	}
	st.Log("domain add " + o.Domain)
	return st.Save()
}

func orUnknown(s string) string {
	if s == "" {
		return "desconocida"
	}
	return s
}
