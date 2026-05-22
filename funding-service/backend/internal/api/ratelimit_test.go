package api

import (
	"testing"
	"time"
)

func TestIPLimiter_Allow(t *testing.T) {
	l := newIPLimiter()
	for i := 1; i <= 5; i++ {
		if !l.allow("1.2.3.4", 5, time.Minute) {
			t.Fatalf("expected allow on request %d", i)
		}
	}
	if l.allow("1.2.3.4", 5, time.Minute) {
		t.Fatal("expected deny on 6th request")
	}
	if !l.allow("5.6.7.8", 5, time.Minute) {
		t.Fatal("expected allow for different IP")
	}
}
