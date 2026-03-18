package ringbuf

import (
	"testing"
	"time"
)

func TestRingBuffer_BasicPushLast(t *testing.T) {
	rb := New(5)
	for i := 1; i <= 5; i++ {
		rb.Push(Sample{TS: int64(i), Value: float64(i)})
	}
	got := rb.Last(5)
	if len(got) != 5 {
		t.Fatalf("expected 5 samples, got %d", len(got))
	}
	for i, s := range got {
		if s.Value != float64(i+1) {
			t.Errorf("sample[%d].Value = %v, want %v", i, s.Value, float64(i+1))
		}
	}
}

func TestRingBuffer_Overwrite(t *testing.T) {
	rb := New(3)
	for i := 1; i <= 6; i++ {
		rb.Push(Sample{TS: int64(i), Value: float64(i)})
	}
	// After 6 pushes into cap-3: should have [4,5,6]
	got := rb.Last(3)
	if len(got) != 3 {
		t.Fatalf("expected 3 samples, got %d", len(got))
	}
	want := []float64{4, 5, 6}
	for i, s := range got {
		if s.Value != want[i] {
			t.Errorf("sample[%d].Value = %v, want %v", i, s.Value, want[i])
		}
	}
}

func TestRingBuffer_LastN_LessThanSize(t *testing.T) {
	rb := New(10)
	for i := 1; i <= 10; i++ {
		rb.Push(Sample{TS: int64(i), Value: float64(i)})
	}
	got := rb.Last(3)
	if len(got) != 3 {
		t.Fatalf("want 3 samples, got %d", len(got))
	}
	// Should be the last 3: 8, 9, 10
	want := []float64{8, 9, 10}
	for i, s := range got {
		if s.Value != want[i] {
			t.Errorf("sample[%d].Value = %v, want %v", i, s.Value, want[i])
		}
	}
}

func TestRingBuffer_Latest(t *testing.T) {
	rb := New(10)
	_, ok := rb.Latest()
	if ok {
		t.Fatal("empty buffer should return ok=false")
	}
	rb.Push(Sample{TS: 1, Value: 42})
	rb.Push(Sample{TS: 2, Value: 99})
	s, ok := rb.Latest()
	if !ok || s.Value != 99 {
		t.Errorf("Latest() = (%v, %v), want (99, true)", s.Value, ok)
	}
}

func TestRingBuffer_Empty(t *testing.T) {
	rb := New(10)
	got := rb.Last(5)
	if got != nil {
		t.Fatalf("empty buffer should return nil, got %v", got)
	}
}

func TestStore_BasicOperations(t *testing.T) {
	s := NewStore(100)
	now := time.Now().Unix()

	s.Push("cpu.total", Sample{TS: now, Value: 55.5})
	s.Push("cpu.total", Sample{TS: now + 1, Value: 60.0})

	latest, ok := s.Latest("cpu.total")
	if !ok || latest.Value != 60.0 {
		t.Errorf("Latest = (%v, %v), want (60.0, true)", latest.Value, ok)
	}

	samples := s.Last("cpu.total", 10)
	if len(samples) != 2 {
		t.Errorf("Last(10) = %d samples, want 2", len(samples))
	}

	// Non-existent key
	_, ok = s.Latest("nonexistent")
	if ok {
		t.Error("nonexistent key should return ok=false")
	}
}

func TestStore_Names(t *testing.T) {
	s := NewStore(10)
	s.Push("a", Sample{TS: 1, Value: 1})
	s.Push("b", Sample{TS: 1, Value: 2})
	s.Push("c", Sample{TS: 1, Value: 3})

	names := s.Names()
	if len(names) != 3 {
		t.Errorf("Names() = %v, want 3 entries", names)
	}
}

func BenchmarkRingBuffer_Push(b *testing.B) {
	rb := New(DefaultCap)
	s := Sample{TS: 1, Value: 42.0}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rb.Push(s)
	}
}

func BenchmarkRingBuffer_Last(b *testing.B) {
	rb := New(DefaultCap)
	for i := 0; i < DefaultCap; i++ {
		rb.Push(Sample{TS: int64(i), Value: float64(i)})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = rb.Last(300)
	}
}
