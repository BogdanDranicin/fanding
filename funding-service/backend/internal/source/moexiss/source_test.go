package moexiss_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/source"
	"github.com/funding-service/backend/internal/source/moexiss"
)

func issResponse(symbol string, last, bid, offer float64) string {
	return fmt.Sprintf(`{
  "marketdata": {
    "columns": ["SECID","LAST","BID","OFFER","VOLTODAY","SYSTIME"],
    "data":    [[%q, %v, %v, %v, 1000, "2024-01-15 10:30:00"]]
  }
}`, symbol, last, bid, offer)
}

func newTestSource(baseURL string) *moexiss.Source {
	return moexiss.NewWithClient(
		moexiss.NewClientWithBaseURL(baseURL),
		20*time.Millisecond,
		zerolog.Nop(),
	)
}

func TestSource_Name(t *testing.T) {
	s := moexiss.New(time.Second, zerolog.Nop())
	if s.Name() != "moex-iss" {
		t.Errorf("expected moex-iss, got %s", s.Name())
	}
}

func TestSource_UnknownSymbol(t *testing.T) {
	s := moexiss.New(time.Second, zerolog.Nop())
	_, err := s.Subscribe(context.Background(), []string{"UNKNOWN"})
	if err == nil {
		t.Fatal("expected error for unknown symbol")
	}
}

func TestSource_TicksDelivered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, issResponse("USDRUBF", 81.91, 81.90, 81.95))
	}))
	defer srv.Close()

	s := newTestSource(srv.URL)
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	ch, err := s.Subscribe(ctx, []string{source.SymbolUSDRUBF})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	var ticks []source.Tick
	for tick := range ch {
		ticks = append(ticks, tick)
		if len(ticks) >= 3 {
			break
		}
	}

	if len(ticks) < 3 {
		t.Fatalf("expected at least 3 ticks (LAST+BID+OFFER), got %d", len(ticks))
	}
	for _, tk := range ticks {
		if tk.Symbol != source.SymbolUSDRUBF {
			t.Errorf("unexpected symbol %s", tk.Symbol)
		}
		if tk.Source != "moex-iss" {
			t.Errorf("unexpected source %s", tk.Source)
		}
		if tk.Price == 0 {
			t.Error("price must not be zero")
		}
	}
}

func TestSource_SpotTOMUsesSecidAndInternalSymbol(t *testing.T) {
	// The spot TOM instrument is requested from MOEX by its SECID (USD000UTSTOM) on the
	// currency/selt/CETS board, but ticks must carry the internal symbol (USDRUB_TOM).
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, issResponse("USD000UTSTOM", 71.37, 71.36, 71.38))
	}))
	defer srv.Close()

	s := newTestSource(srv.URL)
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	ch, err := s.Subscribe(ctx, []string{source.SymbolUSDRubTOM})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	var tick source.Tick
	for tk := range ch {
		tick = tk
		break
	}

	if tick.Symbol != source.SymbolUSDRubTOM {
		t.Errorf("tick symbol: want %q, got %q", source.SymbolUSDRubTOM, tick.Symbol)
	}
	wantSegment := "/engines/currency/markets/selt/boards/CETS/securities/USD000UTSTOM.json"
	if !strings.HasSuffix(gotPath, wantSegment) {
		t.Errorf("request path: want suffix %q, got %q", wantSegment, gotPath)
	}
}

func TestSource_Deduplication(t *testing.T) {
	// Server always returns the same prices — after the first poll,
	// no further ticks should be emitted.
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, issResponse("USDRUBF", 81.91, 81.90, 81.95))
	}))
	defer srv.Close()

	s := newTestSource(srv.URL)
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	ch, err := s.Subscribe(ctx, []string{source.SymbolUSDRUBF})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	var ticks []source.Tick
	for tick := range ch {
		ticks = append(ticks, tick)
	}

	// First poll emits 3 ticks (LAST, BID, OFFER).
	// Subsequent polls with identical prices must not emit any more.
	if len(ticks) != 3 {
		t.Errorf("expected exactly 3 ticks (one set after first poll), got %d", len(ticks))
	}
	if atomic.LoadInt32(&callCount) < 2 {
		t.Error("expected at least 2 HTTP calls to verify deduplication")
	}
}

func TestSource_ErrorResilienceContinuesPolling(t *testing.T) {
	// Server returns errors initially, then valid data.
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, issResponse("USDRUBF", 82.0, 81.99, 82.01))
	}))
	defer srv.Close()

	s := newTestSource(srv.URL)
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	ch, err := s.Subscribe(ctx, []string{source.SymbolUSDRUBF})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	var got source.Tick
	select {
	case got = <-ch:
	case <-ctx.Done():
		t.Fatal("no tick received after error recovery")
	}

	if got.Price == 0 {
		t.Error("tick price must not be zero")
	}
}

func TestSource_CloseStopsGoroutines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, issResponse("USDRUBF", 81.91, 81.90, 81.95))
	}))
	defer srv.Close()

	s := newTestSource(srv.URL)

	ch, err := s.Subscribe(context.Background(), []string{source.SymbolUSDRUBF})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Drain first tick then close.
	<-ch
	s.Close()

	// Channel must close within a reasonable time after Close().
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Error("channel not closed after Close()")
	}
}

func TestSource_DoubleSubscribeReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, issResponse("USDRUBF", 81.91, 81.90, 81.95))
	}))
	defer srv.Close()

	s := newTestSource(srv.URL)
	defer s.Close()

	ctx := context.Background()
	if _, err := s.Subscribe(ctx, []string{source.SymbolUSDRUBF}); err != nil {
		t.Fatalf("first subscribe: %v", err)
	}
	if _, err := s.Subscribe(ctx, []string{source.SymbolUSDRUBF}); err == nil {
		t.Error("expected error on second Subscribe call")
	}
}
