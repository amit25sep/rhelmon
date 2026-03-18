package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rhelmon/agent/internal/alert"
	"github.com/rhelmon/agent/internal/collector"
	"github.com/rhelmon/agent/internal/hub"
	"github.com/rhelmon/agent/internal/plugin"
	"github.com/rhelmon/agent/internal/ringbuf"
	"github.com/rhelmon/agent/internal/selfmon"
	"github.com/rhelmon/agent/internal/tsdb"
	"github.com/rhelmon/agent/internal/web"
)

func main() {
	// ── Core ──────────────────────────────────────────────────────────────────
	addr              := flag.String("addr", ":9000", "listen address")
	bufCap            := flag.Int("history", ringbuf.DefaultCap, "ring buffer capacity (samples/metric)")
	collectInterval   := flag.Duration("interval", time.Second, "collection interval")
	broadcastInterval := flag.Duration("broadcast", time.Second, "WebSocket broadcast interval")

	// ── Plugin loader ─────────────────────────────────────────────────────────
	pluginDir      := flag.String("plugin-dir", plugin.DefaultPluginDir, "directory to scan for plugin executables")
	pluginInterval := flag.Duration("plugin-interval", plugin.DefaultInterval, "how often to run plugins")
	pluginTimeout  := flag.Duration("plugin-timeout", plugin.DefaultTimeout, "max execution time per plugin")

	// ── Alert notifiers ───────────────────────────────────────────────────────
	slackWebhook := flag.String("slack-webhook", "", "Slack incoming webhook URL")
	slackChannel := flag.String("slack-channel", "", "Slack channel override")
	smtpHost     := flag.String("smtp-host", "", "SMTP host for email alerts")
	smtpPort     := flag.Int("smtp-port", 587, "SMTP port")
	smtpUser     := flag.String("smtp-user", "", "SMTP username")
	smtpPass     := flag.String("smtp-pass", "", "SMTP password")
	smtpFrom     := flag.String("smtp-from", "", "alert sender address")
	smtpTo       := flag.String("smtp-to", "", "comma-separated alert recipient addresses")

	// ── Prometheus remote write ───────────────────────────────────────────────
	promURL      := flag.String("prom-url", "", "Prometheus remote write URL")
	promUser     := flag.String("prom-user", "", "Prometheus basic auth username")
	promPassword := flag.String("prom-password", "", "Prometheus basic auth password")
	promBearer   := flag.String("prom-bearer", "", "Prometheus Bearer token")
	promInterval := flag.Duration("prom-interval", 15*time.Second, "Prometheus flush interval")

	// ── InfluxDB ──────────────────────────────────────────────────────────────
	influxURL      := flag.String("influx-url", "", "InfluxDB base URL")
	influxToken    := flag.String("influx-token", "", "InfluxDB v2 API token")
	influxOrg      := flag.String("influx-org", "", "InfluxDB v2 organisation")
	influxBucket   := flag.String("influx-bucket", "", "InfluxDB v2 bucket")
	influxV1DB     := flag.String("influx-v1db", "", "InfluxDB v1 database name")
	influxV1User   := flag.String("influx-v1user", "", "InfluxDB v1 username")
	influxV1Pass   := flag.String("influx-v1pass", "", "InfluxDB v1 password")
	influxInterval := flag.Duration("influx-interval", 15*time.Second, "InfluxDB flush interval")

	flag.Parse()

	// ── Pipeline ──────────────────────────────────────────────────────────────
	store  := ringbuf.NewStore(*bufCap)
	mgr    := collector.New(store, *collectInterval)
	wsHub  := hub.New()
	mon    := selfmon.New(store)

	// ── Plugin loader ─────────────────────────────────────────────────────────
	pluginLoader := plugin.New(store, *pluginDir, *pluginInterval, *pluginTimeout)

	// ── Alert notifiers ───────────────────────────────────────────────────────
	var toAddrs []string
	for _, a := range splitTrim(*smtpTo) {
		if a != "" {
			toAddrs = append(toAddrs, a)
		}
	}
	notifier := alert.NewMultiNotifier(
		&alert.LogNotifier{},
		alert.NewSlackNotifier(alert.SlackConfig{
			WebhookURL: *slackWebhook,
			Channel:    *slackChannel,
			Username:   "rhelmon",
		}),
		alert.NewEmailNotifier(alert.EmailConfig{
			SMTPHost: *smtpHost,
			SMTPPort: *smtpPort,
			Username: *smtpUser,
			Password: *smtpPass,
			From:     *smtpFrom,
			To:       toAddrs,
		}),
	)

	// ── Alert engine ──────────────────────────────────────────────────────────
	engine := alert.New(store, notifier, 5*time.Second)
	for _, r := range alert.DefaultRules() {
		engine.AddRule(r)
	}

	// ── TSDB writers ──────────────────────────────────────────────────────────
	hostname := collector.Hostname()

	promWriter := tsdb.NewPrometheusWriter(tsdb.PrometheusConfig{
		URL:               *promURL,
		BasicAuthUser:     *promUser,
		BasicAuthPassword: *promPassword,
		BearerToken:       *promBearer,
	})
	if promWriter != nil {
		promMgr := tsdb.NewManager(store, hostname, *promInterval)
		promMgr.AddWriter(promWriter)
		promMgr.Start()
		defer promMgr.Stop()
		log.Printf("tsdb: prometheus remote write → %s (every %s)", *promURL, *promInterval)
	}

	influxWriter := tsdb.NewInfluxWriter(tsdb.InfluxConfig{
		URL:        *influxURL,
		Token:      *influxToken,
		Org:        *influxOrg,
		Bucket:     *influxBucket,
		V1Database: *influxV1DB,
		V1Username: *influxV1User,
		V1Password: *influxV1Pass,
	})
	if influxWriter != nil {
		influxMgr := tsdb.NewManager(store, hostname, *influxInterval)
		influxMgr.AddWriter(influxWriter)
		influxMgr.Start()
		defer influxMgr.Stop()
		log.Printf("tsdb: influxdb → %s (every %s)", *influxURL, *influxInterval)
	}

	// ── Web server ────────────────────────────────────────────────────────────
	srv := web.New(wsHub, store, mgr, engine, pluginLoader, mon)

	// ── Start everything ──────────────────────────────────────────────────────
	mgr.Start()
	defer mgr.Stop()
	engine.Start()
	defer engine.Stop()
	pluginLoader.Start()
	defer pluginLoader.Stop()
	// Start watchdog — restarts collector if it stops sending heartbeats
	mon.StartWatchdog(30*time.Second, map[string]time.Duration{
		"collector": 10 * time.Second,
	})
	go web.BroadcastLoop(wsHub, store, mgr, engine, *broadcastInterval)

	httpSrv := &http.Server{
		Addr:         *addr,
		Handler:      srv,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rhelmon: cannot bind %s: %v\n", *addr, err)
		os.Exit(1)
	}
	printBanner(hostname, ln.Addr().String(), promWriter != nil, influxWriter != nil)
	go func() {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatalf("rhelmon: http: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	fmt.Printf("\nrhelmon: received %s — shutting down\n", sig)
}

func splitTrim(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func printBanner(hostname, addr string, prom, influx bool) {
	tsdbStatus := "disabled"
	if prom && influx {
		tsdbStatus = "prometheus + influxdb"
	} else if prom {
		tsdbStatus = "prometheus remote write"
	} else if influx {
		tsdbStatus = "influxdb"
	}
	fmt.Printf(`
  ██████╗ ██╗  ██╗███████╗██╗     ███╗   ███╗ ██████╗ ███╗   ██╗
  ██╔══██╗██║  ██║██╔════╝██║     ████╗ ████║██╔═══██╗████╗  ██║
  ██████╔╝███████║█████╗  ██║     ██╔████╔██║██║   ██║██╔██╗ ██║
  ██╔══██╗██╔══██║██╔══╝  ██║     ██║╚██╔╝██║██║   ██║██║╚██╗██║
  ██║  ██║██║  ██║███████╗███████╗██║ ╚═╝ ██║╚██████╔╝██║ ╚████║
  ╚═╝  ╚═╝╚═╝  ╚═╝╚══════╝╚══════╝╚═╝     ╚═╝ ╚═════╝ ╚═╝  ╚═══╝
                    Phase 4 — Plugin Loader

  Host       : %s
  Dashboard  : http://%s
  Plugins    : http://%s/api/plugins
  Alerts     : http://%s/api/alerts
  Health     : http://%s/api/health
  TSDB       : %s
  Plugin dir : /etc/rhelmon/plugins/

  Press Ctrl+C to stop.

`, hostname, addr, addr, addr, addr, addr, addr, tsdbStatus)
}
