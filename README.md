# rhelmon

**Lightweight real-time system monitoring agent for RHEL and SUSE**

Zero dependencies. Single binary. Netdata-style dashboard.

[![Release](https://img.shields.io/github/v/release/amit25sep/rhelmon)](https://github.com/amit25sep/rhelmon/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

---

## Install

### One-liner (RHEL 9 / Rocky / AlmaLinux / openSUSE)

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/amit25sep/rhelmon/main/scripts/install.sh)
```

### Direct RPM

```bash
# RHEL 9 / Rocky Linux 9 / AlmaLinux 9
rpm -ivh https://github.com/amit25sep/rhelmon/releases/latest/download/rhelmon-0.1.0-1.el9.x86_64.rpm

# With dnf (handles dependencies automatically)
dnf install -y https://github.com/amit25sep/rhelmon/releases/latest/download/rhelmon-0.1.0-1.el9.x86_64.rpm
```

### After install

```bash
systemctl enable --now rhelmon
firewall-cmd --add-port=9000/tcp --permanent && firewall-cmd --reload
```

Open `http://your-server-ip:9000` in your browser.

---

## Features

| Feature | Details |
|---|---|
| **Dashboard** | Netdata-style live dashboard over WebSocket — no browser plugin |
| **Metrics** | CPU (per-core), memory, disk I/O, network, load average — from `/proc` |
| **History** | 1 hour of in-memory ring buffer (3600 samples/metric at 1s resolution) |
| **Alerts** | Threshold-based engine: ok → pending → firing → resolved. Slack + email |
| **TSDB export** | Prometheus remote write + InfluxDB line protocol |
| **Plugins** | Drop any executable in `/etc/rhelmon/plugins/` — metrics appear instantly |
| **Self-monitor** | `/metrics` endpoint (Prometheus format), goroutine/heap/GC tracking |
| **Packaging** | RPM with systemd unit, logrotate, dedicated system user, hardened unit |

---

## Configuration

Edit `/etc/rhelmon/rhelmon.conf`, then restart:

```bash
vi /etc/rhelmon/rhelmon.conf
systemctl restart rhelmon
```

Key settings:

```bash
ADDR=:9000                              # listen address
SLACK_WEBHOOK=https://hooks.slack.com/... # Slack alerts
PROM_URL=http://localhost:9090/api/v1/write # Prometheus remote write
INFLUX_URL=http://localhost:8086        # InfluxDB
```

---

## API Endpoints

| Endpoint | Description |
|---|---|
| `GET /` | Live dashboard |
| `GET /api/metrics` | Latest snapshot (JSON) |
| `GET /api/history?metric=cpu.cpu.total&n=300` | Ring buffer history |
| `GET /api/alerts` | Alert rule states (JSON) |
| `GET /api/plugins` | Plugin metric values (JSON) |
| `GET /api/health` | Health check |
| `GET /api/selfmon` | Agent runtime stats |
| `GET /metrics` | Prometheus text format |
| `WS  /ws` | WebSocket stream |

---

## Writing a plugin

Any executable in `/etc/rhelmon/plugins/` is run every 30 seconds. Print `key value` to stdout:

```bash
cat > /etc/rhelmon/plugins/myapp.sh << 'EOF'
#!/bin/bash
echo "queue_depth $(redis-cli llen myqueue)"
echo "workers $(pgrep -c myworker || echo 0)"
EOF
chmod +x /etc/rhelmon/plugins/myapp.sh
```

Metrics appear as `plugin.myapp.queue_depth` on the dashboard within 30 seconds. No restart needed.

Bundled examples: `nginx_plugin.sh`, `redis_plugin.sh`, `postgres_plugin.py`, `systemd_services_plugin.sh`

---

## Build from source

```bash
git clone https://github.com/amit25sep/rhelmon
cd rhelmon
go mod tidy
make build-rhel          # binary only
make rpm-full            # binary + RPM
```

Requires Go 1.21+.

---

## Service management

```bash
systemctl start rhelmon      # start
systemctl stop rhelmon       # stop
systemctl restart rhelmon    # restart
systemctl status rhelmon     # status
journalctl -u rhelmon -f     # live logs
```

---

## Supported platforms

| Distribution | Versions | Architecture |
|---|---|---|
| RHEL | 8, 9 | x86_64, aarch64 |
| Rocky Linux | 8, 9 | x86_64, aarch64 |
| AlmaLinux | 8, 9 | x86_64, aarch64 |
| CentOS Stream | 8, 9 | x86_64, aarch64 |
| openSUSE Leap | 15.x | x86_64 |
| SLES | 15 | x86_64 |

---

## License

MIT
