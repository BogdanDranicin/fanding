package funding

import (
	"sync"
	"time"
)

type entry struct {
	price     float64
	volume    float64
	timestamp time.Time
}

// VWAPCalculator computes Volume-Weighted Average Price over a sliding time window.
//
// It maintains a running Σ(price×volume) and Σ(volume) so that Value() is O(1)
// when no entries expire, and O(k) amortised when k entries leave the window.
// Safe for concurrent use.
type VWAPCalculator struct {
	window  time.Duration
	entries []entry
	head    int
	sumPV   float64 // Σ(price × volume) for entries in window
	sumV    float64 // Σ(volume) for entries in window
	mu      sync.Mutex
}

// NewVWAP creates a VWAPCalculator with the given sliding window duration.
func NewVWAP(window time.Duration) *VWAPCalculator {
	return &VWAPCalculator{
		window:  window,
		entries: make([]entry, 0, 64),
	}
}

// Add records a price/volume observation at time ts.
func (c *VWAPCalculator) Add(price, volume float64, ts time.Time) {
	c.mu.Lock()
	c.entries = append(c.entries, entry{price, volume, ts})
	c.sumPV += price * volume
	c.sumV += volume
	c.mu.Unlock()
}

// Value returns VWAP over [now-window, now].
// Returns (0, false) when the window contains no data or all volumes are zero.
func (c *VWAPCalculator) Value(now time.Time) (float64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire(now)
	if c.sumV <= 0 {
		return 0, false
	}
	return c.sumPV / c.sumV, true
}

// expire removes entries with timestamp < now-window from the running sums.
func (c *VWAPCalculator) expire(now time.Time) {
	cutoff := now.Add(-c.window)
	for c.head < len(c.entries) && c.entries[c.head].timestamp.Before(cutoff) {
		e := c.entries[c.head]
		c.sumPV -= e.price * e.volume
		c.sumV -= e.volume
		c.head++
	}
	// Compact backing slice when the dead prefix reaches half its length,
	// to prevent unbounded memory growth without copying on every expiry.
	if c.head > 0 && c.head >= len(c.entries)/2 {
		active := c.entries[c.head:]
		n := copy(c.entries, active)
		// Zero out tail to allow GC of any time.Time internals.
		for i := n; i < len(c.entries); i++ {
			c.entries[i] = entry{}
		}
		c.entries = c.entries[:n]
		c.head = 0
	}
}

// Reset clears all entries and resets running sums.
func (c *VWAPCalculator) Reset() {
	c.mu.Lock()
	c.entries = c.entries[:0]
	c.head = 0
	c.sumPV = 0
	c.sumV = 0
	c.mu.Unlock()
}
