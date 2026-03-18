// Package tsdb handles writing metric samples from the ring buffer to
// external time-series databases on a configurable flush interval.
//
// The Writer interface is intentionally minimal — implement it to add any
// new backend (Graphite, OpenTSDB, DataDog, etc.) without touching the core.
package tsdb

import (
	"log"
	"sort"
	"sync"
	"time"

	"github.com/rhelmon/agent/internal/ringbuf"
)

// Point is a single metric data point ready to be written to a backend.
type Point struct {
	// Metric is the full dotted metric name, e.g. "cpu.cpu.total"
	Metric string
	// Tags are key/value labels attached to the point.
	// "host" is always populated by the Manager.
	Tags map[string]string
	// Value is the sample value.
	Value float64
	// TS is the Unix timestamp in milliseconds.
	TS int64
}

// Writer is the interface every TSDB backend must implement.
// Write receives a batch of points and should return an error if the
// entire batch could not be delivered — the Manager will log and retry
// on the next flush interval.
type Writer interface {
	Write(points []Point) error
	// Name returns a short identifier used in logs ("prometheus", "influxdb").
	Name() string
}

// Manager reads from a ring buffer Store on a flush interval, builds
// a batch of Points from the delta since the last flush, and writes
// them to all registered backends.
type Manager struct {
	store    *ringbuf.Store
	writers  []Writer
	host     string
	interval time.Duration
	stopCh   chan struct{}
	wg       sync.WaitGroup

	// cursor tracks the last-seen timestamp per metric to avoid re-sending.
	mu      sync.Mutex
	cursors map[string]int64 // metric → last sent TS (unix seconds)
}

// NewManager creates a Manager. host is added as a "host" tag on every point.
func NewManager(store *ringbuf.Store, host string, interval time.Duration) *Manager {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &Manager{
		store:    store,
		host:     host,
		interval: interval,
		stopCh:   make(chan struct{}),
		cursors:  make(map[string]int64),
	}
}

// AddWriter registers a backend. Call before Start.
func (m *Manager) AddWriter(w Writer) {
	if w != nil {
		m.writers = append(m.writers, w)
	}
}

// Start launches the flush loop.
func (m *Manager) Start() {
	if len(m.writers) == 0 {
		return // nothing to do
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.flush()
			case <-m.stopCh:
				return
			}
		}
	}()
	log.Printf("tsdb: writer started (interval=%s, backends=%d)", m.interval, len(m.writers))
}

// Stop shuts down the flush loop and performs one final flush.
func (m *Manager) Stop() {
	if len(m.writers) == 0 {
		return
	}
	close(m.stopCh)
	m.wg.Wait()
	m.flush() // drain remaining samples
}

// flush collects new samples from the ring buffer and sends them to all writers.
func (m *Manager) flush() {
	names := m.store.Names()
	if len(names) == 0 {
		return
	}
	sort.Strings(names)

	m.mu.Lock()
	var points []Point
	for _, name := range names {
		// Request the last (interval/collectionRate) samples — generous upper bound.
		samples := m.store.Last(name, 60)
		lastSent := m.cursors[name]
		for _, s := range samples {
			if s.TS <= lastSent {
				continue // already sent
			}
			points = append(points, Point{
				Metric: name,
				Tags:   map[string]string{"host": m.host},
				Value:  s.Value,
				TS:     s.TS * 1000, // convert to milliseconds
			})
		}
		// advance cursor to the newest sample we included
		if len(samples) > 0 {
			newest := samples[len(samples)-1].TS
			if newest > lastSent {
				m.cursors[name] = newest
			}
		}
	}
	m.mu.Unlock()

	if len(points) == 0 {
		return
	}

	for _, w := range m.writers {
		if err := w.Write(points); err != nil {
			log.Printf("tsdb: [%s] write error: %v (%d points dropped)", w.Name(), err, len(points))
		} else {
			log.Printf("tsdb: [%s] flushed %d points", w.Name(), len(points))
		}
	}
}
