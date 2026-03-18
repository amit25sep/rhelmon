// Package collector reads Linux /proc and /sys metrics on a 1-second ticker
// and pushes samples into a ringbuf.Store.
package collector

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rhelmon/agent/internal/ringbuf"
)

// Manager owns all collectors and drives their tick loop.
type Manager struct {
	store    *ringbuf.Store
	interval time.Duration
	stopCh   chan struct{}
	wg       sync.WaitGroup

	// per-tick delta state (unexported, updated under mu)
	mu       sync.Mutex
	prevCPU  map[string]cpuStat
	prevDisk map[string]diskStat
	prevNet  map[string]netStat
}

// New creates a Manager. interval is typically 1*time.Second.
func New(store *ringbuf.Store, interval time.Duration) *Manager {
	return &Manager{
		store:    store,
		interval: interval,
		stopCh:   make(chan struct{}),
		prevCPU:  make(map[string]cpuStat),
		prevDisk: make(map[string]diskStat),
		prevNet:  make(map[string]netStat),
	}
}

// Start launches the collection loop in a goroutine.
func (m *Manager) Start() {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		// warm-up: collect once to seed delta state
		m.collect()
		for {
			select {
			case <-ticker.C:
				m.collect()
			case <-m.stopCh:
				return
			}
		}
	}()
}

// Stop signals the collection loop to exit and waits for it.
func (m *Manager) Stop() {
	close(m.stopCh)
	m.wg.Wait()
}

func (m *Manager) collect() {
	now := time.Now().Unix()
	m.collectCPU(now)
	m.collectMemory(now)
	m.collectDisk(now)
	m.collectNetwork(now)
	m.collectLoadAvg(now)
}

// ── CPU ───────────────────────────────────────────────────────────────────────

type cpuStat struct {
	user, nice, system, idle, iowait, irq, softirq, steal uint64
}

func (c cpuStat) total() uint64 {
	return c.user + c.nice + c.system + c.idle + c.iowait + c.irq + c.softirq + c.steal
}
func (c cpuStat) idleTotal() uint64 { return c.idle + c.iowait }

func parseCPULine(fields []string) (cpuStat, error) {
	if len(fields) < 8 {
		return cpuStat{}, fmt.Errorf("short cpu line")
	}
	var s cpuStat
	vals := []*uint64{&s.user, &s.nice, &s.system, &s.idle, &s.iowait, &s.irq, &s.softirq, &s.steal}
	for i, v := range vals {
		n, err := strconv.ParseUint(fields[i+1], 10, 64)
		if err != nil {
			return cpuStat{}, err
		}
		*v = n
	}
	return s, nil
}

func (m *Manager) collectCPU(now int64) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return
	}
	defer f.Close()

	m.mu.Lock()
	defer m.mu.Unlock()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu") {
			break
		}
		fields := strings.Fields(line)
		name := fields[0] // "cpu", "cpu0", "cpu1", …
		cur, err := parseCPULine(fields)
		if err != nil {
			continue
		}
		prev, hasPrev := m.prevCPU[name]
		m.prevCPU[name] = cur
		if !hasPrev {
			continue
		}
		dt := float64(cur.total() - prev.total())
		if dt == 0 {
			continue
		}
		di := float64(cur.idleTotal() - prev.idleTotal())
		totalPct := math.Round((1-di/dt)*10000) / 100

		push := func(key string, val uint64, prevVal uint64) {
			delta := float64(val - prevVal)
			pct := math.Round(delta/dt*10000) / 100
			m.store.Push("cpu."+name+"."+key, ringbuf.Sample{TS: now, Value: pct})
		}
		m.store.Push("cpu."+name+".total", ringbuf.Sample{TS: now, Value: totalPct})
		push("user", cur.user, prev.user)
		push("nice", cur.nice, prev.nice)
		push("system", cur.system, prev.system)
		push("iowait", cur.iowait, prev.iowait)
		push("irq", cur.irq, prev.irq)
		push("softirq", cur.softirq, prev.softirq)
		push("steal", cur.steal, prev.steal)
	}
}

// ── Memory ───────────────────────────────────────────────────────────────────

func (m *Manager) collectMemory(now int64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()

	mem := make(map[string]uint64)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		val, _ := strconv.ParseUint(fields[1], 10, 64)
		mem[key] = val // kB
	}

	total := mem["MemTotal"]
	if total == 0 {
		return
	}
	available := mem["MemAvailable"]
	used := total - available
	cached := mem["Cached"] + mem["Buffers"]
	swapTotal := mem["SwapTotal"]
	swapFree := mem["SwapFree"]
	swapUsed := swapTotal - swapFree

	toMB := func(kb uint64) float64 { return math.Round(float64(kb)/1024*10) / 10 }

	m.store.Push("mem.total_mb", ringbuf.Sample{TS: now, Value: toMB(total)})
	m.store.Push("mem.used_mb", ringbuf.Sample{TS: now, Value: toMB(used)})
	m.store.Push("mem.available_mb", ringbuf.Sample{TS: now, Value: toMB(available)})
	m.store.Push("mem.cached_mb", ringbuf.Sample{TS: now, Value: toMB(cached)})
	m.store.Push("mem.used_pct", ringbuf.Sample{TS: now, Value: math.Round(float64(used)/float64(total)*10000) / 100})
	m.store.Push("mem.swap_total_mb", ringbuf.Sample{TS: now, Value: toMB(swapTotal)})
	m.store.Push("mem.swap_used_mb", ringbuf.Sample{TS: now, Value: toMB(swapUsed)})
	if swapTotal > 0 {
		m.store.Push("mem.swap_pct", ringbuf.Sample{TS: now, Value: math.Round(float64(swapUsed)/float64(swapTotal)*10000) / 100})
	}
}

// ── Disk ─────────────────────────────────────────────────────────────────────

type diskStat struct {
	readsCompleted  uint64
	readSectors     uint64
	writesCompleted uint64
	writeSectors    uint64
	ioTicks         uint64 // milliseconds spent doing I/O
	ts              time.Time
}

func (m *Manager) collectDisk(now int64) {
	f, err := os.Open("/proc/diskstats")
	if err != nil {
		return
	}
	defer f.Close()

	cur := make(map[string]diskStat)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 14 {
			continue
		}
		dev := fields[2]
		// skip loop, ram, dm- partitions (but keep nvme partitions by name pattern)
		if isSkippedDev(dev) {
			continue
		}
		var s diskStat
		s.readsCompleted, _ = strconv.ParseUint(fields[3], 10, 64)
		s.readSectors, _ = strconv.ParseUint(fields[5], 10, 64)
		s.writesCompleted, _ = strconv.ParseUint(fields[7], 10, 64)
		s.writeSectors, _ = strconv.ParseUint(fields[9], 10, 64)
		s.ioTicks, _ = strconv.ParseUint(fields[12], 10, 64)
		s.ts = time.Now()
		cur[dev] = s
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	t := time.Now()
	for dev, c := range cur {
		p, ok := m.prevDisk[dev]
		m.prevDisk[dev] = c
		if !ok {
			continue
		}
		dt := t.Sub(p.ts).Seconds()
		if dt <= 0 {
			dt = 1
		}
		prefix := "disk." + dev + "."
		readMB := float64(c.readSectors-p.readSectors) * 512 / dt / (1024 * 1024)
		writeMB := float64(c.writeSectors-p.writeSectors) * 512 / dt / (1024 * 1024)
		readIOPS := float64(c.readsCompleted-p.readsCompleted) / dt
		writeIOPS := float64(c.writesCompleted-p.writesCompleted) / dt
		util := math.Min(100, float64(c.ioTicks-p.ioTicks)/(dt*10))

		m.store.Push(prefix+"read_mbs", ringbuf.Sample{TS: now, Value: round2(readMB)})
		m.store.Push(prefix+"write_mbs", ringbuf.Sample{TS: now, Value: round2(writeMB)})
		m.store.Push(prefix+"read_iops", ringbuf.Sample{TS: now, Value: round2(readIOPS)})
		m.store.Push(prefix+"write_iops", ringbuf.Sample{TS: now, Value: round2(writeIOPS)})
		m.store.Push(prefix+"util_pct", ringbuf.Sample{TS: now, Value: round2(util)})
	}
}

func isSkippedDev(dev string) bool {
	for _, pfx := range []string{"loop", "ram", "sr", "fd"} {
		if strings.HasPrefix(dev, pfx) {
			return true
		}
	}
	// skip partitions like sda1, vda2 (but not sda, vda, nvme0n1p1 is kept)
	if len(dev) > 0 {
		last := dev[len(dev)-1]
		if last >= '0' && last <= '9' {
			// nvme devices: nvme0n1 is a disk, nvme0n1p1 is a partition
			if strings.Contains(dev, "nvme") && strings.Contains(dev, "p") {
				return true
			}
			// sda1, vda2, xvda3 — skip numbered partitions of non-nvme disks
			if !strings.Contains(dev, "nvme") {
				return true
			}
		}
	}
	return false
}

// ── Network ───────────────────────────────────────────────────────────────────

type netStat struct {
	rxBytes uint64
	txBytes uint64
	ts      time.Time
}

func (m *Manager) collectNetwork(now int64) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return
	}
	defer f.Close()

	cur := make(map[string]netStat)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, ":") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		iface := strings.TrimSpace(parts[0])
		if iface == "lo" {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 9 {
			continue
		}
		rxBytes, _ := strconv.ParseUint(fields[0], 10, 64)
		txBytes, _ := strconv.ParseUint(fields[8], 10, 64)
		cur[iface] = netStat{rxBytes: rxBytes, txBytes: txBytes, ts: time.Now()}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	t := time.Now()
	for iface, c := range cur {
		p, ok := m.prevNet[iface]
		m.prevNet[iface] = c
		if !ok {
			continue
		}
		dt := t.Sub(p.ts).Seconds()
		if dt <= 0 {
			dt = 1
		}
		prefix := "net." + iface + "."
		rxMB := float64(c.rxBytes-p.rxBytes) / dt / (1024 * 1024)
		txMB := float64(c.txBytes-p.txBytes) / dt / (1024 * 1024)
		m.store.Push(prefix+"rx_mbs", ringbuf.Sample{TS: now, Value: round3(rxMB)})
		m.store.Push(prefix+"tx_mbs", ringbuf.Sample{TS: now, Value: round3(txMB)})
		m.store.Push(prefix+"rx_total_mb", ringbuf.Sample{TS: now, Value: round2(float64(c.rxBytes) / (1024 * 1024))})
		m.store.Push(prefix+"tx_total_mb", ringbuf.Sample{TS: now, Value: round2(float64(c.txBytes) / (1024 * 1024))})
	}
}

// ── Load average ──────────────────────────────────────────────────────────────

func (m *Manager) collectLoadAvg(now int64) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return
	}
	for i, key := range []string{"load.1m", "load.5m", "load.15m"} {
		v, err := strconv.ParseFloat(fields[i], 64)
		if err == nil {
			m.store.Push(key, ringbuf.Sample{TS: now, Value: v})
		}
	}
	// running/total processes
	if len(fields) >= 4 {
		parts := strings.SplitN(fields[3], "/", 2)
		if len(parts) == 2 {
			running, _ := strconv.ParseFloat(parts[0], 64)
			total, _ := strconv.ParseFloat(parts[1], 64)
			m.store.Push("procs.running", ringbuf.Sample{TS: now, Value: running})
			m.store.Push("procs.total", ringbuf.Sample{TS: now, Value: total})
		}
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// DeviceNames returns all block device names currently tracked.
func (m *Manager) DeviceNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.prevDisk))
	for k := range m.prevDisk {
		names = append(names, k)
	}
	return names
}

// InterfaceNames returns all network interface names currently tracked.
func (m *Manager) InterfaceNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.prevNet))
	for k := range m.prevNet {
		names = append(names, k)
	}
	return names
}

// Hostname returns the system hostname.
func Hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

// Uptime returns system uptime in seconds.
func Uptime() float64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(fields[0], 64)
	return v
}

// CPUCount returns the number of logical CPUs from /proc/stat.
func CPUCount() int {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 1
	}
	defer f.Close()
	count := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "cpu") && line[3] >= '0' && line[3] <= '9' {
			count++
		}
	}
	if count == 0 {
		return 1
	}
	return count
}

// PluginDir is the directory scanned for external plugin executables.
var PluginDir = "/etc/rhelmon/plugins"

// RunPlugins executes all executables in PluginDir, expecting them to emit
// newline-delimited "key value" pairs (value is a float64).
// Results are pushed into the store under the "plugin.<name>.<key>" namespace.
func RunPlugins(store *ringbuf.Store, now int64) {
	entries, err := filepath.Glob(filepath.Join(PluginDir, "*"))
	if err != nil || len(entries) == 0 {
		return
	}
	// Plugin execution left as an extension point — skipped in Phase 1.
	_ = entries
}
