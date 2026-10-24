package system

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Mount struct {
	Device, Path, FS        string
	Total, Free             uint64
	InodesTotal, InodesFree uint64
}

type Proc struct {
	PID  int
	Name string
	CPU  float64 // porcentaje de un núcleo
	RSS  uint64
}

type NetIf struct {
	Name           string
	RxBytes        uint64
	TxBytes        uint64
	RxRate, TxRate float64 // bytes/s
}

type Status struct {
	Time      time.Time
	CPUPct    float64
	PerCPU    []float64
	Load      [3]float64
	Mem       Mem
	Mounts    []Mount
	Net       []NetIf
	Procs     []Proc
	Uptime    float64
	TempC     float64
	TaskCount int
}

type cpuTimes struct{ idle, total uint64 }

func readCPU() map[string]cpuTimes {
	res := map[string]cpuTimes{}
	f, err := os.Open("/proc/stat")
	if err != nil {
		return res
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := sc.Text()
		if !strings.HasPrefix(l, "cpu") {
			continue
		}
		fl := strings.Fields(l)
		var tot, idle uint64
		for i, v := range fl[1:] {
			n, _ := strconv.ParseUint(v, 10, 64)
			if i < 8 {
				tot += n
			}
			if i == 3 || i == 4 {
				idle += n
			}
		}
		res[fl[0]] = cpuTimes{idle, tot}
	}
	return res
}

func readNet() map[string][2]uint64 {
	res := map[string][2]uint64{}
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return res
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		fl := strings.Fields(rest)
		if len(fl) < 9 || name == "lo" {
			continue
		}
		rx, _ := strconv.ParseUint(fl[0], 10, 64)
		tx, _ := strconv.ParseUint(fl[8], 10, 64)
		res[name] = [2]uint64{rx, tx}
	}
	return res
}

type procSample struct {
	pid   int
	name  string
	ticks uint64
	rss   uint64
}

// sampleProcs lee todos los procesos con un pool de workers.
func sampleProcs() []procSample {
	ents, _ := filepath.Glob("/proc/[0-9]*")
	jobs := make(chan string)
	out := make(chan procSample, len(ents))
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for dir := range jobs {
				b, err := os.ReadFile(dir + "/stat")
				if err != nil {
					continue
				}
				s := string(b)
				l, r := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
				if l < 0 || r < l {
					continue
				}
				fl := strings.Fields(s[r+1:])
				if len(fl) < 13 {
					continue
				}
				ut, _ := strconv.ParseUint(fl[11], 10, 64)
				st, _ := strconv.ParseUint(fl[12], 10, 64)
				var rss uint64
				if m, err := os.ReadFile(dir + "/statm"); err == nil {
					if f := strings.Fields(string(m)); len(f) > 1 {
						p, _ := strconv.ParseUint(f[1], 10, 64)
						rss = p * uint64(os.Getpagesize())
					}
				}
				pid, _ := strconv.Atoi(filepath.Base(dir))
				out <- procSample{pid, s[l+1 : r], ut + st, rss}
			}
		}()
	}
	for _, e := range ents {
		jobs <- e
	}
	close(jobs)
	wg.Wait()
	close(out)
	var res []procSample
	for p := range out {
		res = append(res, p)
	}
	return res
}

// Snapshot toma una muestra completa; las lecturas se hacen en paralelo y el intervalo de CPU/red es 'interval'.
func Snapshot(interval time.Duration) *Status {
	st := &Status{Time: time.Now()}
	c0, n0, p0 := readCPU(), readNet(), sampleProcs()
	t0 := time.Now()

	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); st.Mem = ReadMem() }()
	go func() { defer wg.Done(); st.Mounts = readMounts() }()
	go func() {
		defer wg.Done()
		if b, err := os.ReadFile("/proc/loadavg"); err == nil {
			f := strings.Fields(string(b))
			for i := 0; i < 3 && i < len(f); i++ {
				st.Load[i], _ = strconv.ParseFloat(f[i], 64)
			}
		}
		if b, err := os.ReadFile("/proc/uptime"); err == nil {
			f := strings.Fields(string(b))
			if len(f) > 0 {
				st.Uptime, _ = strconv.ParseFloat(f[0], 64)
			}
		}
		st.TempC = readTemp()
	}()
	time.Sleep(interval)
	c1, n1, p1 := readCPU(), readNet(), sampleProcs()
	dt := time.Since(t0).Seconds()
	wg.Wait()

	pct := func(a, b cpuTimes) float64 {
		dt, di := float64(b.total-a.total), float64(b.idle-a.idle)
		if dt <= 0 {
			return 0
		}
		return (dt - di) / dt * 100
	}
	st.CPUPct = pct(c0["cpu"], c1["cpu"])
	for i := 0; ; i++ {
		k := "cpu" + strconv.Itoa(i)
		a, ok := c0[k]
		if !ok {
			break
		}
		st.PerCPU = append(st.PerCPU, pct(a, c1[k]))
	}
	for name, v := range n1 {
		o := n0[name]
		st.Net = append(st.Net, NetIf{name, v[0], v[1], float64(v[0]-o[0]) / dt, float64(v[1]-o[1]) / dt})
	}
	sort.Slice(st.Net, func(i, j int) bool { return st.Net[i].Name < st.Net[j].Name })

	prev := map[int]uint64{}
	for _, p := range p0 {
		prev[p.pid] = p.ticks
	}
	for _, p := range p1 {
		d := float64(p.ticks-prev[p.pid]) / 100.0 / dt * 100
		if p.ticks < prev[p.pid] {
			d = 0
		}
		st.Procs = append(st.Procs, Proc{p.pid, p.name, d, p.rss})
	}
	st.TaskCount = len(st.Procs)
	sort.Slice(st.Procs, func(i, j int) bool {
		if st.Procs[i].CPU == st.Procs[j].CPU {
			return st.Procs[i].RSS > st.Procs[j].RSS
		}
		return st.Procs[i].CPU > st.Procs[j].CPU
	})
	return st
}

var pseudoFS = map[string]bool{
	"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true, "tmpfs": true, "cgroup": true, "cgroup2": true,
	"overlay": true, "squashfs": true, "securityfs": true, "debugfs": true, "tracefs": true, "pstore": true,
	"configfs": true, "fusectl": true, "mqueue": true, "hugetlbfs": true, "bpf": true, "autofs": true,
	"binfmt_misc": true, "efivarfs": true, "nsfs": true, "ramfs": true, "fuse.lxcfs": true,
}

// readMounts consulta cada sistema de archivos real en paralelo.
func readMounts() []Mount {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()
	seen := map[string]bool{}
	var list []Mount
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fl := strings.Fields(sc.Text())
		if len(fl) < 3 || pseudoFS[fl[2]] || seen[fl[0]] || !strings.HasPrefix(fl[0], "/") {
			continue
		}
		seen[fl[0]] = true
		list = append(list, Mount{Device: fl[0], Path: fl[1], FS: fl[2]})
	}
	var wg sync.WaitGroup
	for i := range list {
		wg.Add(1)
		go func(m *Mount) {
			defer wg.Done()
			d := DiskUsage(m.Path)
			m.Total, m.Free, m.InodesTotal, m.InodesFree = d.Total, d.Free, d.InodesTotal, d.InodesFree
		}(&list[i])
	}
	wg.Wait()
	return list
}

func readTemp() float64 {
	zones, _ := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
	var max float64
	for _, z := range zones {
		if b, err := os.ReadFile(z); err == nil {
			if v, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64); err == nil && v/1000 > max {
				max = v / 1000
			}
		}
	}
	return max
}
