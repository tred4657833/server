// Package system detecta el entorno del servidor y lee métricas desde /proc de forma concurrente.
package system

import (
	"sort"
	"bufio"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Info struct {
	Hostname   string
	OS         string
	Distro     string
	Version    string
	DistroID   string
	Kernel     string
	Arch       string
	CPUs       int
	CPUModel   string
	MemTotal   uint64
	MemAvail   uint64
	SwapTotal  uint64
	SwapFree   uint64
	DiskTotal  uint64
	DiskFree   uint64
	IsRoot     bool
	Systemd    bool
	PkgMgr     string // apt | dnf | yum | apk | pacman
	PublicIP   string
	Online     bool
	SSHPort    int
	SSHPorts   []int // todos los puertos SSH conocidos (el firewall los permite todos)
	Listening  map[int]string
	Installed  map[string]bool // nginx, ufw, certbot, ...
	Virtual    string
	UptimeSecs float64
}

// Detect lanza todas las sondas en paralelo.
func Detect() *Info {
	i := &Info{
		OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(),
		IsRoot: os.Geteuid() == 0, SSHPort: 22,
		Installed: map[string]bool{},
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	run := func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}
	run(func() { i.Hostname, _ = os.Hostname() })
	run(func() {
		kv := osRelease()
		mu.Lock()
		i.Distro, i.Version, i.DistroID = kv["NAME"], kv["VERSION_ID"], kv["ID"]
		mu.Unlock()
	})
	run(func() {
		b, _ := os.ReadFile("/proc/sys/kernel/osrelease")
		i.Kernel = strings.TrimSpace(string(b))
	})
	run(func() {
		m := ReadMem()
		mu.Lock()
		i.MemTotal, i.MemAvail, i.SwapTotal, i.SwapFree = m.Total, m.Available, m.SwapTotal, m.SwapFree
		mu.Unlock()
	})
	run(func() { i.CPUModel = cpuModel() })
	run(func() {
		d := DiskUsage("/")
		i.DiskTotal, i.DiskFree = d.Total, d.Free
	})
	run(func() { _, err := os.Stat("/run/systemd/system"); i.Systemd = err == nil })
	run(func() {
		for _, pm := range []string{"apt-get", "dnf", "yum", "apk", "pacman"} {
			if _, err := exec.LookPath(pm); err == nil {
				if pm == "apt-get" {
					pm = "apt"
				}
				i.PkgMgr = pm
				return
			}
		}
	})
	run(func() { i.Online = Online() })
	run(func() { i.PublicIP = PublicIP() })
	run(func() {
		l := ListeningPorts()
		mu.Lock()
		i.Listening = l
		mu.Unlock()
	})
	run(func() { i.SSHPort = sshPort(); i.SSHPorts = sshPortList() })
	run(func() {
		b, _ := os.ReadFile("/proc/uptime")
		f := strings.Fields(string(b))
		if len(f) > 0 {
			i.UptimeSecs, _ = strconv.ParseFloat(f[0], 64)
		}
	})
	run(func() {
		if out, err := exec.Command("systemd-detect-virt").Output(); err == nil {
			i.Virtual = strings.TrimSpace(string(out))
		}
	})
	for _, b := range []string{"nginx", "ufw", "firewall-cmd", "certbot", "fail2ban-client", "git", "go", "node", "php", "docker", "psql", "redis-server", "nft"} {
		b := b
		run(func() {
			_, err := exec.LookPath(b)
			mu.Lock()
			i.Installed[b] = err == nil
			mu.Unlock()
		})
	}
	wg.Wait()
	return i
}

func osRelease() map[string]string {
	m := map[string]string{}
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if ok {
			m[k] = strings.Trim(v, `"`)
		}
	}
	return m
}

func cpuModel() string {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "model name") {
			_, v, _ := strings.Cut(sc.Text(), ":")
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// Online comprueba salida a Internet (TCP a un resolvedor público).
func Online() bool {
	for _, a := range []string{"1.1.1.1:443", "8.8.8.8:443"} {
		c, err := net.DialTimeout("tcp", a, 2*time.Second)
		if err == nil {
			c.Close()
			return true
		}
	}
	return false
}

// publicIPServices son servicios independientes que devuelven la IPv4 pública en texto plano.
var publicIPServices = []string{"https://api.ipify.org", "https://ipv4.icanhazip.com", "https://checkip.amazonaws.com"}

// PublicIP devuelve la IP pública IPv4 ACTUAL del servidor. Consulta varios servicios a la vez, sin
// caché ni conexiones reutilizadas, y devuelve la que coincide en al menos dos (o la primera válida
// si solo responde uno), de modo que un cambio de IP se refleja en cuanto lo ve Internet.
func PublicIP() string { return publicIPFrom(publicIPServices, 3*time.Second) }

func publicIPFrom(urls []string, timeout time.Duration) string {
	tr := &http.Transport{DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	cl := http.Client{Timeout: timeout, Transport: tr}
	type res struct{ ip string }
	ch := make(chan res, len(urls))
	for _, u := range urls {
		go func(u string) {
			req, err := http.NewRequest(http.MethodGet, u, nil)
			if err != nil {
				ch <- res{}
				return
			}
			req.Header.Set("Cache-Control", "no-cache")
			r, err := cl.Do(req)
			if err != nil {
				ch <- res{}
				return
			}
			b, _ := io.ReadAll(io.LimitReader(r.Body, 64))
			r.Body.Close()
			ip := strings.TrimSpace(string(b))
			if p := net.ParseIP(ip); p == nil || p.To4() == nil || r.StatusCode != 200 {
				ip = ""
			}
			ch <- res{ip}
		}(u)
	}
	count := map[string]int{}
	first := ""
	for range urls {
		r := <-ch
		if r.ip == "" {
			continue
		}
		if first == "" {
			first = r.ip
		}
		if count[r.ip]++; count[r.ip] >= 2 {
			return r.ip
		}
	}
	return first
}

// LocalIP es una dirección IPv4 privada/local de una interfaz de red.
type LocalIP struct {
	Iface   string `json:"iface"`
	IP      string `json:"ip"`
	Primary bool   `json:"primary"` // la que usa el servidor para salir a Internet
}

// virtualIface indica interfaces de contenedores/virtualización que no son "la IP del servidor".
func virtualIface(name string) bool {
	for _, p := range []string{"docker", "veth", "br-", "cni", "flannel", "cali", "virbr", "lxc", "tailscale"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// LocalIPs lista las IPv4 locales actuales (leídas del sistema en el momento, sin caché).
func LocalIPs() []LocalIP {
	primary := ""
	if c, err := net.Dial("udp", "8.8.8.8:80"); err == nil { // no envía paquetes: solo elige la ruta
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
			primary = a.IP.String()
		}
		c.Close()
	}
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []LocalIP
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || virtualIface(ifc.Name) {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLinkLocalUnicast() {
				out = append(out, LocalIP{Iface: ifc.Name, IP: n.IP.String(), Primary: n.IP.String() == primary})
			}
		}
	}
	if primary == "" && len(out) > 0 {
		out[0].Primary = true
	}
	return out
}

// sshPort detecta el puerto SSH real: conexión actual, sshd_config o 22.
func sshPort() int {
	if v := os.Getenv("SSH_CONNECTION"); v != "" {
		f := strings.Fields(v)
		if len(f) == 4 {
			if p, err := strconv.Atoi(f[3]); err == nil {
				return p
			}
		}
	}
	b, _ := os.ReadFile("/etc/ssh/sshd_config")
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && strings.EqualFold(f[0], "Port") {
			if p, err := strconv.Atoi(f[1]); err == nil {
				return p
			}
		}
	}
	return 22
}

// sshPortList reúne TODOS los puertos SSH conocidos: la conexión actual, `sshd -T` (configuración
// efectiva, con Include), sshd_config y su directorio .d. El firewall los permite todos: equivocarse
// con uno solo dejaría al usuario fuera del servidor.
func sshPortList() []int {
	var out []int
	seen := map[int]bool{}
	add := func(s string) {
		if p, err := strconv.Atoi(s); err == nil && p > 0 && p < 65536 && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if f := strings.Fields(os.Getenv("SSH_CONNECTION")); len(f) == 4 {
		add(f[3])
	}
	bin := ""
	if p, err := exec.LookPath("sshd"); err == nil {
		bin = p
	} else if _, err := os.Stat("/usr/sbin/sshd"); err == nil {
		bin = "/usr/sbin/sshd"
	}
	if bin != "" {
		if o, err := exec.Command(bin, "-T").Output(); err == nil {
			for _, l := range strings.Split(string(o), "\n") {
				if f := strings.Fields(l); len(f) == 2 && strings.EqualFold(f[0], "port") {
					add(f[1])
				}
			}
		}
	}
	files := []string{"/etc/ssh/sshd_config"}
	if m, _ := filepath.Glob("/etc/ssh/sshd_config.d/*.conf"); len(m) > 0 {
		files = append(files, m...)
	}
	for _, fn := range files {
		b, _ := os.ReadFile(fn)
		for _, l := range strings.Split(string(b), "\n") {
			if k := strings.Index(l, "#"); k >= 0 {
				l = l[:k]
			}
			if f := strings.Fields(l); len(f) == 2 && strings.EqualFold(f[0], "port") {
				add(f[1])
			}
		}
	}
	if len(out) == 0 {
		out = append(out, 22)
	}
	return out
}

// ListeningPorts devuelve los puertos TCP en escucha (estado 0A) leyendo /proc.
func ListeningPorts() map[int]string {
	res := map[int]string{}
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		sc.Scan() // cabecera
		for sc.Scan() {
			p := strings.Fields(sc.Text())
			if len(p) < 4 || p[3] != "0A" {
				continue
			}
			_, port, ok := strings.Cut(p[1], ":")
			if !ok {
				continue
			}
			n, _ := strconv.ParseInt(port, 16, 32)
			res[int(n)] = "tcp"
		}
		fh.Close()
	}
	return res
}

// NearbyListeners devuelve los puertos en escucha a menos de radius del dado (sin contarlo), del más
// cercano al más lejano. skip, si no es nil, descarta los que no interesan (SSH, ya publicados...).
// Sirve para detectar que la app está en el 1556 cuando se pidió el 1555.
func NearbyListeners(port, radius int, skip func(int) bool) []int {
	var out []int
	for p := range ListeningPorts() {
		d := p - port
		if d < 0 {
			d = -d
		}
		if d == 0 || d > radius || p < 1024 || (skip != nil && skip(p)) {
			continue
		}
		out = append(out, p)
	}
	dist := func(p int) int {
		if d := p - port; d >= 0 {
			return d
		}
		return port - p
	}
	sort.Slice(out, func(a, b int) bool {
		if da, db := dist(out[a]), dist(out[b]); da != db {
			return da < db
		}
		return out[a] < out[b]
	})
	return out
}

// PortOpen comprueba si algo escucha en 127.0.0.1:port.
func PortOpen(port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// HTTPHealthy comprueba que 127.0.0.1:port responda HTTP (cualquier código de estado cuenta: la app
// contesta). Un puerto abierto que no habla HTTP o que no responde a tiempo se considera no saludable.
func HTTPHealthy(port int) bool {
	c := &http.Client{
		Timeout:       4 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := c.Get("http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) + "/")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

type Mem struct{ Total, Available, Free, Buffers, Cached, SwapTotal, SwapFree uint64 }

// ReadMem lee /proc/meminfo.
func ReadMem() Mem {
	var m Mem
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fl := strings.Fields(v)
		if len(fl) == 0 {
			continue
		}
		n, _ := strconv.ParseUint(fl[0], 10, 64)
		n *= 1024
		switch k {
		case "MemTotal":
			m.Total = n
		case "MemAvailable":
			m.Available = n
		case "MemFree":
			m.Free = n
		case "Buffers":
			m.Buffers = n
		case "Cached":
			m.Cached = n
		case "SwapTotal":
			m.SwapTotal = n
		case "SwapFree":
			m.SwapFree = n
		}
	}
	return m
}
