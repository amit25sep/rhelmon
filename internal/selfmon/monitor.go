// Package selfmon tracks the rhelmon agent's own runtime health.
//
// It exposes:
//   - /metrics  — Prometheus text format (agent self-metrics + all collected metrics)
//   - SelfStats — struct polled by the web layer for the dashboard Self-Monitor tab
//   - Watchdog  — detects stuck collector goroutines and triggers restarts
//   - Counters  — thread-safe error / missed-tick counters for collectors to call
package selfmon

import (
	"fmt"
	"math"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rhelmon/agent/internal/ringbuf"
)

// Monitor is the central self-monitoring component.
type Monitor struct {
	store     *ringbuf.Store
	startTime time.Time

	// Atomic counters — collectors increment these on errors/missed ticks.
	collectErrors  atomic.Int64
	missedTicks    atomic.Int64
	wsMessages     atomic.Int64
	alertFired     atomic.Int64
	alertResolved  atomic.Int64
	pluginErrors   atomic.Int64
	tsdbErrors     atomic.Int64
	tsdbPointsSent atomic.Int64

	// Watchdog
	mu          sync.Mutex
	lastSeen    map[string]time.Time // component name → last heartbeat
	watchdogCbs map[string]func()   // component name → restart callback
}

// New creates a Monitor.
func New(store *ringbuf.Store) *Monitor {
	return &Monitor{
		store:       store,
		startTime:   time.Now(),
		lastSeen:    make(map[string]time.Time),
		watchdogCbs: make(map[string]func()),
	}
}

// ── Counters (called by other packages) ──────────────────────────────────────

func (m *Monitor) IncCollectError()   { m.collectErrors.Add(1) }
func (m *Monitor) IncMissedTick()     { m.missedTicks.Add(1) }
func (m *Monitor) IncWSMessage()      { m.wsMessages.Add(1) }
func (m *Monitor) IncAlertFired()     { m.alertFired.Add(1) }
func (m *Monitor) IncAlertResolved()  { m.alertResolved.Add(1) }
func (m *Monitor) IncPluginError()    { m.pluginErrors.Add(1) }
func (m *Monitor) IncTSDBError()      { m.tsdbErrors.Add(1) }
func (m *Monitor) AddTSDBPoints(n int64) { m.tsdbPointsSent.Add(n) }

// ── Watchdog ──────────────────────────────────────────────────────────────────

// RegisterWatchdog registers a component with the watchdog.
// onStuck is called if the component does not call Heartbeat within timeout.
func (m *Monitor) RegisterWatchdog(name string, timeout time.Duration, onStuck func()) {
	m.mu.Lock()
	m.lastSeen[name] = time.Now()
	m.watchdogCbs[name] = onStuck
	m.mu.Unlock()
}

// Heartbeat signals that the named component is alive.
func (m *Monitor) Heartbeat(name string) {
	m.mu.Lock()
	m.lastSeen[name] = time.Now()
	m.mu.Unlock()
}

// StartWatchdog launches the watchdog loop, checking every interval.
func (m *Monitor) StartWatchdog(checkInterval time.Duration, timeouts map[string]time.Duration) {
	if checkInterval <= 0 {
		checkInterval = 30 * time.Second
	}
	go func() {
		ticker := time.NewTicker(checkInterval)
		defer ticker.Stop()
		for range ticker.C {
			m.mu.Lock()
			for name, last := range m.lastSeen {
				timeout := timeouts[name]
				if timeout <= 0 {
					timeout = 60 * time.Second
				}
				if time.Since(last) > timeout {
					cb := m.watchdogCbs[name]
					m.mu.Unlock()
					fmt.Printf("selfmon: watchdog: [%s] missed heartbeat (last seen %s ago) — triggering restart\n",
						name, time.Since(last).Round(time.Second))
					if cb != nil {
						go cb()
					}
					m.mu.Lock()
					m.lastSeen[name] = time.Now() // reset so we don't fire repeatedly
				}
			}
			m.mu.Unlock()
		}
	}()
}

// ── Stats snapshot ────────────────────────────────────────────────────────────

// Stats is a snapshot of the agent's runtime health.
type Stats struct {
	UptimeSeconds   float64 `json:"uptime_seconds"`
	UptimeHuman     string  `json:"uptime_human"`
	GoVersion       string  `json:"go_version"`
	NumGoroutine    int     `json:"goroutines"`
	NumCPU          int     `json:"num_cpu"`
	HeapAllocMB     float64 `json:"heap_alloc_mb"`
	HeapSysMB       float64 `json:"heap_sys_mb"`
	StackInUseMB    float64 `json:"stack_inuse_mb"`
	GCRuns          uint32  `json:"gc_runs"`
	LastGCPauseMS   float64 `json:"last_gc_pause_ms"`
	CollectErrors   int64   `json:"collect_errors"`
	MissedTicks     int64   `json:"missed_ticks"`
	WSMessages      int64   `json:"ws_messages_sent"`
	AlertsFired     int64   `json:"alerts_fired"`
	AlertsResolved  int64   `json:"alerts_resolved"`
	PluginErrors    int64   `json:"plugin_errors"`
	TSDBErrors      int64   `json:"tsdb_errors"`
	TSDBPointsSent  int64   `json:"tsdb_points_sent"`
	MetricSeries    int     `json:"metric_series"`
	RingBufSamples  int64   `json:"ringbuf_samples_total"`
}

// Snapshot returns a current Stats snapshot.
func (m *Monitor) Snapshot() Stats {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	uptime := time.Since(m.startTime)
	days := int(uptime.Hours()) / 24
	hours := int(uptime.Hours()) % 24
	mins := int(uptime.Minutes()) % 60
	secs := int(uptime.Seconds()) % 60
	var humanParts []string
	if days > 0 {
		humanParts = append(humanParts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		humanParts = append(humanParts, fmt.Sprintf("%dh", hours))
	}
	if mins > 0 {
		humanParts = append(humanParts, fmt.Sprintf("%dm", mins))
	}
	humanParts = append(humanParts, fmt.Sprintf("%ds", secs))

	// Count ring buffer series and total samples
	names := m.store.Names()
	var totalSamples int64
	for _, n := range names {
		samples := m.store.Last(n, 0)
		totalSamples += int64(len(samples))
	}

	lastGCPause := float64(ms.PauseNs[(ms.NumGC+255)%256]) / 1e6

	return Stats{
		UptimeSeconds:  uptime.Seconds(),
		UptimeHuman:    strings.Join(humanParts, " "),
		GoVersion:      runtime.Version(),
		NumGoroutine:   runtime.NumGoroutine(),
		NumCPU:         runtime.NumCPU(),
		HeapAllocMB:    round2(float64(ms.HeapAlloc) / (1024 * 1024)),
		HeapSysMB:      round2(float64(ms.HeapSys) / (1024 * 1024)),
		StackInUseMB:   round2(float64(ms.StackInuse) / (1024 * 1024)),
		GCRuns:         ms.NumGC,
		LastGCPauseMS:  round2(lastGCPause),
		CollectErrors:  m.collectErrors.Load(),
		MissedTicks:    m.missedTicks.Load(),
		WSMessages:     m.wsMessages.Load(),
		AlertsFired:    m.alertFired.Load(),
		AlertsResolved: m.alertResolved.Load(),
		PluginErrors:   m.pluginErrors.Load(),
		TSDBErrors:     m.tsdbErrors.Load(),
		TSDBPointsSent: m.tsdbPointsSent.Load(),
		MetricSeries:   len(names),
		RingBufSamples: totalSamples,
	}
}

// ── Prometheus text format exposition ─────────────────────────────────────────

// PrometheusMetrics returns all metrics in Prometheus text exposition format.
// This includes both agent self-metrics and all collected system metrics.
func (m *Monitor) PrometheusMetrics(hostname string) string {
	s := m.Snapshot()
	var sb strings.Builder
	now := time.Now().UnixMilli()

	writeMetric := func(name, help, typ string, value float64, labels ...string) {
		sb.WriteString("# HELP ")
		sb.WriteString(name)
		sb.WriteByte(' ')
		sb.WriteString(help)
		sb.WriteByte('\n')
		sb.WriteString("# TYPE ")
		sb.WriteString(name)
		sb.WriteByte(' ')
		sb.WriteString(typ)
		sb.WriteByte('\n')
		sb.WriteString(name)
		if len(labels) > 0 && len(labels)%2 == 0 {
			sb.WriteByte('{')
			for i := 0; i < len(labels); i += 2 {
				if i > 0 {
					sb.WriteByte(',')
				}
				sb.WriteString(labels[i])
				sb.WriteString(`="`)
				sb.WriteString(labels[i+1])
				sb.WriteByte('"')
			}
			sb.WriteByte('}')
		}
		sb.WriteByte(' ')
		sb.WriteString(formatFloat(value))
		sb.WriteByte(' ')
		sb.WriteString(fmt.Sprintf("%d", now))
		sb.WriteByte('\n')
	}

	// Agent self-metrics
	writeMetric("rhelmon_up", "Agent is running", "gauge", 1, "host", hostname)
	writeMetric("rhelmon_uptime_seconds", "Agent uptime in seconds", "counter", s.UptimeSeconds, "host", hostname)
	writeMetric("rhelmon_goroutines", "Number of goroutines", "gauge", float64(s.NumGoroutine), "host", hostname)
	writeMetric("rhelmon_heap_alloc_bytes", "Heap allocated bytes", "gauge", float64(s.HeapAllocMB*1024*1024), "host", hostname)
	writeMetric("rhelmon_heap_sys_bytes", "Heap system bytes", "gauge", float64(s.HeapSysMB*1024*1024), "host", hostname)
	writeMetric("rhelmon_gc_runs_total", "Total GC runs", "counter", float64(s.GCRuns), "host", hostname)
	writeMetric("rhelmon_last_gc_pause_ms", "Last GC pause in milliseconds", "gauge", s.LastGCPauseMS, "host", hostname)
	writeMetric("rhelmon_collect_errors_total", "Total collection errors", "counter", float64(s.CollectErrors), "host", hostname)
	writeMetric("rhelmon_missed_ticks_total", "Total missed collection ticks", "counter", float64(s.MissedTicks), "host", hostname)
	writeMetric("rhelmon_ws_messages_total", "Total WebSocket messages sent", "counter", float64(s.WSMessages), "host", hostname)
	writeMetric("rhelmon_alerts_fired_total", "Total alerts fired", "counter", float64(s.AlertsFired), "host", hostname)
	writeMetric("rhelmon_alerts_resolved_total", "Total alerts resolved", "counter", float64(s.AlertsResolved), "host", hostname)
	writeMetric("rhelmon_plugin_errors_total", "Total plugin execution errors", "counter", float64(s.PluginErrors), "host", hostname)
	writeMetric("rhelmon_tsdb_errors_total", "Total TSDB write errors", "counter", float64(s.TSDBErrors), "host", hostname)
	writeMetric("rhelmon_tsdb_points_sent_total", "Total TSDB data points sent", "counter", float64(s.TSDBPointsSent), "host", hostname)
	writeMetric("rhelmon_metric_series", "Number of active metric series in ring buffer", "gauge", float64(s.MetricSeries), "host", hostname)
	writeMetric("rhelmon_ringbuf_samples_total", "Total samples stored across all series", "gauge", float64(s.RingBufSamples), "host", hostname)

	// All collected system metrics from ring buffer
	names := m.store.Names()
	sort.Strings(names)
	for _, name := range names {
		sample, ok := m.store.Latest(name)
		if !ok {
			continue
		}
		promName := "rhelmon_" + strings.ReplaceAll(name, ".", "_")
		sb.WriteString(promName)
		sb.WriteString(`{host="`)
		sb.WriteString(hostname)
		sb.WriteString(`"} `)
		sb.WriteString(formatFloat(sample.Value))
		sb.WriteByte(' ')
		sb.WriteString(fmt.Sprintf("%d", sample.TS*1000))
		sb.WriteByte('\n')
	}

	return sb.String()
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func formatFloat(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "0"
	}
	return fmt.Sprintf("%g", v)
}
