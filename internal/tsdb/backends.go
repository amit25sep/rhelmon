// backends.go — Prometheus remote write and InfluxDB line protocol writers.
package tsdb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ── Prometheus remote write ───────────────────────────────────────────────────
//
// Implements the Prometheus remote write v1 protocol:
//   POST {url}  Content-Type: application/x-protobuf
//               Content-Encoding: snappy
//               X-Prometheus-Remote-Write-Version: 0.1.0
//
// We hand-encode the protobuf to avoid a heavy dependency.
// The wire format is straightforward for our use-case (no exemplars, no metadata).

// PrometheusConfig holds configuration for the Prometheus remote write backend.
type PrometheusConfig struct {
	// URL is the remote write endpoint, e.g.
	// "https://prometheus-prod.example.com/api/v1/write"
	// "http://localhost:9090/api/v1/write"
	// "https://prometheus-prod-01-eu-west-0.grafana.net/api/prom/push" (Grafana Cloud)
	URL string

	// BasicAuthUser / BasicAuthPassword for endpoints that require HTTP Basic auth
	// (Grafana Cloud uses these — user = numeric ID, password = API token).
	BasicAuthUser     string
	BasicAuthPassword string

	// BearerToken for Bearer-token authenticated endpoints.
	BearerToken string

	// Timeout for each HTTP POST. Defaults to 15s.
	Timeout time.Duration

	// ExtraLabels are added to every time series, e.g. {"env": "prod", "dc": "eu-west"}.
	ExtraLabels map[string]string
}

// PrometheusWriter sends metrics to a Prometheus remote write endpoint.
type PrometheusWriter struct {
	cfg    PrometheusConfig
	client *http.Client
}

// NewPrometheusWriter creates a PrometheusWriter. Returns nil if URL is empty.
func NewPrometheusWriter(cfg PrometheusConfig) *PrometheusWriter {
	if cfg.URL == "" {
		return nil
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &PrometheusWriter{cfg: cfg, client: &http.Client{Timeout: timeout}}
}

func (p *PrometheusWriter) Name() string { return "prometheus" }

func (p *PrometheusWriter) Write(points []Point) error {
	// Group points by metric name → build one TimeSeries per unique label-set.
	type seriesKey struct {
		metric string
		host   string
	}
	grouped := make(map[seriesKey][]Point)
	for _, pt := range points {
		key := seriesKey{metric: pt.Metric, host: pt.Tags["host"]}
		grouped[key] = append(grouped[key], pt)
	}

	// Encode WriteRequest protobuf manually.
	// WriteRequest { repeated TimeSeries timeseries = 1; }
	// TimeSeries   { repeated Label labels = 1; repeated Sample samples = 2; }
	// Label        { string name = 1; string value = 2; }
	// Sample       { double value = 1; int64 timestamp = 2; }
	var wr bytes.Buffer

	keys := make([]seriesKey, 0, len(grouped))
	for k := range grouped {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].metric < keys[j].metric
	})

	for _, key := range keys {
		pts := grouped[key]
		sort.Slice(pts, func(i, j int) bool { return pts[i].TS < pts[j].TS })

		// Build label set: __name__, host, any extra labels.
		metricName := prometheusMetricName(key.metric)
		labelPairs := [][2]string{
			{"__name__", metricName},
			{"host", key.host},
		}
		for k, v := range p.cfg.ExtraLabels {
			labelPairs = append(labelPairs, [2]string{k, v})
		}
		sort.Slice(labelPairs, func(i, j int) bool { return labelPairs[i][0] < labelPairs[j][0] })

		var ts bytes.Buffer
		// Encode labels (field 1 of TimeSeries)
		for _, lp := range labelPairs {
			var label bytes.Buffer
			protoWriteString(&label, 1, lp[0]) // name
			protoWriteString(&label, 2, lp[1]) // value
			protoWriteBytes(&ts, 1, label.Bytes())
		}
		// Encode samples (field 2 of TimeSeries)
		for _, pt := range pts {
			var sample bytes.Buffer
			protoWriteDouble(&sample, 1, pt.Value)
			protoWriteInt64(&sample, 2, pt.TS) // milliseconds
			protoWriteBytes(&ts, 2, sample.Bytes())
		}
		// Write TimeSeries as field 1 of WriteRequest
		protoWriteBytes(&wr, 1, ts.Bytes())
	}

	compressed := snappyEncode(wr.Bytes())

	req, err := http.NewRequest(http.MethodPost, p.cfg.URL, bytes.NewReader(compressed))
	if err != nil {
		return fmt.Errorf("prometheus: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	req.Header.Set("User-Agent", "rhelmon/1.0")

	if p.cfg.BasicAuthUser != "" {
		req.SetBasicAuth(p.cfg.BasicAuthUser, p.cfg.BasicAuthPassword)
	} else if p.cfg.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.BearerToken)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("prometheus: post: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("prometheus: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// prometheusMetricName converts "cpu.cpu.total" → "rhelmon_cpu_cpu_total"
func prometheusMetricName(metric string) string {
	return "rhelmon_" + strings.ReplaceAll(metric, ".", "_")
}

// ── Minimal protobuf encoder ──────────────────────────────────────────────────

func protoWriteTag(b *bytes.Buffer, field, wireType int) {
	b.WriteByte(byte((field << 3) | wireType))
}

func protoWriteVarint(b *bytes.Buffer, v uint64) {
	for v >= 0x80 {
		b.WriteByte(byte(v&0x7f) | 0x80)
		v >>= 7
	}
	b.WriteByte(byte(v))
}

func protoWriteString(b *bytes.Buffer, field int, s string) {
	protoWriteTag(b, field, 2) // wire type 2 = length-delimited
	protoWriteVarint(b, uint64(len(s)))
	b.WriteString(s)
}

func protoWriteBytes(b *bytes.Buffer, field int, data []byte) {
	protoWriteTag(b, field, 2)
	protoWriteVarint(b, uint64(len(data)))
	b.Write(data)
}

func protoWriteDouble(b *bytes.Buffer, field int, v float64) {
	protoWriteTag(b, field, 1) // wire type 1 = 64-bit
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], math.Float64bits(v))
	b.Write(buf[:])
}

func protoWriteInt64(b *bytes.Buffer, field int, v int64) {
	protoWriteTag(b, field, 0) // wire type 0 = varint
	// zigzag encode for signed int64
	uv := uint64(v)
	protoWriteVarint(b, uv)
}

// ── Minimal Snappy framing encoder ───────────────────────────────────────────
//
// Prometheus remote write requires snappy block format (not framing format).
// We implement just enough to compress the protobuf payload.

func snappyEncode(src []byte) []byte {
	// snappy block format:
	// varint(uncompressed length) followed by a sequence of elements.
	// For simplicity we emit a single literal element covering the whole input.
	// This is valid snappy — decoders handle it correctly.
	// A real snappy implementation would compress; this is "store" mode.
	// Production note: replace with github.com/golang/snappy for actual compression.
	var buf bytes.Buffer
	// Write uncompressed length as varint
	writeSnappyVarint(&buf, uint64(len(src)))
	// Emit as literal runs of max 60 bytes each
	for len(src) > 0 {
		chunk := src
		if len(chunk) > 60 {
			chunk = chunk[:60]
		}
		src = src[len(chunk):]
		n := len(chunk) - 1
		if n < 60 {
			// tag byte for literal: bottom 2 bits = 0 (literal), top 6 bits = length-1
			buf.WriteByte(byte(n << 2))
		} else {
			buf.WriteByte(0xf8) // 60-byte literal tag
		}
		buf.Write(chunk)
	}
	return buf.Bytes()
}

func writeSnappyVarint(b *bytes.Buffer, v uint64) {
	for v >= 0x80 {
		b.WriteByte(byte(v&0x7f) | 0x80)
		v >>= 7
	}
	b.WriteByte(byte(v))
}

// ── InfluxDB line protocol writer ─────────────────────────────────────────────
//
// Supports InfluxDB v2 (/api/v2/write with token auth)
// and InfluxDB v1 (/write with basic auth or no auth).
//
// Line protocol format:
//   measurement,tag1=val1,tag2=val2 field=value timestamp_ns

// InfluxConfig holds configuration for the InfluxDB backend.
type InfluxConfig struct {
	// URL is the InfluxDB base URL, e.g. "http://localhost:8086"
	URL string

	// Token is the InfluxDB v2 API token (for v2 auth).
	Token string

	// Org is the InfluxDB v2 organisation name.
	Org string

	// Bucket is the InfluxDB v2 bucket (or v1 database) to write to.
	Bucket string

	// V1Database / V1Username / V1Password for InfluxDB v1 compatibility.
	V1Database string
	V1Username string
	V1Password string

	// Measurement is the InfluxDB measurement name. Defaults to "rhelmon".
	Measurement string

	// Precision is the timestamp precision ("ns", "us", "ms", "s"). Defaults to "ms".
	Precision string

	// Timeout defaults to 15s.
	Timeout time.Duration

	// ExtraTags are added to every line, e.g. {"env": "prod"}.
	ExtraTags map[string]string
}

// InfluxWriter sends metrics to InfluxDB using the line protocol.
type InfluxWriter struct {
	cfg    InfluxConfig
	client *http.Client
}

// NewInfluxWriter creates an InfluxWriter. Returns nil if URL is empty.
func NewInfluxWriter(cfg InfluxConfig) *InfluxWriter {
	if cfg.URL == "" {
		return nil
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	if cfg.Measurement == "" {
		cfg.Measurement = "rhelmon"
	}
	if cfg.Precision == "" {
		cfg.Precision = "ms"
	}
	return &InfluxWriter{cfg: cfg, client: &http.Client{Timeout: timeout}}
}

func (i *InfluxWriter) Name() string { return "influxdb" }

func (i *InfluxWriter) Write(points []Point) error {
	var sb strings.Builder
	measurement := escapeMeasurement(i.cfg.Measurement)

	for _, pt := range points {
		// Build tag set: host + metric name (as tag) + extra tags
		tags := make([][2]string, 0, 3+len(i.cfg.ExtraTags))
		tags = append(tags, [2]string{"host", pt.Tags["host"]})
		tags = append(tags, [2]string{"metric", escapeTagValue(pt.Metric)})
		for k, v := range i.cfg.ExtraTags {
			tags = append(tags, [2]string{k, escapeTagValue(v)})
		}
		sort.Slice(tags, func(a, b int) bool { return tags[a][0] < tags[b][0] })

		// measurement,tags field=value timestamp
		sb.WriteString(measurement)
		for _, t := range tags {
			sb.WriteByte(',')
			sb.WriteString(escapeTagKey(t[0]))
			sb.WriteByte('=')
			sb.WriteString(t[1])
		}
		sb.WriteString(" value=")
		sb.WriteString(formatFloat(pt.Value))
		sb.WriteByte(' ')
		// InfluxDB expects nanoseconds by default; we use ms precision
		sb.WriteString(fmt.Sprintf("%d", pt.TS))
		sb.WriteByte('\n')
	}

	body := sb.String()
	writeURL := i.buildWriteURL()

	req, err := http.NewRequest(http.MethodPost, writeURL, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("influxdb: new request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("User-Agent", "rhelmon/1.0")

	if i.cfg.Token != "" {
		req.Header.Set("Authorization", "Token "+i.cfg.Token)
	} else if i.cfg.V1Username != "" {
		req.SetBasicAuth(i.cfg.V1Username, i.cfg.V1Password)
	}

	resp, err := i.client.Do(req)
	if err != nil {
		return fmt.Errorf("influxdb: post: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	// InfluxDB v2 returns 204 on success; v1 returns 204 or 200.
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("influxdb: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

func (i *InfluxWriter) buildWriteURL() string {
	base := strings.TrimRight(i.cfg.URL, "/")
	if i.cfg.V1Database != "" {
		// InfluxDB v1
		return fmt.Sprintf("%s/write?db=%s&precision=%s", base, i.cfg.V1Database, i.cfg.Precision)
	}
	// InfluxDB v2
	return fmt.Sprintf("%s/api/v2/write?org=%s&bucket=%s&precision=%s",
		base, i.cfg.Org, i.cfg.Bucket, i.cfg.Precision)
}

// ── Line protocol escape helpers ──────────────────────────────────────────────

func escapeMeasurement(s string) string {
	s = strings.ReplaceAll(s, ",", `\,`)
	s = strings.ReplaceAll(s, " ", `\ `)
	return s
}

func escapeTagKey(s string) string {
	s = strings.ReplaceAll(s, ",", `\,`)
	s = strings.ReplaceAll(s, "=", `\=`)
	s = strings.ReplaceAll(s, " ", `\ `)
	return s
}

func escapeTagValue(s string) string {
	return escapeTagKey(s)
}

func formatFloat(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "0"
	}
	return fmt.Sprintf("%g", v)
}

// ── Multi-writer ──────────────────────────────────────────────────────────────

// MultiWriter fans out to multiple Writer backends.
type MultiWriter struct {
	writers []Writer
}

// NewMultiWriter wraps multiple writers. Nil entries are skipped.
func NewMultiWriter(ws ...Writer) *MultiWriter {
	m := &MultiWriter{}
	for _, w := range ws {
		if w != nil {
			m.writers = append(m.writers, w)
		}
	}
	return m
}

func (m *MultiWriter) Name() string { return "multi" }

func (m *MultiWriter) Write(points []Point) error {
	var errs []string
	for _, w := range m.writers {
		if err := w.Write(points); err != nil {
			errs = append(errs, fmt.Sprintf("[%s] %v", w.Name(), err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// Len returns the number of registered backends.
func (m *MultiWriter) Len() int { return len(m.writers) }
