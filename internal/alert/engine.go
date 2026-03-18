// Package alert implements a threshold-based alert engine.
//
// Flow:
//
//	OK ──(threshold crossed)──► Pending ──(held for ForDuration)──► Firing
//	       Pending ──(drops below)──► OK
//	       Firing  ──(drops below)──► OK  (notifier sends "resolved")
package alert

import (
	"log"
	"sync"
	"time"

	"github.com/rhelmon/agent/internal/ringbuf"
)

// State represents the lifecycle state of a single alert rule instance.
type State int

const (
	StateOK      State = iota // metric is below threshold
	StatePending              // threshold crossed but ForDuration not yet elapsed
	StateFiring               // threshold held for ForDuration — notifications sent
)

func (s State) String() string {
	switch s {
	case StateOK:
		return "ok"
	case StatePending:
		return "pending"
	case StateFiring:
		return "firing"
	}
	return "unknown"
}

// Op is the comparison operator used in a rule.
type Op int

const (
	OpGT Op = iota // >
	OpGTE          // >=
	OpLT           // <
	OpLTE          // <=
)

func (o Op) eval(value, threshold float64) bool {
	switch o {
	case OpGT:
		return value > threshold
	case OpGTE:
		return value >= threshold
	case OpLT:
		return value < threshold
	case OpLTE:
		return value <= threshold
	}
	return false
}

func (o Op) String() string {
	switch o {
	case OpGT:
		return ">"
	case OpGTE:
		return ">="
	case OpLT:
		return "<"
	case OpLTE:
		return "<="
	}
	return "?"
}

// Rule defines a single alerting condition.
type Rule struct {
	// Name is a human-readable identifier shown in the dashboard and notifications.
	Name string

	// Metric is the ring buffer key to evaluate (e.g. "cpu.cpu.total").
	Metric string

	// Op and Threshold define the condition: fire when metric Op Threshold.
	Op        Op
	Threshold float64

	// ForDuration is how long the condition must hold before transitioning
	// Pending → Firing. Zero means fire immediately.
	ForDuration time.Duration

	// Severity is a free-form label ("critical", "warning", "info").
	Severity string

	// Annotations are extra key/value pairs included in notifications.
	Annotations map[string]string
}

// AlertEvent is emitted by the engine when a rule transitions state.
type AlertEvent struct {
	Rule      *Rule
	State     State     // the new state
	Value     float64   // metric value that triggered the transition
	FiredAt   time.Time // when Firing was first entered (zero if not firing)
	ResolvedAt time.Time // when resolved (zero if not resolved)
}

// ruleState tracks per-rule runtime state inside the engine.
type ruleState struct {
	rule        *Rule
	current     State
	pendingSince time.Time // when we entered Pending
	firedAt     time.Time // when we entered Firing
	lastValue   float64
}

// Engine evaluates a set of Rules against a ring buffer Store on a tick.
type Engine struct {
	store    *ringbuf.Store
	rules    []*Rule
	states   map[string]*ruleState // keyed by rule name
	notifier Notifier
	interval time.Duration

	mu     sync.RWMutex
	stopCh chan struct{}
	wg     sync.WaitGroup

	// eventCh receives every state transition for broadcast to the dashboard.
	eventCh chan AlertEvent
}

// New creates an Engine. Call AddRule to register rules, then Start.
func New(store *ringbuf.Store, notifier Notifier, interval time.Duration) *Engine {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Engine{
		store:    store,
		states:   make(map[string]*ruleState),
		notifier: notifier,
		interval: interval,
		stopCh:   make(chan struct{}),
		eventCh:  make(chan AlertEvent, 64),
	}
}

// AddRule registers a rule. Must be called before Start.
func (e *Engine) AddRule(r *Rule) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules = append(e.rules, r)
	e.states[r.Name] = &ruleState{rule: r, current: StateOK}
}

// Start launches the evaluation loop.
func (e *Engine) Start() {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		ticker := time.NewTicker(e.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				e.evaluate()
			case <-e.stopCh:
				return
			}
		}
	}()
}

// Stop shuts down the engine.
func (e *Engine) Stop() {
	close(e.stopCh)
	e.wg.Wait()
}

// Events returns the channel that receives AlertEvents on every state transition.
func (e *Engine) Events() <-chan AlertEvent {
	return e.eventCh
}

// ActiveAlerts returns a snapshot of all rules currently in Pending or Firing state.
func (e *Engine) ActiveAlerts() []AlertSnapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []AlertSnapshot
	for _, rs := range e.states {
		if rs.current != StateOK {
			out = append(out, toSnapshot(rs))
		}
	}
	return out
}

// AllAlerts returns a snapshot of every rule with its current state.
func (e *Engine) AllAlerts() []AlertSnapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]AlertSnapshot, 0, len(e.states))
	for _, rs := range e.states {
		out = append(out, toSnapshot(rs))
	}
	return out
}

// AlertSnapshot is the JSON-serialisable view of a rule's current state.
type AlertSnapshot struct {
	Name        string            `json:"name"`
	Metric      string            `json:"metric"`
	Op          string            `json:"op"`
	Threshold   float64           `json:"threshold"`
	Severity    string            `json:"severity"`
	State       string            `json:"state"`
	Value       float64           `json:"value"`
	ForDuration string            `json:"for_duration"`
	PendingSince int64            `json:"pending_since,omitempty"` // unix ts
	FiredAt      int64            `json:"fired_at,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

func toSnapshot(rs *ruleState) AlertSnapshot {
	s := AlertSnapshot{
		Name:        rs.rule.Name,
		Metric:      rs.rule.Metric,
		Op:          rs.rule.Op.String(),
		Threshold:   rs.rule.Threshold,
		Severity:    rs.rule.Severity,
		State:       rs.current.String(),
		Value:       rs.lastValue,
		ForDuration: rs.rule.ForDuration.String(),
		Annotations: rs.rule.Annotations,
	}
	if !rs.pendingSince.IsZero() {
		s.PendingSince = rs.pendingSince.Unix()
	}
	if !rs.firedAt.IsZero() {
		s.FiredAt = rs.firedAt.Unix()
	}
	return s
}

// evaluate runs one tick of the state machine for every registered rule.
func (e *Engine) evaluate() {
	now := time.Now()

	e.mu.Lock()
	defer e.mu.Unlock()

	for _, rs := range e.states {
		sample, ok := e.store.Latest(rs.rule.Metric)
		if !ok {
			continue // metric not yet collected
		}
		value := sample.Value
		rs.lastValue = value
		triggered := rs.rule.Op.eval(value, rs.rule.Threshold)

		prev := rs.current

		switch rs.current {
		case StateOK:
			if triggered {
				rs.current = StatePending
				rs.pendingSince = now
				log.Printf("alert: [pending] %s  %s %s %.2f  (value=%.2f)",
					rs.rule.Name, rs.rule.Metric, rs.rule.Op, rs.rule.Threshold, value)
			}

		case StatePending:
			if !triggered {
				// condition dropped — back to OK without notifying
				rs.current = StateOK
				rs.pendingSince = time.Time{}
			} else if rs.rule.ForDuration == 0 || now.Sub(rs.pendingSince) >= rs.rule.ForDuration {
				// held long enough → fire
				rs.current = StateFiring
				rs.firedAt = now
				log.Printf("alert: [firing]  %s  %s %s %.2f  (value=%.2f)",
					rs.rule.Name, rs.rule.Metric, rs.rule.Op, rs.rule.Threshold, value)
				e.notify(AlertEvent{Rule: rs.rule, State: StateFiring, Value: value, FiredAt: rs.firedAt})
			}

		case StateFiring:
			if !triggered {
				rs.current = StateOK
				resolvedAt := now
				rs.pendingSince = time.Time{}
				rs.firedAt = time.Time{}
				log.Printf("alert: [resolved] %s  (value=%.2f)", rs.rule.Name, value)
				e.notify(AlertEvent{Rule: rs.rule, State: StateOK, Value: value, ResolvedAt: resolvedAt})
			}
		}

		// emit event on any transition
		if rs.current != prev {
			select {
			case e.eventCh <- AlertEvent{Rule: rs.rule, State: rs.current, Value: value, FiredAt: rs.firedAt}:
			default:
			}
		}
	}
}

func (e *Engine) notify(ev AlertEvent) {
	if e.notifier == nil {
		return
	}
	go func() {
		if err := e.notifier.Notify(ev); err != nil {
			log.Printf("alert: notifier error: %v", err)
		}
	}()
}

// DefaultRules returns a sensible set of rules for a typical RHEL server.
// Callers should add these to the engine at startup, or replace with their own.
func DefaultRules() []*Rule {
	return []*Rule{
		{
			Name:        "cpu-high",
			Metric:      "cpu.cpu.total",
			Op:          OpGT,
			Threshold:   85,
			ForDuration: 30 * time.Second,
			Severity:    "warning",
			Annotations: map[string]string{"summary": "CPU utilization above 85% for 30s"},
		},
		{
			Name:        "cpu-critical",
			Metric:      "cpu.cpu.total",
			Op:          OpGT,
			Threshold:   95,
			ForDuration: 15 * time.Second,
			Severity:    "critical",
			Annotations: map[string]string{"summary": "CPU utilization above 95% for 15s"},
		},
		{
			Name:        "memory-high",
			Metric:      "mem.used_pct",
			Op:          OpGT,
			Threshold:   85,
			ForDuration: 60 * time.Second,
			Severity:    "warning",
			Annotations: map[string]string{"summary": "Memory usage above 85% for 60s"},
		},
		{
			Name:        "memory-critical",
			Metric:      "mem.used_pct",
			Op:          OpGT,
			Threshold:   95,
			ForDuration: 30 * time.Second,
			Severity:    "critical",
			Annotations: map[string]string{"summary": "Memory usage above 95% for 30s"},
		},
		{
			Name:        "iowait-high",
			Metric:      "cpu.cpu.iowait",
			Op:          OpGT,
			Threshold:   20,
			ForDuration: 30 * time.Second,
			Severity:    "warning",
			Annotations: map[string]string{"summary": "I/O wait above 20% for 30s — possible disk bottleneck"},
		},
		{
			Name:        "load-high",
			Metric:      "load.1m",
			Op:          OpGT,
			Threshold:   8,
			ForDuration: 60 * time.Second,
			Severity:    "warning",
			Annotations: map[string]string{"summary": "1-minute load average above 8 for 60s"},
		},
		{
			Name:        "swap-in-use",
			Metric:      "mem.swap_pct",
			Op:          OpGT,
			Threshold:   50,
			ForDuration: 120 * time.Second,
			Severity:    "warning",
			Annotations: map[string]string{"summary": "Swap usage above 50% for 2 minutes"},
		},
	}
}
