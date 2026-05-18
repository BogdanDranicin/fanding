package forex_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/source"
	"github.com/funding-service/backend/internal/source/forex"
)

const testAPIKey = "test-key"

func newTestSource(baseURL string) *forex.Source {
	return forex.NewWithBaseURL(testAPIKey, baseURL, 20*time.Millisecond, zerolog.Nop())
}

// multiResponse mimics TwelveData multi-symbol /price response.
func multiResponse(eurusd, usdcnh string) string {
	return fmt.Sprintf(`{"EUR/USD":{"price":%q},"USD/CNH":{"price":%q}}`, eurusd, usdcnh)
}

func TestSource_Name(t *testing.T) {
	s := forex.New("key", time.Second, zerolog.Nop())
	if s.Name() != "twelvedata" {
		t.Errorf("expected twelvedata, got %s", s.Name())
	}
}

func TestSource_NoAPIKey_ReturnsError(t *testing.T) {
	s := forex.NewWithBaseURL("", "http://localhost", time.Second, zerolog.Nop())
	_, err := s.Subscribe(context.Background(), []string{source.SymbolEURUSD})
	if err == nil {
		t.Fatal("expected error when API key is empty")
	}
}

func TestSource_UnknownSymbol_ReturnsError(t *testing.T) {
	s := forex.New("key", time.Second, zerolog.Nop())
	_, err := s.Subscribe(context.Background(), []string{"UNKNOWN"})
	if err == nil {
		t.Fatal("expected error for unknown symbol")
	}
}

func TestSource_TicksDelivered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, multiResponse("1.08500", "7.24100"))
	}))
	defer srv.Close()

	s := newTestSource(srv.URL)
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	ch, err := s.Subscribe(ctx, []string{source.SymbolEURUSD, source.SymbolUSDCNH})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	var ticks []source.Tick
	for tick := range ch {
		ticks = append(ticks, tick)
		if len(ticks) >= 2 {
			break
		}
	}

	if len(ticks) < 2 {
		t.Fatalf("expected at least 2 ticks, got %d", len(ticks))
	}

	seen := make(map[string]float64)
	for _, tk := range ticks {
		seen[tk.Symbol] = tk.Price
		if tk.Kind != source.KindLastPrice {
			t.Errorf("expected KindLastPrice, got %v", tk.Kind)
		}
		if tk.Source != "twelvedata" {
			t.Errorf("expected source=twelvedata, got %s", tk.Source)
		}
	}
	if seen[source.SymbolEURUSD] != 1.085 {
		t.Errorf("EUR/USD: want 1.085, got %v", seen[source.SymbolEURUSD])
	}
	if seen[source.SymbolUSDCNH] != 7.241 {
		t.Errorf("USD/CNH: want 7.241, got %v", seen[source.SymbolUSDCNH])
	}
}

func TestSource_Deduplication(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, multiResponse("1.08500", "7.24100"))
	}))
	defer srv.Close()

	s := newTestSource(srv.URL)
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	ch, err := s.Subscribe(ctx, []string{source.SymbolEURUSD, source.SymbolUSDCNH})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	var ticks []source.Tick
	for tick := range ch {
		ticks = append(ticks, tick)
	}

	// Only 2 ticks (one per symbol) should arrive; subsequent polls are identical.
	if len(ticks) != 2 {
		t.Errorf("expected 2 ticks (first poll only), got %d", len(ticks))
	}
	if atomic.LoadInt32(&callCount) < 2 {
		t.Error("expected at least 2 HTTP calls to verify deduplication")
	}
}

func TestSource_ErrorResilienceContinuesPolling(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, multiResponse("1.09000", "7.25000"))
	}))
	defer srv.Close()

	s := newTestSource(srv.URL)
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	ch, err := s.Subscribe(ctx, []string{source.SymbolEURUSD})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	select {
	case tick := <-ch:
		if tick.Price == 0 {
			t.Error("price must not be zero")
		}
	case <-ctx.Done():
		t.Fatal("no tick received after error recovery")
	}
}

func TestSource_CloseStopsChannel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, multiResponse("1.08500", "7.24100"))
	}))
	defer srv.Close()

	s := newTestSource(srv.URL)
	ch, err := s.Subscribe(context.Background(), []string{source.SymbolEURUSD})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	<-ch // wait for first tick
	s.Close()

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

func TestSource_APIKeyInURL(t *testing.T) {
	var receivedQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, multiResponse("1.08500", "7.24100"))
	}))
	defer srv.Close()

	s := newTestSource(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if _, err := s.Subscribe(ctx, []string{source.SymbolEURUSD, source.SymbolUSDCNH}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	<-ctx.Done()

	if receivedQuery == "" {
		t.Fatal("no request received")
	}
	if !contains(receivedQuery, "apikey="+testAPIKey) {
		t.Errorf("API key not found in query: %s", receivedQuery)
	}
	if !contains(receivedQuery, "EUR%2FUSD") && !contains(receivedQuery, "EUR/USD") {
		t.Errorf("EUR/USD not found in query: %s", receivedQuery)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub ||
		len(s) > 0 && func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
