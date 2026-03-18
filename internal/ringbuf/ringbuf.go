// Package ringbuf provides a fixed-size circular buffer for metric time-series.
// Each metric gets its own RingBuffer holding up to Cap samples.
// Writes never block; the oldest sample is overwritten when full.
package ringbuf

import "sync"

const DefaultCap = 3600 // 1 hour at 1-second resolution

// Sample is a single timestamped data point.
type Sample struct {
	TS    int64   // Unix timestamp (seconds)
	Value float64
}

// RingBuffer is a thread-safe circular buffer of Samples.
type RingBuffer struct {
	mu   sync.RWMutex
	data []Sample
	head int // next write position
	size int // number of valid entries
	cap  int
}

// New creates a RingBuffer with the given capacity.
func New(cap int) *RingBuffer {
	if cap <= 0 {
		cap = DefaultCap
	}
	return &RingBuffer{data: make([]Sample, cap), cap: cap}
}

// Push appends a sample, overwriting the oldest if full.
func (r *RingBuffer) Push(s Sample) {
	r.mu.Lock()
	r.data[r.head] = s
	r.head = (r.head + 1) % r.cap
	if r.size < r.cap {
		r.size++
	}
	r.mu.Unlock()
}

// Last returns the most recent n samples in chronological order.
// If n <= 0 or n > size, all available samples are returned.
func (r *RingBuffer) Last(n int) []Sample {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.size == 0 {
		return nil
	}
	count := n
	if count <= 0 || count > r.size {
		count = r.size
	}
	out := make([]Sample, count)
	// tail is the index of the oldest sample we want
	tail := (r.head - count + r.cap*2) % r.cap
	for i := 0; i < count; i++ {
		out[i] = r.data[(tail+i)%r.cap]
	}
	return out
}

// Latest returns the single most recent sample, or zero if empty.
func (r *RingBuffer) Latest() (Sample, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.size == 0 {
		return Sample{}, false
	}
	idx := (r.head - 1 + r.cap) % r.cap
	return r.data[idx], true
}

// Len returns the number of valid samples stored.
func (r *RingBuffer) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.size
}

// Store holds named RingBuffers, one per metric series.
type Store struct {
	mu      sync.RWMutex
	buffers map[string]*RingBuffer
	bufCap  int
}

// NewStore creates a Store with buffers of given capacity.
func NewStore(bufCap int) *Store {
	if bufCap <= 0 {
		bufCap = DefaultCap
	}
	return &Store{buffers: make(map[string]*RingBuffer), bufCap: bufCap}
}

// Push writes a sample to the named series, creating it if needed.
func (s *Store) Push(name string, sample Sample) {
	s.mu.RLock()
	buf, ok := s.buffers[name]
	s.mu.RUnlock()
	if !ok {
		s.mu.Lock()
		if buf, ok = s.buffers[name]; !ok {
			buf = New(s.bufCap)
			s.buffers[name] = buf
		}
		s.mu.Unlock()
	}
	buf.Push(sample)
}

// Last returns the last n samples for the named series.
func (s *Store) Last(name string, n int) []Sample {
	s.mu.RLock()
	buf, ok := s.buffers[name]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	return buf.Last(n)
}

// Latest returns the most recent sample for the named series.
func (s *Store) Latest(name string) (Sample, bool) {
	s.mu.RLock()
	buf, ok := s.buffers[name]
	s.mu.RUnlock()
	if !ok {
		return Sample{}, false
	}
	return buf.Latest()
}

// Names returns all metric series names currently tracked.
func (s *Store) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.buffers))
	for k := range s.buffers {
		names = append(names, k)
	}
	return names
}
