// Package web serves the HTTP API, WebSocket endpoint, and the embedded dashboard SPA.
package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/rhelmon/agent/internal/alert"
	"github.com/rhelmon/agent/internal/plugin"
	"github.com/rhelmon/agent/internal/selfmon"
	"github.com/rhelmon/agent/internal/collector"
	"github.com/rhelmon/agent/internal/hub"
	"github.com/rhelmon/agent/internal/ringbuf"
)

// Server wires together the HTTP mux, the hub, the store, the collector, and the alert engine.
type Server struct {
	mux     *http.ServeMux
	hub     *hub.Hub
	store   *ringbuf.Store
	mgr     *collector.Manager
	engine  *alert.Engine
	plugins *plugin.Loader
	mon     *selfmon.Monitor
	startTS time.Time
}

// New creates a Server.
func New(h *hub.Hub, store *ringbuf.Store, mgr *collector.Manager, engine *alert.Engine, pl *plugin.Loader, mon *selfmon.Monitor) *Server {
	s := &Server{
		mux:     http.NewServeMux(),
		hub:     h,
		store:   store,
		mgr:     mgr,
		engine:  engine,
		plugins: pl,
		mon:     mon,
		startTS: time.Now(),
	}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("/ws", s.hub.ServeWS)
	s.mux.HandleFunc("/api/metrics", s.handleMetrics)
	s.mux.HandleFunc("/api/history", s.handleHistory)
	s.mux.HandleFunc("/api/health", s.handleHealth)
	s.mux.HandleFunc("/api/alerts", s.handleAlerts)
	s.mux.HandleFunc("/api/plugins", s.handlePlugins)
	s.mux.HandleFunc("/metrics", s.handlePrometheusMetrics)
	s.mux.HandleFunc("/api/selfmon", s.handleSelfMon)
	s.mux.HandleFunc("/", s.handleDashboard)
}

// ── Snapshot ──────────────────────────────────────────────────────────────────

// MetricSnapshot is the JSON payload sent to the browser every second.
type MetricSnapshot struct {
	TS       int64                  `json:"ts"`
	Hostname string                 `json:"hostname"`
	Uptime   UptimeInfo             `json:"uptime"`
	CPU      CPUSnapshot            `json:"cpu"`
	Memory   MemSnapshot            `json:"memory"`
	Disk     []DiskSnapshot         `json:"disk"`
	Network  []NetworkSnapshot      `json:"network"`
	LoadAvg  LoadAvgSnapshot        `json:"loadavg"`
	Alerts   []alert.AlertSnapshot  `json:"alerts"`
	Clients  int                    `json:"ws_clients"`
}

type UptimeInfo struct {
	Seconds float64 `json:"seconds"`
	Human   string  `json:"human"`
}

type CPUSnapshot struct {
	Total   float64 `json:"total"`
	User    float64 `json:"user"`
	System  float64 `json:"system"`
	IOWait  float64 `json:"iowait"`
	SoftIRQ float64 `json:"softirq"`
	Steal   float64 `json:"steal"`
	Nice    float64 `json:"nice"`
	IRQ     float64 `json:"irq"`
}

type MemSnapshot struct {
	TotalMB     float64 `json:"total_mb"`
	UsedMB      float64 `json:"used_mb"`
	AvailableMB float64 `json:"available_mb"`
	CachedMB    float64 `json:"cached_mb"`
	UsedPct     float64 `json:"used_pct"`
	SwapTotalMB float64 `json:"swap_total_mb"`
	SwapUsedMB  float64 `json:"swap_used_mb"`
	SwapPct     float64 `json:"swap_pct"`
}

type DiskSnapshot struct {
	Device    string  `json:"device"`
	ReadMBs   float64 `json:"read_mbs"`
	WriteMBs  float64 `json:"write_mbs"`
	ReadIOPS  float64 `json:"read_iops"`
	WriteIOPS float64 `json:"write_iops"`
	UtilPct   float64 `json:"util_pct"`
}

type NetworkSnapshot struct {
	Iface     string  `json:"iface"`
	RxMBs     float64 `json:"rx_mbs"`
	TxMBs     float64 `json:"tx_mbs"`
	RxTotalMB float64 `json:"rx_total_mb"`
	TxTotalMB float64 `json:"tx_total_mb"`
}

type LoadAvgSnapshot struct {
	Load1m  float64 `json:"1m"`
	Load5m  float64 `json:"5m"`
	Load15m float64 `json:"15m"`
	Running float64 `json:"running"`
	Total   float64 `json:"total"`
}

func latestF(store *ringbuf.Store, key string) float64 {
	s, ok := store.Latest(key)
	if !ok {
		return 0
	}
	return s.Value
}

// BuildSnapshot reads the latest values from the store and assembles a snapshot.
func BuildSnapshot(store *ringbuf.Store, mgr *collector.Manager, engine *alert.Engine, wsClients int) MetricSnapshot {
	now := time.Now()
	upSec := collector.Uptime()
	upDays := int(upSec / 86400)
	upHours := int(upSec/3600) % 24
	upMins := int(upSec/60) % 60

	snap := MetricSnapshot{
		TS:       now.Unix(),
		Hostname: collector.Hostname(),
		Uptime: UptimeInfo{
			Seconds: upSec,
			Human:   fmt.Sprintf("%dd %dh %dm", upDays, upHours, upMins),
		},
		CPU: CPUSnapshot{
			Total:   latestF(store, "cpu.cpu.total"),
			User:    latestF(store, "cpu.cpu.user"),
			System:  latestF(store, "cpu.cpu.system"),
			IOWait:  latestF(store, "cpu.cpu.iowait"),
			SoftIRQ: latestF(store, "cpu.cpu.softirq"),
			Steal:   latestF(store, "cpu.cpu.steal"),
			Nice:    latestF(store, "cpu.cpu.nice"),
			IRQ:     latestF(store, "cpu.cpu.irq"),
		},
		Memory: MemSnapshot{
			TotalMB:     latestF(store, "mem.total_mb"),
			UsedMB:      latestF(store, "mem.used_mb"),
			AvailableMB: latestF(store, "mem.available_mb"),
			CachedMB:    latestF(store, "mem.cached_mb"),
			UsedPct:     latestF(store, "mem.used_pct"),
			SwapTotalMB: latestF(store, "mem.swap_total_mb"),
			SwapUsedMB:  latestF(store, "mem.swap_used_mb"),
			SwapPct:     latestF(store, "mem.swap_pct"),
		},
		LoadAvg: LoadAvgSnapshot{
			Load1m:  latestF(store, "load.1m"),
			Load5m:  latestF(store, "load.5m"),
			Load15m: latestF(store, "load.15m"),
			Running: latestF(store, "procs.running"),
			Total:   latestF(store, "procs.total"),
		},
		Clients: wsClients,
	}

	// Alert snapshots
	if engine != nil {
		snap.Alerts = engine.AllAlerts()
	}

	// Disk devices
	devs := mgr.DeviceNames()
	sort.Strings(devs)
	for _, dev := range devs {
		pfx := "disk." + dev + "."
		snap.Disk = append(snap.Disk, DiskSnapshot{
			Device:    dev,
			ReadMBs:   latestF(store, pfx+"read_mbs"),
			WriteMBs:  latestF(store, pfx+"write_mbs"),
			ReadIOPS:  latestF(store, pfx+"read_iops"),
			WriteIOPS: latestF(store, pfx+"write_iops"),
			UtilPct:   latestF(store, pfx+"util_pct"),
		})
	}

	// Network interfaces
	ifaces := mgr.InterfaceNames()
	sort.Strings(ifaces)
	for _, iface := range ifaces {
		pfx := "net." + iface + "."
		snap.Network = append(snap.Network, NetworkSnapshot{
			Iface:     iface,
			RxMBs:     latestF(store, pfx+"rx_mbs"),
			TxMBs:     latestF(store, pfx+"tx_mbs"),
			RxTotalMB: latestF(store, pfx+"rx_total_mb"),
			TxTotalMB: latestF(store, pfx+"tx_total_mb"),
		})
	}

	return snap
}

// ── HTTP handlers ─────────────────────────────────────────────────────────────

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	snap := BuildSnapshot(s.store, s.mgr, s.engine, s.hub.ClientCount())
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(snap)
}

type HistoryResponse struct {
	Metric  string           `json:"metric"`
	Samples []ringbuf.Sample `json:"samples"`
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	metric := r.URL.Query().Get("metric")
	if metric == "" {
		http.Error(w, "metric param required", http.StatusBadRequest)
		return
	}
	n := 300
	if nStr := r.URL.Query().Get("n"); nStr != "" {
		fmt.Sscanf(nStr, "%d", &n)
	}
	samples := s.store.Last(metric, n)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(HistoryResponse{Metric: metric, Samples: samples})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":     "ok",
		"uptime_s":   time.Since(s.startTS).Seconds(),
		"ws_clients": s.hub.ClientCount(),
		"hostname":   collector.Hostname(),
	})
}

func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	var snaps []alert.AlertSnapshot
	if s.engine != nil {
		snaps = s.engine.AllAlerts()
	}
	if snaps == nil {
		snaps = []alert.AlertSnapshot{}
	}
	json.NewEncoder(w).Encode(snaps)
}

func (s *Server) handlePlugins(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	type pluginMetric struct {
		Name   string  `json:"name"`
		Metric string  `json:"metric"`
		Value  float64 `json:"value"`
		TS     int64   `json:"ts"`
	}
	var results []pluginMetric
	for _, name := range s.store.Names() {
		if !strings.HasPrefix(name, "plugin.") {
			continue
		}
		sample, ok := s.store.Latest(name)
		if !ok {
			continue
		}
		results = append(results, pluginMetric{
			Name:   name,
			Metric: name,
			Value:  sample.Value,
			TS:     sample.TS,
		})
	}
	if results == nil {
		results = []pluginMetric{}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Metric < results[j].Metric })
	json.NewEncoder(w).Encode(results)
}

func (s *Server) handlePrometheusMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if s.mon != nil {
		fmt.Fprint(w, s.mon.PrometheusMetrics(collector.Hostname()))
	} else {
		fmt.Fprintf(w, "rhelmon_up{host=\"%s\"} 1\n", collector.Hostname())
	}
}

func (s *Server) handleSelfMon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if s.mon == nil {
		json.NewEncoder(w).Encode(map[string]string{"status": "disabled"})
		return
	}
	json.NewEncoder(w).Encode(s.mon.Snapshot())
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, dashboardHTML)
}

// BroadcastLoop runs in a goroutine; builds a snapshot every interval and pushes to all WS clients.
func BroadcastLoop(h *hub.Hub, store *ringbuf.Store, mgr *collector.Manager, engine *alert.Engine, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		if h.ClientCount() == 0 {
			continue
		}
		snap := BuildSnapshot(store, mgr, engine, h.ClientCount())
		h.Broadcast(snap)
	}
}



// ── Embedded dashboard ────────────────────────────────────────────────────────

var dashboardHTML = buildDashboardHTML()

func buildDashboardHTML() string {
	return strings.ReplaceAll(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>rhelmon</title>
<style>
  *{box-sizing:border-box;margin:0;padding:0}
  :root{
    --bg:#0f1117;--bg2:#181c27;--bg3:#1e2235;--border:#2a2f45;
    --text:#e8ecf4;--muted:#6b7799;
    --green:#4ade80;--red:#f87171;--amber:#fbbf24;
    --blue:#60a5fa;--teal:#2dd4bf;--purple:#a78bfa;
    --rhel:#cc0000;
  }
  body{background:var(--bg);color:var(--text);font-family:'JetBrains Mono',ui-monospace,monospace;font-size:13px}

  /* topbar */
  .topbar{display:flex;align-items:center;gap:10px;padding:8px 16px;background:var(--bg2);border-bottom:1px solid var(--border);flex-wrap:wrap;position:sticky;top:0;z-index:10}
  .rhel-dot{width:10px;height:10px;border-radius:50%;background:var(--rhel);flex-shrink:0}
  .topbar-title{font-weight:700;font-size:13px;letter-spacing:.06em}
  .badge{font-size:10px;padding:2px 7px;border-radius:12px;font-weight:700;letter-spacing:.04em}
  .badge-live{background:rgba(74,222,128,.15);color:var(--green);border:1px solid rgba(74,222,128,.3)}
  .badge-ws{background:rgba(96,165,250,.12);color:var(--blue);border:1px solid rgba(96,165,250,.25)}
  .badge-off{background:rgba(248,113,113,.12);color:var(--red);border:1px solid rgba(248,113,113,.25)}
  .topbar-right{margin-left:auto;display:flex;align-items:center;gap:10px}
  .topbar-time{font-size:11px;color:var(--muted);font-family:monospace}
  .pulse{width:8px;height:8px;border-radius:50%;background:var(--green);flex-shrink:0}
  .pulse.live{animation:pulse 2s infinite}
  .pulse.dead{background:var(--red);animation:none}
  @keyframes pulse{0%,100%{opacity:1}50%{opacity:.3}}

  /* nav tabs */
  .nav{display:flex;gap:0;padding:0 16px;background:var(--bg2);border-bottom:1px solid var(--border);overflow-x:auto}
  .nav-tab{padding:8px 14px;font-size:12px;color:var(--muted);cursor:pointer;border-bottom:2px solid transparent;white-space:nowrap;user-select:none}
  .nav-tab:hover{color:var(--text)}
  .nav-tab.active{color:var(--text);border-bottom-color:var(--rhel)}

  /* panels */
  .panel{display:none;padding:14px 16px}
  .panel.active{display:block}

  /* section */
  .sec-lbl{font-size:10px;font-weight:700;letter-spacing:.1em;color:var(--muted);text-transform:uppercase;margin-bottom:10px;display:flex;align-items:center;gap:6px}
  .sec-lbl::after{content:'';flex:1;height:1px;background:var(--border)}

  /* gauges */
  .gauges{display:grid;grid-template-columns:repeat(auto-fit,minmax(120px,1fr));gap:8px;margin-bottom:14px}
  .g-card{background:var(--bg2);border:1px solid var(--border);border-radius:10px;padding:10px;display:flex;flex-direction:column;align-items:center;gap:5px}
  .g-lbl{font-size:10px;color:var(--muted);text-align:center}
  .g-wrap{position:relative;width:74px;height:74px}
  .g-wrap svg{width:74px;height:74px;transform:rotate(-90deg)}
  .g-center{position:absolute;top:50%;left:50%;transform:translate(-50%,-50%);text-align:center;pointer-events:none}
  .g-num{font-size:15px;font-weight:700;line-height:1.1}
  .g-unit{font-size:9px;color:var(--muted)}
  .big-val{font-size:26px;font-weight:700}

  /* mini metric cards */
  .mini-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(130px,1fr));gap:8px;margin-bottom:14px}
  .mini{background:var(--bg2);border:1px solid var(--border);border-radius:8px;padding:10px 12px}
  .mini-lbl{font-size:10px;color:var(--muted);margin-bottom:3px}
  .mini-val{font-size:19px;font-weight:700}
  .bar{height:3px;background:var(--border);border-radius:2px;margin-top:6px;overflow:hidden}
  .bar-fill{height:100%;border-radius:2px;transition:width .5s ease}

  /* chart */
  .chart-box{background:var(--bg2);border:1px solid var(--border);border-radius:10px;padding:12px;margin-bottom:12px}
  .chart-hdr{display:flex;align-items:center;justify-content:space-between;margin-bottom:8px}
  .chart-title{font-size:11px;color:var(--muted)}
  .legend{display:flex;flex-wrap:wrap;gap:10px;margin-top:7px}
  .leg{display:flex;align-items:center;gap:4px;font-size:10px;color:var(--muted)}
  .leg-sq{width:8px;height:8px;border-radius:2px;flex-shrink:0}

  /* table */
  .tbl-box{background:var(--bg2);border:1px solid var(--border);border-radius:10px;overflow:auto;margin-bottom:14px}
  table{width:100%;border-collapse:collapse;font-size:11px}
  th{padding:6px 10px;color:var(--muted);font-weight:500;border-bottom:1px solid var(--border);white-space:nowrap;font-size:10px;letter-spacing:.05em;text-transform:uppercase;text-align:left}
  td{padding:5px 10px;border-bottom:1px solid rgba(42,47,69,.5);white-space:nowrap}
  tr:last-child td{border-bottom:none}
  .teal{color:var(--teal);font-weight:600}

  /* two-col */
  .two-col{display:grid;grid-template-columns:1fr 1fr;gap:10px;margin-bottom:14px}
  @media(max-width:600px){.two-col{grid-template-columns:1fr}}

  /* history range controls */
  .range-btns{display:flex;gap:4px}
  .rbtn{font-size:10px;padding:3px 8px;border:1px solid var(--border);border-radius:6px;background:transparent;color:var(--muted);cursor:pointer}
  .rbtn.active{background:rgba(204,0,0,.15);color:#ff6b6b;border-color:rgba(204,0,0,.3)}

  /* connection toast */
  #toast{position:fixed;bottom:16px;right:16px;background:var(--bg2);border:1px solid var(--border);border-radius:8px;padding:8px 14px;font-size:12px;color:var(--muted);opacity:0;transition:opacity .3s;pointer-events:none;z-index:99}
  #toast.show{opacity:1}
</style>
</head>
<body>

<div class="topbar">
  <div class="rhel-dot"></div>
  <span class="topbar-title">RHELMON</span>
  <span id="topbar-host" style="font-size:12px;color:var(--muted)">connecting…</span>
  <div id="pulse-dot" class="pulse dead"></div>
  <span id="conn-badge" class="badge badge-off">WS</span>
  <div class="topbar-right">
    <span id="ws-clients" class="badge badge-ws" style="display:none">0 viewers</span>
    <span id="uptime-lbl" style="font-size:11px;color:var(--muted)"></span>
    <span id="topbar-time" class="topbar-time"></span>
  </div>
</div>

<div class="nav">
  <div class="nav-tab active" onclick="showPanel('overview',this)">Overview</div>
  <div class="nav-tab" onclick="showPanel('cpu',this)">CPU</div>
  <div class="nav-tab" onclick="showPanel('memory',this)">Memory</div>
  <div class="nav-tab" onclick="showPanel('disk',this)">Disk</div>
  <div class="nav-tab" onclick="showPanel('network',this)">Network</div>
  <div class="nav-tab" onclick="showPanel('alerts',this);loadAlerts()">Alerts</div>
  <div class="nav-tab" onclick="showPanel('plugins',this);loadPlugins()">Plugins</div>
  <div class="nav-tab" onclick="showPanel('selfmon',this);loadSelfMon()">Agent</div>
</div>

<!-- CPU PANEL -->
<div id="panel-cpu" class="panel">
  <div class="sec-lbl"><span class="rhel-dot" style="width:7px;height:7px"></span>CPU utilization</div>
  <div class="mini-grid">
    <div class="mini"><div class="mini-lbl">Total</div><div class="mini-val"><span id="c-total">—</span>%</div><div class="bar"><div id="cb-total" class="bar-fill" style="width:0%"></div></div></div>
    <div class="mini"><div class="mini-lbl">User</div><div class="mini-val"><span id="c-user">—</span>%</div><div class="bar"><div id="cb-user" class="bar-fill" style="width:0%;background:#f87171"></div></div></div>
    <div class="mini"><div class="mini-lbl">System</div><div class="mini-val"><span id="c-sys">—</span>%</div><div class="bar"><div id="cb-sys" class="bar-fill" style="width:0%;background:#fbbf24"></div></div></div>
    <div class="mini"><div class="mini-lbl">IOWait</div><div class="mini-val"><span id="c-iowait">—</span>%</div><div class="bar"><div id="cb-iowait" class="bar-fill" style="width:0%;background:#60a5fa"></div></div></div>
    <div class="mini"><div class="mini-lbl">SoftIRQ</div><div class="mini-val"><span id="c-sirq">—</span>%</div><div class="bar"><div id="cb-sirq" class="bar-fill" style="width:0%;background:#a78bfa"></div></div></div>
    <div class="mini"><div class="mini-lbl">Steal</div><div class="mini-val"><span id="c-steal">—</span>%</div><div class="bar"><div id="cb-steal" class="bar-fill" style="width:0%;background:#888780"></div></div></div>
    <div class="mini"><div class="mini-lbl">Load 1m</div><div class="mini-val" id="load-1m">—</div></div>
    <div class="mini"><div class="mini-lbl">Load 5m</div><div class="mini-val" id="load-5m">—</div></div>
    <div class="mini"><div class="mini-lbl">Processes</div><div class="mini-val"><span id="procs-run">—</span><span style="font-size:12px;color:var(--muted)"> / <span id="procs-tot">—</span></span></div></div>
  </div>
  <div class="chart-box">
    <div class="chart-hdr">
      <span class="chart-title">CPU · /proc/stat · 1s resolution</span>
      <div class="range-btns">
        <button class="rbtn active" onclick="setRange('cpu',60,this)">1m</button>
        <button class="rbtn" onclick="setRange('cpu',300,this)">5m</button>
        <button class="rbtn" onclick="setRange('cpu',900,this)">15m</button>
        <button class="rbtn" onclick="setRange('cpu',3600,this)">1h</button>
      </div>
    </div>
    <div style="position:relative;height:180px"><canvas id="cpu-chart"></canvas></div>
    <div class="legend">
      <span class="leg"><span class="leg-sq" style="background:#f87171"></span>user</span>
      <span class="leg"><span class="leg-sq" style="background:#fbbf24"></span>system</span>
      <span class="leg"><span class="leg-sq" style="background:#60a5fa"></span>iowait</span>
      <span class="leg"><span class="leg-sq" style="background:#a78bfa"></span>softirq</span>
      <span class="leg"><span class="leg-sq" style="background:#888780"></span>steal</span>
    </div>
  </div>
</div>

<!-- MEMORY PANEL -->
<div id="panel-memory" class="panel">
  <div class="sec-lbl"><span class="rhel-dot" style="width:7px;height:7px"></span>Memory · /proc/meminfo</div>
  <div class="mini-grid">
    <div class="mini"><div class="mini-lbl">Used</div><div class="mini-val"><span id="m-used">—</span> <span style="font-size:11px;color:var(--muted)">MB</span></div><div class="bar"><div id="mb-used" class="bar-fill" style="width:0%;background:#a78bfa"></div></div></div>
    <div class="mini"><div class="mini-lbl">Available</div><div class="mini-val"><span id="m-avail">—</span> <span style="font-size:11px;color:var(--muted)">MB</span></div><div class="bar"><div id="mb-avail" class="bar-fill" style="width:0%;background:#4ade80"></div></div></div>
    <div class="mini"><div class="mini-lbl">Cached</div><div class="mini-val"><span id="m-cached">—</span> <span style="font-size:11px;color:var(--muted)">MB</span></div><div class="bar"><div id="mb-cached" class="bar-fill" style="width:0%;background:#60a5fa"></div></div></div>
    <div class="mini"><div class="mini-lbl">Total</div><div class="mini-val"><span id="m-total">—</span> <span style="font-size:11px;color:var(--muted)">MB</span></div></div>
    <div class="mini"><div class="mini-lbl">Swap used</div><div class="mini-val"><span id="m-swap">—</span> <span style="font-size:11px;color:var(--muted)">MB</span></div><div class="bar"><div id="mb-swap" class="bar-fill" style="width:0%;background:#f87171"></div></div></div>
    <div class="mini"><div class="mini-lbl">Used %</div><div class="mini-val"><span id="m-pct">—</span>%</div></div>
  </div>
  <div class="chart-box">
    <div class="chart-hdr">
      <span class="chart-title">Memory usage over time</span>
      <div class="range-btns">
        <button class="rbtn active" onclick="setRange('mem',60,this)">1m</button>
        <button class="rbtn" onclick="setRange('mem',300,this)">5m</button>
        <button class="rbtn" onclick="setRange('mem',900,this)">15m</button>
        <button class="rbtn" onclick="setRange('mem',3600,this)">1h</button>
      </div>
    </div>
    <div style="position:relative;height:180px"><canvas id="mem-chart"></canvas></div>
    <div class="legend">
      <span class="leg"><span class="leg-sq" style="background:#a78bfa"></span>used MB</span>
      <span class="leg"><span class="leg-sq" style="background:#60a5fa"></span>cached MB</span>
    </div>
  </div>
</div>

<!-- DISK PANEL -->
<div id="panel-disk" class="panel">
  <div class="sec-lbl"><span class="rhel-dot" style="width:7px;height:7px"></span>Block devices · /proc/diskstats</div>
  <div class="gauges" id="disk-gauges">
    <div class="g-card"><div class="g-lbl">Read IOPS</div>
      <div class="g-wrap"><svg viewBox="0 0 74 74"><circle cx="37" cy="37" r="29" fill="none" stroke="#2a2f45" stroke-width="6"/><circle id="ga-riops" cx="37" cy="37" r="29" fill="none" stroke="#4ade80" stroke-width="6" stroke-dasharray="0 182" stroke-linecap="round"/></svg>
      <div class="g-center"><div class="g-num" id="g-riops">—</div><div class="g-unit">ops/s</div></div></div></div>
    <div class="g-card"><div class="g-lbl">Write IOPS</div>
      <div class="g-wrap"><svg viewBox="0 0 74 74"><circle cx="37" cy="37" r="29" fill="none" stroke="#2a2f45" stroke-width="6"/><circle id="ga-wiops" cx="37" cy="37" r="29" fill="none" stroke="#f87171" stroke-width="6" stroke-dasharray="0 182" stroke-linecap="round"/></svg>
      <div class="g-center"><div class="g-num" id="g-wiops">—</div><div class="g-unit">ops/s</div></div></div></div>
    <div class="g-card"><div class="g-lbl">Read MiB/s</div>
      <div class="g-wrap"><svg viewBox="0 0 74 74"><circle cx="37" cy="37" r="29" fill="none" stroke="#2a2f45" stroke-width="6"/><circle id="ga-rmb" cx="37" cy="37" r="29" fill="none" stroke="#4ade80" stroke-width="6" stroke-dasharray="0 182" stroke-linecap="round"/></svg>
      <div class="g-center"><div class="g-num" id="g-rmb">—</div><div class="g-unit">MiB/s</div></div></div></div>
    <div class="g-card"><div class="g-lbl">Write MiB/s</div>
      <div class="g-wrap"><svg viewBox="0 0 74 74"><circle cx="37" cy="37" r="29" fill="none" stroke="#2a2f45" stroke-width="6"/><circle id="ga-wmb" cx="37" cy="37" r="29" fill="none" stroke="#f87171" stroke-width="6" stroke-dasharray="0 182" stroke-linecap="round"/></svg>
      <div class="g-center"><div class="g-num" id="g-wmb">—</div><div class="g-unit">MiB/s</div></div></div></div>
    <div class="g-card"><div class="g-lbl">Max util</div>
      <div class="big-val"><span id="g-util">—</span><span style="font-size:13px;color:var(--muted)">%</span></div>
      <div class="bar" style="width:80px;margin-top:4px"><div id="gb-util" class="bar-fill" style="width:0%"></div></div></div>
  </div>
  <div class="tbl-box">
    <table><thead><tr>
      <th>Device</th><th>Read MiB/s</th><th>Write MiB/s</th><th>Read IOPS</th><th>Write IOPS</th><th>Utilization</th>
    </tr></thead><tbody id="disk-tbody"></tbody></table>
  </div>
  <div class="chart-box">
    <div class="chart-hdr"><span class="chart-title">Disk throughput (all devices)</span></div>
    <div style="position:relative;height:150px"><canvas id="disk-chart"></canvas></div>
    <div class="legend">
      <span class="leg"><span class="leg-sq" style="background:#4ade80"></span>read MiB/s</span>
      <span class="leg"><span class="leg-sq" style="background:#f87171"></span>write MiB/s</span>
    </div>
  </div>
</div>

<!-- NETWORK PANEL -->
<div id="panel-network" class="panel">
  <div class="sec-lbl"><span class="rhel-dot" style="width:7px;height:7px"></span>Network · /proc/net/dev</div>
  <div class="tbl-box">
    <table><thead><tr>
      <th>Interface</th><th>RX MiB/s</th><th>TX MiB/s</th><th>RX Total MB</th><th>TX Total MB</th>
    </tr></thead><tbody id="net-tbody"></tbody></table>
  </div>
  <div class="chart-box">
    <div class="chart-hdr">
      <span class="chart-title">Network throughput (all interfaces)</span>
      <div class="range-btns">
        <button class="rbtn active" onclick="setRange('net',60,this)">1m</button>
        <button class="rbtn" onclick="setRange('net',300,this)">5m</button>
        <button class="rbtn" onclick="setRange('net',3600,this)">1h</button>
      </div>
    </div>
    <div style="position:relative;height:160px"><canvas id="net-chart"></canvas></div>
    <div class="legend">
      <span class="leg"><span class="leg-sq" style="background:#4ade80"></span>RX MiB/s</span>
      <span class="leg"><span class="leg-sq" style="background:#f87171"></span>TX MiB/s</span>
    </div>
  </div>
</div>

<!-- OVERVIEW PANEL -->
<div id="panel-overview" class="panel active">
  <div class="sec-lbl"><span class="rhel-dot" style="width:7px;height:7px"></span>System overview</div>
  <div class="gauges">
    <div class="g-card"><div class="g-lbl">CPU total</div>
      <div class="g-wrap"><svg viewBox="0 0 74 74"><circle cx="37" cy="37" r="29" fill="none" stroke="#2a2f45" stroke-width="6"/><circle id="ov-cpu-arc" cx="37" cy="37" r="29" fill="none" stroke="#fbbf24" stroke-width="6" stroke-dasharray="0 182" stroke-linecap="round"/></svg>
      <div class="g-center"><div class="g-num" id="ov-cpu">—</div><div class="g-unit">%</div></div></div></div>
    <div class="g-card"><div class="g-lbl">Memory</div>
      <div class="g-wrap"><svg viewBox="0 0 74 74"><circle cx="37" cy="37" r="29" fill="none" stroke="#2a2f45" stroke-width="6"/><circle id="ov-mem-arc" cx="37" cy="37" r="29" fill="none" stroke="#a78bfa" stroke-width="6" stroke-dasharray="0 182" stroke-linecap="round"/></svg>
      <div class="g-center"><div class="g-num" id="ov-mem">—</div><div class="g-unit">%</div></div></div></div>
    <div class="g-card"><div class="g-lbl">Disk max util</div>
      <div class="g-wrap"><svg viewBox="0 0 74 74"><circle cx="37" cy="37" r="29" fill="none" stroke="#2a2f45" stroke-width="6"/><circle id="ov-disk-arc" cx="37" cy="37" r="29" fill="none" stroke="#2dd4bf" stroke-width="6" stroke-dasharray="0 182" stroke-linecap="round"/></svg>
      <div class="g-center"><div class="g-num" id="ov-disk">—</div><div class="g-unit">%</div></div></div></div>
    <div class="g-card"><div class="g-lbl">Load 1m</div><div class="big-val" id="ov-load">—</div></div>
  </div>
  <div class="two-col">
    <div class="chart-box" style="margin-bottom:0"><div class="chart-hdr"><span class="chart-title">CPU</span></div><div style="position:relative;height:120px"><canvas id="ov-cpu-chart"></canvas></div></div>
    <div class="chart-box" style="margin-bottom:0"><div class="chart-hdr"><span class="chart-title">Memory</span></div><div style="position:relative;height:120px"><canvas id="ov-mem-chart"></canvas></div></div>
  </div>
</div>

<div id="toast"></div>

<script src="https://cdnjs.cloudflare.com/ajax/libs/Chart.js/4.4.1/chart.umd.js"></script>
<script>
const CIRC = 2 * Math.PI * 29;
const gridC = 'rgba(255,255,255,0.05)';
const tickC = '#4a5568';

function makeChart(id, datasets, yLabel, maxY) {
  const opts = {
    responsive:true, maintainAspectRatio:false, animation:{duration:0},
    plugins:{legend:{display:false}},
    scales:{
      x:{ticks:{color:tickC,maxTicksLimit:6,font:{size:9}},grid:{color:gridC}},
      y:{min:0,ticks:{color:tickC,font:{size:9},callback:v=>v+(yLabel||'')},grid:{color:gridC}}
    },
    interaction:{mode:'index',intersect:false}
  };
  if(maxY) opts.scales.y.max = maxY;
  return new Chart(document.getElementById(id), {
    type:'line',
    data:{labels:Array(120).fill(''), datasets},
    options: opts
  });
}

function ds(color, fill) {
  return {
    data: Array(120).fill(null),
    fill, borderColor:color, borderWidth:1.5, pointRadius:0, tension:0.4,
    backgroundColor: color.replace(')',',0.2)').replace('rgb','rgba')
  };
}

const charts = {
  cpu:  makeChart('cpu-chart',  [ds('#f87171',true),ds('#fbbf24',true),ds('#60a5fa',true),ds('#a78bfa',true),ds('#888780',true)], '%', 100),
  mem:  makeChart('mem-chart',  [ds('#a78bfa',true),ds('#60a5fa',true)], ' MB'),
  disk: makeChart('disk-chart', [ds('#4ade80',false),ds('#f87171',false)], ' MiB/s'),
  net:  makeChart('net-chart',  [ds('#4ade80',false),ds('#f87171',false)], ' MiB/s'),
  ovCpu: makeChart('ov-cpu-chart', [ds('#fbbf24',true)], '%', 100),
  ovMem: makeChart('ov-mem-chart', [ds('#a78bfa',true)], ' MB'),
};

const ranges = {cpu:60, mem:60, net:60};
function setRange(key, n, btn) {
  ranges[key] = n;
  btn.closest('.range-btns').querySelectorAll('.rbtn').forEach(b=>b.classList.remove('active'));
  btn.classList.add('active');
  // fetch history and reload chart
  loadHistory(key);
}

async function loadHistory(key) {
  const metricMap = {
    cpu: ['cpu.cpu.user','cpu.cpu.system','cpu.cpu.iowait','cpu.cpu.softirq','cpu.cpu.steal'],
    mem: ['mem.used_mb','mem.cached_mb'],
    net: ['net_rx','net_tx'],
  };
  const n = ranges[key] || 60;
  const chart = {cpu:charts.cpu, mem:charts.mem, net:charts.net}[key];
  if(!chart) return;

  // For net, we need to aggregate across interfaces — skip for now and rely on live pushes
  if(key === 'net') return;

  try {
    const results = await Promise.all(
      metricMap[key].map(m => fetch('/api/history?metric='+m+'&n='+n).then(r=>r.json()))
    );
    const maxLen = Math.max(...results.map(r=>r.samples?.length||0));
    if(maxLen === 0) return;
    const labels = Array(maxLen).fill('');
    chart.data.labels = labels;
    results.forEach((r,i) => {
      const vals = (r.samples||[]).map(s=>s.value);
      // pad front if needed
      const padded = Array(maxLen - vals.length).fill(null).concat(vals);
      chart.data.datasets[i].data = padded;
    });
    chart.update('none');
  } catch(e) { console.warn('history load failed', e); }
}

function arcDA(pct) {
  const fill = Math.min(pct/100,1) * CIRC;
  return fill.toFixed(1)+' '+(CIRC-fill).toFixed(1);
}
function colorPct(p) { return p>80?'#f87171':p>60?'#fbbf24':'#4ade80'; }
function set(id,v) { const e=document.getElementById(id); if(e) e.textContent=v; }
function setBar(id,pct,color) {
  const e=document.getElementById(id);
  if(!e) return;
  e.style.width=Math.min(pct,100)+'%';
  if(color) e.style.background=color;
  else e.style.background=colorPct(pct);
}
function setArc(id,pct,color) {
  const e=document.getElementById(id);
  if(!e) return;
  e.setAttribute('stroke-dasharray', arcDA(pct));
  if(color) e.setAttribute('stroke',color);
  else e.setAttribute('stroke',colorPct(pct));
}
function pushChart(chart, vals) {
  chart.data.labels.push(''); chart.data.labels.shift();
  vals.forEach((v,i)=>{ chart.data.datasets[i].data.push(v); chart.data.datasets[i].data.shift(); });
  chart.update('none');
}
function showPanel(name, tab) {
  document.querySelectorAll('.panel').forEach(p=>p.classList.remove('active'));
  document.querySelectorAll('.nav-tab').forEach(t=>t.classList.remove('active'));
  document.getElementById('panel-'+name).classList.add('active');
  tab.classList.add('active');
}
function toast(msg) {
  const t = document.getElementById('toast');
  t.textContent = msg;
  t.classList.add('show');
  setTimeout(()=>t.classList.remove('show'), 2500);
}

function applySnapshot(d) {
  // topbar
  set('topbar-host', d.hostname||'');
  set('topbar-time', new Date().toLocaleTimeString());
  if(d.uptime) set('uptime-lbl', 'up '+d.uptime.human);
  if(d.ws_clients !== undefined) {
    const el = document.getElementById('ws-clients');
    el.style.display = '';
    el.textContent = d.ws_clients+' viewer'+(d.ws_clients===1?'':'s');
  }

  // CPU
  const c = d.cpu || {};
  set('c-total', c.total?.toFixed(1));  setBar('cb-total', c.total||0);
  set('c-user',  c.user?.toFixed(1));   setBar('cb-user',  c.user||0,  '#f87171');
  set('c-sys',   c.system?.toFixed(1)); setBar('cb-sys',   c.system||0,'#fbbf24');
  set('c-iowait',c.iowait?.toFixed(1)); setBar('cb-iowait',c.iowait||0,'#60a5fa');
  set('c-sirq',  c.softirq?.toFixed(1));setBar('cb-sirq',  c.softirq||0,'#a78bfa');
  set('c-steal', c.steal?.toFixed(1));  setBar('cb-steal', c.steal||0, '#888780');
  pushChart(charts.cpu, [c.user||0, c.system||0, c.iowait||0, c.softirq||0, c.steal||0]);
  pushChart(charts.ovCpu, [c.total||0]);
  setArc('ov-cpu-arc', c.total||0, '#fbbf24');
  set('ov-cpu', (c.total||0).toFixed(0));

  // Load
  const l = d.loadavg || {};
  set('load-1m', l['1m']?.toFixed(2));
  set('load-5m', l['5m']?.toFixed(2));
  set('procs-run', l.running||0);
  set('procs-tot', l.total||0);
  set('ov-load', l['1m']?.toFixed(2));

  // Memory
  const m = d.memory || {};
  set('m-used',   m.used_mb||0);   setBar('mb-used',  m.used_pct||0, '#a78bfa');
  set('m-avail',  m.available_mb||0); setBar('mb-avail',100-(m.used_pct||0),'#4ade80');
  set('m-cached', m.cached_mb||0); setBar('mb-cached', m.total_mb>0?m.cached_mb/m.total_mb*100:0,'#60a5fa');
  set('m-total',  m.total_mb||0);
  set('m-swap',   m.swap_used_mb||0); setBar('mb-swap',m.swap_pct||0,'#f87171');
  set('m-pct',    (m.used_pct||0).toFixed(1));
  pushChart(charts.mem, [m.used_mb||0, m.cached_mb||0]);
  pushChart(charts.ovMem, [m.used_mb||0]);
  setArc('ov-mem-arc', m.used_pct||0, '#a78bfa');
  set('ov-mem', (m.used_pct||0).toFixed(0));

  // Disk
  const disk = d.disk || [];
  let totR=0, totW=0, totRI=0, totWI=0, maxU=0;
  disk.forEach(dv=>{ totR+=dv.read_mbs; totW+=dv.write_mbs; totRI+=dv.read_iops; totWI+=dv.write_iops; maxU=Math.max(maxU,dv.util_pct); });
  set('g-riops', totRI.toFixed(0)); setArc('ga-riops', Math.min(totRI/200*100,100), '#4ade80');
  set('g-wiops', totWI.toFixed(0)); setArc('ga-wiops', Math.min(totWI/200*100,100), '#f87171');
  set('g-rmb',   totR.toFixed(2));  setArc('ga-rmb',   Math.min(totR/500*100,100),  '#4ade80');
  set('g-wmb',   totW.toFixed(2));  setArc('ga-wmb',   Math.min(totW/500*100,100),  '#f87171');
  set('g-util',  maxU.toFixed(1));  setBar('gb-util',  maxU);
  setArc('ov-disk-arc', maxU, '#2dd4bf');
  set('ov-disk', maxU.toFixed(0));
  pushChart(charts.disk, [totR, totW]);

  const tbody = document.getElementById('disk-tbody');
  tbody.innerHTML = '';
  disk.forEach(dv => {
    const uc = colorPct(dv.util_pct);
    tbody.innerHTML += '<tr>'
      +'<td class="teal">'+dv.device+'</td>'
      +'<td style="color:#4ade80">'+dv.read_mbs.toFixed(2)+'</td>'
      +'<td style="color:#f87171">'+dv.write_mbs.toFixed(2)+'</td>'
      +'<td>'+dv.read_iops.toFixed(0)+'</td>'
      +'<td>'+dv.write_iops.toFixed(0)+'</td>'
      +'<td><div style="display:flex;align-items:center;gap:6px">'
        +'<div style="width:60px;height:3px;background:#2a2f45;border-radius:2px;overflow:hidden"><div style="width:'+Math.min(dv.util_pct,100)+'%;height:100%;background:'+uc+'"></div></div>'
        +'<span style="color:'+uc+'">'+dv.util_pct.toFixed(1)+'%</span>'
      +'</div></td>'
    +'</tr>';
  });
  if(!disk.length) tbody.innerHTML='<tr><td colspan="6" style="color:var(--muted);text-align:center;padding:14px">No block devices</td></tr>';

  // Network
  const net = d.network || [];
  let rxSum=0, txSum=0;
  net.forEach(n=>{ rxSum+=n.rx_mbs; txSum+=n.tx_mbs; });
  pushChart(charts.net, [rxSum, txSum]);

  const ntbody = document.getElementById('net-tbody');
  ntbody.innerHTML = '';
  net.forEach(n => {
    ntbody.innerHTML += '<tr>'
      +'<td class="teal">'+n.iface+'</td>'
      +'<td style="color:#4ade80">'+n.rx_mbs.toFixed(3)+'</td>'
      +'<td style="color:#f87171">'+n.tx_mbs.toFixed(3)+'</td>'
      +'<td>'+n.rx_total_mb.toFixed(1)+'</td>'
      +'<td>'+n.tx_total_mb.toFixed(1)+'</td>'
    +'</tr>';
  });
  if(!net.length) ntbody.innerHTML='<tr><td colspan="5" style="color:var(--muted);text-align:center;padding:14px">No interfaces</td></tr>';
}

// ── WebSocket connection with auto-reconnect ─────────────────────────────────
let ws, reconnTimer;
function connect() {
  const proto = location.protocol==='https:'?'wss':'ws';
  ws = new WebSocket(proto+'://'+location.host+'/ws');

  ws.onopen = () => {
    document.getElementById('pulse-dot').className = 'pulse live';
    document.getElementById('conn-badge').className = 'badge badge-live';
    document.getElementById('conn-badge').textContent = 'WS';
    clearTimeout(reconnTimer);
    toast('Connected via WebSocket');
  };
  ws.onmessage = e => {
    try { applySnapshot(JSON.parse(e.data)); } catch(err) { console.warn(err); }
  };
  ws.onclose = () => {
    document.getElementById('pulse-dot').className = 'pulse dead';
    document.getElementById('conn-badge').className = 'badge badge-off';
    document.getElementById('conn-badge').textContent = 'OFF';
    toast('Disconnected — reconnecting…');
    reconnTimer = setTimeout(connect, 3000);
  };
  ws.onerror = () => ws.close();
}

// Seed charts from history on first load, then connect WS
(async () => {
  await loadHistory('cpu');
  await loadHistory('mem');
  connect();
})();

function loadAlerts() {
  fetch('/api/alerts').then(r=>r.json()).then(data => {
    const c = document.getElementById('alerts-container');
    if(!data || !data.length){ c.innerHTML='<div style="color:var(--muted);font-size:12px;padding:20px 0">No alert rules configured.</div>'; return; }
    const stateColor = {ok:'#4ade80', pending:'#fbbf24', firing:'#f87171'};
    const sevColor = {critical:'#f87171', warning:'#fbbf24', info:'#60a5fa'};
    c.innerHTML = data.map(a => {
      const sc = stateColor[a.state]||'#888';
      const sev = sevColor[a.severity]||'#888';
      const summary = a.annotations && a.annotations.summary ? a.annotations.summary : '';
      return '<div style="background:var(--bg2);border:1px solid var(--border);border-left:3px solid '+sc+';border-radius:8px;padding:10px 14px;margin-bottom:8px">'
        +'<div style="display:flex;align-items:center;gap:8px;margin-bottom:4px">'
        +'<span style="font-size:11px;font-weight:700;color:'+sc+'">'+a.state.toUpperCase()+'</span>'
        +'<span style="font-size:13px;font-weight:600;color:var(--text)">'+a.name+'</span>'
        +'<span style="margin-left:auto;font-size:10px;padding:2px 7px;border-radius:10px;background:rgba(255,255,255,.06);color:'+sev+'">'+a.severity+'</span>'
        +'</div>'
        +'<div style="font-size:11px;color:var(--muted);margin-bottom:3px">'+a.metric+' '+a.op+' '+a.threshold+' &nbsp;·&nbsp; for '+a.for_duration+'</div>'
        +(summary ? '<div style="font-size:11px;color:var(--muted)">'+summary+'</div>' : '')
        +'<div style="font-size:11px;color:var(--muted);margin-top:4px">current value: <span style="color:var(--text)">'+a.value.toFixed(2)+'</span></div>'
        +'</div>';
    }).join('');
  }).catch(()=>{});
}


function loadPlugins() {
  fetch('/api/plugins').then(r=>r.json()).then(data => {
    const c = document.getElementById('plugins-container');
    if(!data || !data.length){
      c.innerHTML='<div style="color:var(--muted);font-size:12px;padding:20px 0">No plugin metrics yet. Add executables to <code>/etc/rhelmon/plugins/</code></div>';
      return;
    }
    // group by plugin name (plugin.<name>.<key>)
    const groups = {};
    data.forEach(m => {
      const parts = m.metric.split('.');
      const grp = parts.length >= 2 ? parts[1] : 'unknown';
      if(!groups[grp]) groups[grp] = [];
      groups[grp].push(m);
    });
    c.innerHTML = Object.keys(groups).sort().map(grp => {
      const metrics = groups[grp];
      const rows = metrics.map(m => {
        const key = m.metric.split('.').slice(2).join('.');
        return '<tr>'
          +'<td class="teal">'+key+'</td>'
          +'<td style="color:var(--text)">'+m.value.toFixed(3)+'</td>'
          +'<td style="color:var(--muted)">'+new Date(m.ts*1000).toLocaleTimeString()+'</td>'
        +'</tr>';
      }).join('');
      return '<div style="margin-bottom:14px">'
        +'<div style="font-size:11px;font-weight:700;color:var(--muted);letter-spacing:.08em;text-transform:uppercase;margin-bottom:6px">'+grp+'</div>'
        +'<div class="tbl-box"><table>'
        +'<thead><tr><th>Metric</th><th>Value</th><th>Last update</th></tr></thead>'
        +'<tbody>'+rows+'</tbody>'
        +'</table></div></div>';
    }).join('');
  }).catch(()=>{});
}


function loadSelfMon() {
  fetch('/api/selfmon').then(r=>r.json()).then(d => {
    const grid = document.getElementById('selfmon-grid');
    const runtime = document.getElementById('selfmon-runtime');
    if(!d || d.status === 'disabled') {
      grid.innerHTML = '<div style="color:var(--muted);font-size:12px">Self-monitor not available.</div>';
      return;
    }
    function card(label, val, sub) {
      return '<div class="mini"><div class="mini-lbl">'+label+'</div>'
        +'<div class="mini-val" style="font-size:16px">'+val+'</div>'
        +(sub?'<div style="font-size:10px;color:var(--muted);margin-top:2px">'+sub+'</div>':'')
        +'</div>';
    }
    grid.innerHTML =
      card('Uptime', d.uptime_human || '-') +
      card('Metric series', d.metric_series, 'in ring buffer') +
      card('Ring buf samples', (d.ringbuf_samples_total||0).toLocaleString()) +
      card('Alerts fired', d.alerts_fired, 'total') +
      card('Alerts resolved', d.alerts_resolved, 'total') +
      card('TSDB points sent', (d.tsdb_points_sent||0).toLocaleString()) +
      card('Plugin errors', d.plugin_errors, d.plugin_errors>0?'check journalctl':'ok') +
      card('Collect errors', d.collect_errors, d.collect_errors>0?'check /proc access':'ok');
    runtime.innerHTML =
      card('Go version', d.go_version || '-') +
      card('Goroutines', d.goroutines) +
      card('Heap alloc', (d.heap_alloc_mb||0).toFixed(1)+' MB') +
      card('Heap sys', (d.heap_sys_mb||0).toFixed(1)+' MB') +
      card('GC runs', d.gc_runs) +
      card('Last GC pause', (d.last_gc_pause_ms||0).toFixed(2)+' ms') +
      card('CPUs', d.num_cpu) +
      card('WS messages', (d.ws_messages_sent||0).toLocaleString(), 'sent to browsers');
  }).catch(()=>{});
}

</script>

<!-- ALERTS PANEL -->
<div id="panel-alerts" class="panel">
  <div class="sec-lbl"><span class="rhel-dot" style="width:7px;height:7px"></span>Alert rules</div>
  <div id="alerts-container">
    <div style="color:var(--muted);font-size:12px;padding:20px 0">Loading…</div>
  </div>
</div>

<!-- PLUGINS PANEL -->
<div id="panel-plugins" class="panel">
  <div class="sec-lbl"><span class="rhel-dot" style="width:7px;height:7px"></span>External plugins · /etc/rhelmon/plugins/</div>
  <div id="plugins-info" style="font-size:12px;color:var(--muted);margin-bottom:12px">
    Drop any executable into <code style="background:rgba(255,255,255,.08);padding:1px 5px;border-radius:4px">/etc/rhelmon/plugins/</code> — it runs every 30s and its output appears here.
  </div>
  <div id="plugins-container">
    <div style="color:var(--muted);font-size:12px;padding:20px 0">Loading…</div>
  </div>
</div>

<!-- SELF-MONITOR PANEL -->
<div id="panel-selfmon" class="panel">
  <div class="sec-lbl"><span class="rhel-dot" style="width:7px;height:7px"></span>Agent self-monitor</div>
  <div id="selfmon-grid" class="mini-grid" style="margin-bottom:14px"></div>
  <div class="sec-lbl" style="margin-top:4px"><span class="rhel-dot" style="width:7px;height:7px;background:#60a5fa"></span>Runtime</div>
  <div id="selfmon-runtime" class="mini-grid"></div>
  <div style="margin-top:14px;font-size:11px;color:var(--muted)">
    Prometheus metrics: <a href="/metrics" style="color:var(--blue)">/metrics</a> &nbsp;·&nbsp;
    JSON: <a href="/api/selfmon" style="color:var(--blue)">/api/selfmon</a>
  </div>
</div>
</body>
</html>`, "REPLACE_NOTHING", "")
}
