package funding_test

import (
	"testing"
	"time"

	"github.com/funding-service/backend/internal/funding"
)

var (
	t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
)

func sec(n int) time.Time { return t0.Add(time.Duration(n) * time.Second) }

func TestVWAP_EmptyWindowReturnsFalse(t *testing.T) {
	c := funding.NewVWAP(time.Minute)
	_, ok := c.Value(t0)
	if ok {
		t.Error("expected false for empty window")
	}
}

func TestVWAP_SingleEntry(t *testing.T) {
	c := funding.NewVWAP(time.Minute)
	c.Add(80.0, 100, sec(0))
	v, ok := c.Value(sec(0))
	if !ok {
		t.Fatal("expected value, got false")
	}
	if v != 80.0 {
		t.Errorf("expected 80.0, got %v", v)
	}
}

func TestVWAP_WeightedAverage(t *testing.T) {
	// price=100 vol=1, price=200 vol=3  →  VWAP = (100+600)/4 = 175
	c := funding.NewVWAP(time.Minute)
	c.Add(100, 1, sec(0))
	c.Add(200, 3, sec(1))
	v, ok := c.Value(sec(2))
	if !ok {
		t.Fatal("expected value")
	}
	const want = 175.0
	if v != want {
		t.Errorf("want %.4f, got %.4f", want, v)
	}
}

func TestVWAP_EqualVolumes(t *testing.T) {
	// Equal volumes → VWAP = arithmetic mean
	c := funding.NewVWAP(time.Minute)
	c.Add(80, 10, sec(0))
	c.Add(90, 10, sec(1))
	c.Add(100, 10, sec(2))
	v, ok := c.Value(sec(3))
	if !ok {
		t.Fatal("expected value")
	}
	const want = 90.0
	if v != want {
		t.Errorf("want %.4f, got %.4f", want, v)
	}
}

func TestVWAP_EntriesExpireFromWindow(t *testing.T) {
	// window = 10s; add at t=0 and t=20, query at t=30
	// entry at t=0 is 30s old → outside window; only t=20 survives
	c := funding.NewVWAP(10 * time.Second)
	c.Add(80.0, 100, sec(0))  // will expire
	c.Add(90.0, 100, sec(20)) // within window at t=30
	v, ok := c.Value(sec(30))
	if !ok {
		t.Fatal("expected value after partial expiry")
	}
	if v != 90.0 {
		t.Errorf("want 90.0 (only unexpired entry), got %v", v)
	}
}

func TestVWAP_AllEntriesExpireReturnsFalse(t *testing.T) {
	c := funding.NewVWAP(10 * time.Second)
	c.Add(80.0, 100, sec(0))
	// query 60s later — all entries expired
	_, ok := c.Value(sec(60))
	if ok {
		t.Error("expected false after all entries expired")
	}
}

func TestVWAP_ExactWindowBoundaryIsIncluded(t *testing.T) {
	// window = 10s; at Value(t=10), cutoff = t=0.
	// expire condition is ts.Before(cutoff) → ts < 0 → false for ts=0.
	// So the entry at ts=0 stays (inclusive left boundary [now-window, now]).
	c := funding.NewVWAP(10 * time.Second)
	c.Add(80.0, 100, sec(0))
	v, ok := c.Value(sec(10))
	if !ok {
		t.Fatal("expected entry at exact boundary to remain in window")
	}
	if v != 80.0 {
		t.Errorf("want 80.0, got %v", v)
	}
}

func TestVWAP_EntryJustOutsideWindowExpires(t *testing.T) {
	// entry at sec(0), query at sec(11), window=10s → cutoff=sec(1) → ts=0 < 1 → expired
	c := funding.NewVWAP(10 * time.Second)
	c.Add(80.0, 100, sec(0))
	_, ok := c.Value(sec(11))
	if ok {
		t.Error("entry one second outside window should be expired")
	}
}

func TestVWAP_ZeroVolumeEntryIgnored(t *testing.T) {
	c := funding.NewVWAP(time.Minute)
	c.Add(80.0, 0, sec(0)) // zero volume: contributes nothing to sumV
	_, ok := c.Value(sec(1))
	if ok {
		t.Error("zero-volume entry alone should return false (sumV==0)")
	}
}

func TestVWAP_MixedZeroAndNonZeroVolume(t *testing.T) {
	c := funding.NewVWAP(time.Minute)
	c.Add(80.0, 0, sec(0))   // ignored
	c.Add(100.0, 10, sec(1)) // effective
	v, ok := c.Value(sec(2))
	if !ok {
		t.Fatal("expected value")
	}
	if v != 100.0 {
		t.Errorf("want 100.0, got %v", v)
	}
}

func TestVWAP_PartialWindowManyEntries(t *testing.T) {
	c := funding.NewVWAP(30 * time.Second)
	// Add 60 entries at 1s intervals; at query time t=60, only t=30..59 are in window
	for i := 0; i < 60; i++ {
		c.Add(float64(i), 1.0, sec(i))
	}
	v, ok := c.Value(sec(60))
	if !ok {
		t.Fatal("expected value")
	}
	// entries 30..59 (30 entries): mean of [30,31,...,59] = (30+59)/2 = 44.5
	const want = 44.5
	if v != want {
		t.Errorf("want %.4f, got %.4f", want, v)
	}
}

func TestVWAP_Reset(t *testing.T) {
	c := funding.NewVWAP(time.Minute)
	c.Add(80.0, 100, sec(0))
	c.Reset()
	_, ok := c.Value(sec(1))
	if ok {
		t.Error("expected false after Reset")
	}
}

func TestVWAP_CompactionDoesNotLoseData(t *testing.T) {
	// Add 200 entries, then expire the first 100. After compaction, VWAP should
	// reflect only entries 100..199.
	c := funding.NewVWAP(100 * time.Second)
	for i := 0; i < 200; i++ {
		c.Add(float64(i), 1.0, sec(i))
	}
	// At sec(200), entries 0..99 are expired (ts < sec(100))
	v, ok := c.Value(sec(200))
	if !ok {
		t.Fatal("expected value")
	}
	// entries 100..199: mean = (100+199)/2 = 149.5
	const want = 149.5
	if v != want {
		t.Errorf("want %.4f, got %.4f", want, v)
	}
}

// --- benchmark ---

func BenchmarkVWAP_AddValue(b *testing.B) {
	c := funding.NewVWAP(time.Minute)
	now := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ts := now.Add(time.Duration(i) * time.Millisecond)
		c.Add(80.0+float64(i)*0.001, 100, ts)
		c.Value(ts)
	}
}

func BenchmarkVWAP_ValueOnly(b *testing.B) {
	c := funding.NewVWAP(time.Minute)
	now := time.Now()
	for i := 0; i < 500; i++ {
		c.Add(80.0, 100, now.Add(time.Duration(i)*time.Millisecond))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Value(now.Add(time.Duration(b.N+i) * time.Millisecond))
	}
}
