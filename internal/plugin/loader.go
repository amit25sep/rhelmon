// Package plugin implements the external plugin loader.
//
// Any executable file placed in PluginDir is run on every tick.
// Each plugin writes metric lines to stdout in one of these formats:
//
//	key value
//	key value tag1=val1 tag2=val2
//
// Examples:
//
//	connections 42
//	active_connections 18 port=80
//	query_time_ms 3.7 db=myapp
//
// Metrics are pushed into the ring buffer under the namespace:
//
//	plugin.<plugin-name>.<key>
//
// where <plugin-name> is the executable filename without extension.
//
// Rules:
//   - Lines starting with # are comments and are ignored.
//   - Empty lines are ignored.
//   - key must match [a-zA-Z0-9_.-]
//   - value must be a valid float64.
//   - Tags are optional key=value pairs after the value; they are recorded
//     in the metric name when they make the series unique, otherwise ignored.
//   - If a plugin takes longer than Timeout to run, it is killed and its
//     output for that tick is discarded.
//   - If a plugin exits non-zero, its output is still parsed but the error
//     is logged.
package plugin

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rhelmon/agent/internal/ringbuf"
)

const (
	// DefaultPluginDir is scanned for executable plugins.
	DefaultPluginDir = "/etc/rhelmon/plugins"

	// DefaultTimeout is the maximum time a single plugin may run per tick.
	DefaultTimeout = 10 * time.Second

	// DefaultInterval is how often each plugin is executed.
	DefaultInterval = 30 * time.Second
)

// Loader discovers and runs external plugins.
type Loader struct {
	store     *ringbuf.Store
	pluginDir string
	interval  time.Duration
	timeout   time.Duration
	stopCh    chan struct{}
	wg        sync.WaitGroup

	mu      sync.RWMutex
	plugins []*pluginEntry
}

type pluginEntry struct {
	path string // full path to executable
	name string // basename without extension — used as metric namespace
}

// New creates a Loader. Call Start to begin executing plugins.
func New(store *ringbuf.Store, pluginDir string, interval, timeout time.Duration) *Loader {
	if pluginDir == "" {
		pluginDir = DefaultPluginDir
	}
	if interval <= 0 {
		interval = DefaultInterval
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Loader{
		store:     store,
		pluginDir: pluginDir,
		interval:  interval,
		timeout:   timeout,
		stopCh:    make(chan struct{}),
	}
}

// Start launches the plugin execution loop.
func (l *Loader) Start() {
	l.discover()
	if len(l.plugins) == 0 {
		log.Printf("plugin: no plugins found in %s (create executable files there to add metrics)", l.pluginDir)
		return
	}
	log.Printf("plugin: found %d plugin(s) in %s", len(l.plugins), l.pluginDir)
	for _, p := range l.plugins {
		log.Printf("plugin:   + %s", p.name)
	}

	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		// run immediately on start, then on each tick
		l.runAll()
		ticker := time.NewTicker(l.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				l.discover() // pick up newly added plugins
				l.runAll()
			case <-l.stopCh:
				return
			}
		}
	}()
}

// Stop shuts down the plugin loop.
func (l *Loader) Stop() {
	close(l.stopCh)
	l.wg.Wait()
}

// PluginNames returns the names of all currently loaded plugins.
func (l *Loader) PluginNames() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	names := make([]string, len(l.plugins))
	for i, p := range l.plugins {
		names[i] = p.name
	}
	return names
}

// discover scans pluginDir for executable files and updates l.plugins.
func (l *Loader) discover() {
	entries, err := os.ReadDir(l.pluginDir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("plugin: cannot read plugin dir %s: %v", l.pluginDir, err)
		}
		return
	}

	var found []*pluginEntry
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(l.pluginDir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		// must be executable by owner
		if info.Mode()&0100 == 0 {
			continue
		}
		name := pluginName(e.Name())
		found = append(found, &pluginEntry{path: path, name: name})
	}

	l.mu.Lock()
	l.plugins = found
	l.mu.Unlock()
}

// runAll executes every registered plugin concurrently.
func (l *Loader) runAll() {
	l.mu.RLock()
	plugins := make([]*pluginEntry, len(l.plugins))
	copy(plugins, l.plugins)
	l.mu.RUnlock()

	var wg sync.WaitGroup
	for _, p := range plugins {
		wg.Add(1)
		go func(pe *pluginEntry) {
			defer wg.Done()
			l.runOne(pe)
		}(p)
	}
	wg.Wait()
}

// runOne executes a single plugin and parses its output.
func (l *Loader) runOne(pe *pluginEntry) {
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, pe.path)
	cmd.Env = append(os.Environ(),
		"RHELMON_PLUGIN=1",
		fmt.Sprintf("RHELMON_INTERVAL=%s", l.interval),
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	startTime := time.Now()
	err := cmd.Run()
	elapsed := time.Since(startTime)

	if ctx.Err() == context.DeadlineExceeded {
		log.Printf("plugin: [%s] timed out after %s", pe.name, l.timeout)
		return
	}
	if err != nil {
		log.Printf("plugin: [%s] exited with error (%v) in %s — parsing output anyway", pe.name, err, elapsed.Round(time.Millisecond))
	}
	if stderr.Len() > 0 {
		log.Printf("plugin: [%s] stderr: %s", pe.name, strings.TrimSpace(stderr.String()))
	}

	now := time.Now().Unix()
	count := 0
	sc := bufio.NewScanner(&stdout)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, err := parseLine(line)
		if err != nil {
			log.Printf("plugin: [%s] parse error on %q: %v", pe.name, line, err)
			continue
		}
		metricKey := "plugin." + pe.name + "." + key
		l.store.Push(metricKey, ringbuf.Sample{TS: now, Value: value})
		count++
	}

	log.Printf("plugin: [%s] ran in %s, pushed %d metrics", pe.name, elapsed.Round(time.Millisecond), count)
}

// parseLine parses one output line from a plugin.
// Format: "key value [tag=val ...]"
// Tags are currently ignored (reserved for future label support).
func parseLine(line string) (key string, value float64, err error) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", 0, fmt.Errorf("need at least 2 fields, got %d", len(fields))
	}
	key = fields[0]
	if !isValidKey(key) {
		return "", 0, fmt.Errorf("invalid key %q (use [a-zA-Z0-9_.-])", key)
	}
	value, err = strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return "", 0, fmt.Errorf("invalid value %q: %w", fields[1], err)
	}
	return key, value, nil
}

func isValidKey(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' && c != '.' && c != '-' {
			return false
		}
	}
	return true
}

// pluginName strips common extensions and returns a safe metric namespace name.
func pluginName(filename string) string {
	name := filename
	for _, ext := range []string{".sh", ".py", ".rb", ".pl", ".js", ".lua", ".exe", ".bin"} {
		name = strings.TrimSuffix(name, ext)
	}
	// replace anything not alphanumeric or underscore with underscore
	var sb strings.Builder
	for _, c := range name {
		if unicode.IsLetter(c) || unicode.IsDigit(c) || c == '_' {
			sb.WriteRune(c)
		} else {
			sb.WriteRune('_')
		}
	}
	return sb.String()
}
